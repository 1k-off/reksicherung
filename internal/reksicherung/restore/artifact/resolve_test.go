package artifact

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"testing"
)

const testBackup = "native SQL Server backup bytes"

func TestResolvePlainAndNestedArtifacts(t *testing.T) {
	bacpac := makeBACPAC(t)
	zipBackup := makeZIP(t, map[string][]byte{"nested/path/database.bak": []byte(testBackup)})
	tarContainingZip := makeTarGzip(t, map[string][]byte{"level1/backup.zip": zipBackup})
	tarWithWrapperAndTerminal := makeTarGzip(t, map[string][]byte{
		"aos-default/archive.tar": []byte("not-a-real-tar"),
		"aos-default/db.bacpac":   bacpac,
	})

	tests := []struct {
		name     string
		fileName string
		data     []byte
		kind     Kind
		want     []byte
	}{
		{name: "plain BAK", data: []byte(testBackup), kind: KindSQLBackup, want: []byte(testBackup)},
		{name: "plain BACPAC", data: bacpac, kind: KindBACPAC, want: bacpac},
		{name: "ZIP with nested BAK path", data: zipBackup, kind: KindSQLBackup, want: []byte(testBackup)},
		{name: "gzip tar containing ZIP", data: tarContainingZip, kind: KindSQLBackup, want: []byte(testBackup)},
		{name: "gzip tar prefers bacpac over nested tar", data: tarWithWrapperAndTerminal, kind: KindBACPAC, want: bacpac},
		{name: "plain SQL dump", data: []byte("-- MySQL dump\n"), fileName: "app.sql", kind: KindSQL, want: []byte("-- MySQL dump\n")},
		{name: "gzip SQL dump", data: gzipBytes(t, []byte("CREATE TABLE t(i INT);\n")), fileName: "app.sql.gz", kind: KindSQL, want: []byte("CREATE TABLE t(i INT);\n")},
		{name: "ZIP nested SQL", data: makeZIP(t, map[string][]byte{"db/app.sql": []byte("USE `app`;\n")}), kind: KindSQL, want: []byte("USE `app`;\n")},
		{name: "ZIP nested gzip SQL", data: makeZIP(t, map[string][]byte{"db/app.sql.gz": gzipBytes(t, []byte("USE `app`;\n"))}), kind: KindSQL, want: []byte("USE `app`;\n")},
		{name: "gzip tar nested SQL", data: makeTarGzip(t, map[string][]byte{"dump/app.sql": []byte("SELECT 1;\n")}), kind: KindSQL, want: []byte("SELECT 1;\n")},
		{name: "unknown extension still maps to BAK", data: []byte(testBackup), fileName: "backup.dat", kind: KindSQLBackup, want: []byte(testBackup)},
		{name: "uppercase SQL extension", data: []byte("-- dump\n"), fileName: "APP.SQL", kind: KindSQL, want: []byte("-- dump\n")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tempDir := t.TempDir()
			fileName := test.fileName
			if fileName == "" {
				fileName = "backup"
			}
			resolved, kind, err := Resolve(context.Background(), io.NopCloser(bytes.NewReader(test.data)), fileName, testLimits(tempDir))
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if kind != test.kind {
				t.Fatalf("Resolve() kind = %q, want %q", kind, test.kind)
			}
			got, err := io.ReadAll(resolved)
			if err != nil {
				t.Fatalf("read resolved artifact: %v", err)
			}
			if !bytes.Equal(got, test.want) {
				t.Fatalf("resolved contents = %q, want %q", got, test.want)
			}
			if entries, err := os.ReadDir(tempDir); err != nil || len(entries) != 1 {
				t.Fatalf("temporary files before Close() = %v, err %v; want one", entries, err)
			}
			if err := resolved.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if entries, err := os.ReadDir(tempDir); err != nil || len(entries) != 0 {
				t.Fatalf("temporary files after Close() = %v, err %v; want none", entries, err)
			}
		})
	}
}

func TestListCandidatesAndResolveWithMemberPath(t *testing.T) {
	data := makeZIP(t, map[string][]byte{
		"shop/db-a.bak": []byte("aaa"),
		"shop/db-b.bak": []byte("bbb"),
	})
	listed, err := ListCandidates(context.Background(), io.NopCloser(bytes.NewReader(data)), "archive.zip", testLimits(t.TempDir()))
	if err != nil {
		t.Fatalf("ListCandidates() error = %v", err)
	}
	if len(listed.Candidates) != 2 || listed.Truncated {
		t.Fatalf("ListCandidates() = %#v", listed)
	}
	_, _, err = Resolve(context.Background(), io.NopCloser(bytes.NewReader(data)), "archive.zip", testLimits(t.TempDir()))
	ambiguous, ok := AsAmbiguous(err)
	if !ok || len(ambiguous.Candidates) != 2 {
		t.Fatalf("Resolve() error = %v, want AmbiguousError", err)
	}
	resolved, kind, err := ResolveWith(context.Background(), io.NopCloser(bytes.NewReader(data)), "archive.zip", testLimits(t.TempDir()), ResolveOptions{
		MemberPath: "shop/db-a.bak",
	})
	if err != nil {
		t.Fatalf("ResolveWith() error = %v", err)
	}
	defer resolved.Close()
	got, err := io.ReadAll(resolved)
	if err != nil || kind != KindSQLBackup || string(got) != "aaa" {
		t.Fatalf("ResolveWith() kind=%q data=%q err=%v", kind, got, err)
	}
}

