package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/restore/artifact"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
)

func TestBackupStagerResolvesNestedArchiveWithoutWritingMemberPaths(t *testing.T) {
	payload := []byte("native SQL backup fixture")
	tarBytes := makeTar(t, "backup/nested/database.bak", payload)
	zipBytes := makeZip(t, "outer/backup.tar", tarBytes)
	stageDirectory := t.TempDir()
	stageStore, err := staging.New(stageDirectory, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakeArtifactOpener{data: zipBytes}
	stager, err := newBackupStager(source, stageStore, artifact.Limits{
		MaxInputBytes: 1 << 20, MaxExpandedBytes: 1 << 20, MaxTemporaryBytes: 2 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := stager.Stage(context.Background(), "run-123/backup", "")
	if err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if result.Kind != staging.KindBAK || result.Size != int64(len(payload)) || source.opened != "run-123/backup" {
		t.Fatalf("staged result/source = %#v / %q", result, source.opened)
	}
	file, _, err := stageStore.Open(result.ArtifactID, result.Kind)
	if err != nil {
		t.Fatalf("stageStore.Open() error = %v", err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("staged payload = %q, error = %v", got, err)
	}
	entries, err := os.ReadDir(stageDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || strings.Contains(entries[0].Name(), "backup") {
		t.Fatalf("stage directory contains unexpected member paths: %#v", entries)
	}
}

func TestBackupStagerAmbiguousRequiresMemberPath(t *testing.T) {
	zipBytes := makeZipMembers(t, map[string][]byte{
		"shop/db-a.bak": []byte("aaa"),
		"shop/db-b.bak": []byte("bbb"),
	})
	stageStore, err := staging.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	stager, err := newBackupStager(&fakeArtifactOpener{data: zipBytes}, stageStore, artifact.Limits{
		MaxInputBytes: 1 << 20, MaxExpandedBytes: 1 << 20, MaxTemporaryBytes: 2 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	listed, err := stager.ListCandidates(context.Background(), "run/archive.zip")
	if err != nil || len(listed.Candidates) != 2 {
		t.Fatalf("ListCandidates() = %#v, err %v", listed, err)
	}
	_, err = stager.Stage(context.Background(), "run/archive.zip", "")
	var ambiguous *ambiguousArchiveError
	if !errors.As(err, &ambiguous) || len(ambiguous.Candidates) != 2 {
		t.Fatalf("Stage() error = %v, want ambiguous with 2 candidates", err)
	}
	result, err := stager.Stage(context.Background(), "run/archive.zip", "shop/db-b.bak")
	if err != nil {
		t.Fatalf("Stage(member_path) error = %v", err)
	}
	if result.Kind != staging.KindBAK || result.Size != 3 {
		t.Fatalf("staged = %#v", result)
	}
}

type fakeArtifactOpener struct {
	data   []byte
	opened string
}

func (source *fakeArtifactOpener) Open(_ context.Context, id string) (io.ReadCloser, error) {
	source.opened = id
	return io.NopCloser(bytes.NewReader(source.data)), nil
}

func makeTar(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func makeZip(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	return makeZipMembers(t, map[string][]byte{name: data})
}

func makeZipMembers(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, data := range members {
		member, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