func TestResolveRejectsAmbiguousUnsafeAndMalformedArchives(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{
			name: "multiple backup candidates",
			data: makeZIP(t, map[string][]byte{"a.bak": []byte("a"), "b.bak": []byte("b")}),
			want: ErrAmbiguousArchive,
		},
		{
			name: "traversal member",
			data: makeZIP(t, map[string][]byte{"../outside.bak": []byte("x")}),
			want: ErrInvalidArchive,
		},
		{
			name: "no supported artifact",
			data: makeZIP(t, map[string][]byte{"notes.txt": []byte("not a backup")}),
			want: ErrNoArtifact,
		},
		{
			name: "unsafe path in BACPAC-shaped ZIP",
			data: makeZIP(t, map[string][]byte{
				"model.xml": []byte("<model/>"), "origin.xml": []byte("<origin/>"),
				"../outside.txt": []byte("unsafe"),
			}),
			want: ErrInvalidArchive,
		},
		{name: "truncated ZIP", data: []byte("PK\x03\x04truncated"), want: ErrInvalidArchive},
		{name: "corrupt gzip tar", data: []byte{0x1f, 0x8b, 0x08, 0x00}, want: ErrInvalidArchive},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := Resolve(context.Background(), io.NopCloser(bytes.NewReader(test.data)), "backup.zip", testLimits(t.TempDir()))
			if !errors.Is(err, test.want) {
				t.Fatalf("Resolve() error = %v, want errors.Is(_, %v)", err, test.want)
			}
		})
	}
}

func TestResolveEnforcesByteAndNestingLimits(t *testing.T) {
	inner := makeZIP(t, map[string][]byte{"database.bak": []byte(testBackup)})
	outer := makeZIP(t, map[string][]byte{"nested.zip": inner})

	tests := []struct {
		name   string
		data   []byte
		limits Limits
		want   error
	}{
		{
			name:   "input limit",
			data:   inner,
			limits: Limits{MaxInputBytes: int64(len(inner) - 1), MaxExpandedBytes: 1 << 20, MaxTemporaryBytes: 1 << 20, TempDir: t.TempDir()},
			want:   ErrLimitExceeded,
		},
		{
			name:   "expanded limit",
			data:   inner,
			limits: Limits{MaxInputBytes: 1 << 20, MaxExpandedBytes: 4, MaxTemporaryBytes: 1 << 20, TempDir: t.TempDir()},
			want:   ErrLimitExceeded,
		},
		{
			name:   "temporary disk limit",
			data:   inner,
			limits: Limits{MaxInputBytes: 1 << 20, MaxExpandedBytes: 1 << 20, MaxTemporaryBytes: int64(len(inner) + len(testBackup) - 1), TempDir: t.TempDir()},
			want:   ErrLimitExceeded,
		},
		{
			name: "nesting limit",
			data: outer,
			limits: Limits{
				MaxInputBytes: 1 << 20, MaxExpandedBytes: 1 << 20, MaxTemporaryBytes: 2 << 20,
				MaxNestingDepth: 1, TempDir: t.TempDir(),
			},
			want: ErrNestingTooDeep,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := Resolve(context.Background(), io.NopCloser(bytes.NewReader(test.data)), "backup", test.limits)
			if !errors.Is(err, test.want) {
				t.Fatalf("Resolve() error = %v, want errors.Is(_, %v)", err, test.want)
			}
		})
	}
}

func TestResolveRejectsTarLinksAndHonorsCancellation(t *testing.T) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "link.bak", Typeflag: tar.TypeSymlink, Linkname: "target.bak"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err := Resolve(context.Background(), io.NopCloser(bytes.NewReader(buffer.Bytes())), "backup.tar", testLimits(t.TempDir()))
	if !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("Resolve(symlink) error = %v, want ErrInvalidArchive", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = Resolve(ctx, io.NopCloser(bytes.NewReader([]byte(testBackup))), "backup.bak", testLimits(t.TempDir()))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve(canceled) error = %v, want context.Canceled", err)
	}
}

func TestResolveSQLFailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		data     []byte
		limits   Limits
		want     error
	}{
		{
			name: "ZIP with BAK and SQL is ambiguous", fileName: "archive.zip",
			data:   makeZIP(t, map[string][]byte{"a.bak": []byte("a"), "b.sql": []byte("b")}),
			limits: testLimits(t.TempDir()), want: ErrAmbiguousArchive,
		},
		{
			name: "ZIP with two SQL dumps is ambiguous", fileName: "archive.zip",
			data:   makeZIP(t, map[string][]byte{"a.sql": []byte("a"), "b.sql": []byte("b")}),
			limits: testLimits(t.TempDir()), want: ErrAmbiguousArchive,
		},
		{
			name: "corrupt gzip SQL", fileName: "app.sql.gz",
			data:   []byte{0x1f, 0x8b, 0x08, 0x00, 0x01, 0x02},
			limits: testLimits(t.TempDir()), want: ErrInvalidArchive,
		},
		{
			name: "gzip SQL expanded limit", fileName: "app.sql.gz",
			data:   gzipBytes(t, bytes.Repeat([]byte("x"), 1024)),
			limits: Limits{MaxInputBytes: 1 << 20, MaxExpandedBytes: 16, MaxTemporaryBytes: 1 << 20, TempDir: t.TempDir()},
			want:   ErrLimitExceeded,
		},
		{
			name: "gzip SQL decompressing to ZIP without artifact", fileName: "app.sql.gz",
			data:   gzipBytes(t, makeZIP(t, map[string][]byte{"notes.txt": []byte("x")})),
			limits: testLimits(t.TempDir()), want: ErrNoArtifact,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := Resolve(context.Background(), io.NopCloser(bytes.NewReader(test.data)), test.fileName, test.limits)
			if !errors.Is(err, test.want) {
				t.Fatalf("Resolve() error = %v, want errors.Is(_, %v)", err, test.want)
			}
			if entries, readErr := os.ReadDir(test.limits.TempDir); readErr != nil || len(entries) != 0 {
				t.Fatalf("temporary files after failure = %v, err %v; want none", entries, readErr)
			}
		})
	}
}

func TestKindHintForName(t *testing.T) {
	tests := map[string]Kind{
		"db/app.bak":    KindSQLBackup,
		"db/app.bacpac": KindBACPAC,
		"db/app.sql":    KindSQL,
		"db/APP.SQL.GZ": KindSQL,
		"db/app.sql.gz": KindSQL,
		"db/app.txt":    "",
	}
	for name, want := range tests {
		if got := kindHintForName(name); got != want {
			t.Errorf("kindHintForName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestListCandidatesIncludesSQL(t *testing.T) {
	data := makeZIP(t, map[string][]byte{"shop/a.sql": []byte("a"), "shop/b.sql.gz": gzipBytes(t, []byte("b"))})
	listed, err := ListCandidates(context.Background(), io.NopCloser(bytes.NewReader(data)), "archive.zip", testLimits(t.TempDir()))
	if err != nil || len(listed.Candidates) != 2 {
		t.Fatalf("ListCandidates() = %#v, err %v", listed, err)
	}
	for _, candidate := range listed.Candidates {
		if candidate.KindHint != KindSQL {
			t.Errorf("candidate %q hint = %q, want %q", candidate.Path, candidate.KindHint, KindSQL)
		}
	}
	resolved, kind, err := ResolveWith(context.Background(), io.NopCloser(bytes.NewReader(data)), "archive.zip", testLimits(t.TempDir()), ResolveOptions{MemberPath: "shop/b.sql.gz"})
	if err != nil {
		t.Fatalf("ResolveWith() error = %v", err)
	}
	defer resolved.Close()
	got, err := io.ReadAll(resolved)
	if err != nil || kind != KindSQL || string(got) != "b" {
		t.Fatalf("ResolveWith() kind=%q data=%q err=%v", kind, got, err)
	}
}

func gzipBytes(t *testing.T, contents []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func testLimits(tempDir string) Limits {
	return Limits{
		MaxInputBytes:     1 << 20,
		MaxExpandedBytes:  1 << 20,
		MaxTemporaryBytes: 2 << 20,
		TempDir:           tempDir,
	}
}

func makeZIP(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, contents := range members {
		member, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func makeBACPAC(t *testing.T) []byte {
	t.Helper()
	return makeZIP(t, map[string][]byte{
		"model.xml":  []byte("<model/>"),
		"origin.xml": []byte("<origin/>"),
	})
}

func makeTarGzip(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, contents := range members {
		if err := tarWriter.WriteHeader(&tar.Header{
			Name: name, Mode: 0600, Size: int64(len(contents)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
