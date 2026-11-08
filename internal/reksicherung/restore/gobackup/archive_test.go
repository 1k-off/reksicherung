package gobackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

var testLimits = Limits{
	MaxCompressedBytes:   1 << 20,
	MaxUncompressedBytes: 1 << 20,
	MaxBACPACBytes:       1 << 18,
}

func TestExtractBACPAC(t *testing.T) {
	tests := []struct {
		name    string
		format  Format
		entries []tarEntry
		want    string
	}{
		{
			name:   "plain tar",
			format: FormatTar,
			entries: []tarEntry{
				{name: "backup/readme.txt", body: "metadata"},
				{name: "backup/database.bacpac", body: "bacpac contents"},
			},
			want: "bacpac contents",
		},
		{
			name:    "gzip tar",
			format:  FormatTarGzip,
			entries: []tarEntry{{name: "database.bacpac", body: "compressed bacpac"}},
			want:    "compressed bacpac",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := makeArchive(t, test.entries, test.format)
			var output bytes.Buffer
			if err := ExtractBACPAC(context.Background(), bytes.NewReader(archive), &output, test.format, testLimits); err != nil {
				t.Fatalf("ExtractBACPAC() error = %v", err)
			}
			if got := output.String(); got != test.want {
				t.Fatalf("output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExtractBACPACRejectsInvalidArchives(t *testing.T) {
	tests := []struct {
		name    string
		entries []tarEntry
		archive []byte
		format  Format
		wantErr error
	}{
		{
			name:    "traversal",
			entries: []tarEntry{{name: "../database.bacpac", body: "data"}},
			format:  FormatTar,
			wantErr: ErrUnsafePath,
		},
		{
			name:    "absolute path",
			entries: []tarEntry{{name: "/database.bacpac", body: "data"}},
			format:  FormatTar,
			wantErr: ErrUnsafePath,
		},
		{
			name:    "backslash",
			entries: []tarEntry{{name: `folder\database.bacpac`, body: "data"}},
			format:  FormatTar,
			wantErr: ErrUnsafePath,
		},
		{
			name:    "symlink",
			entries: []tarEntry{{name: "database.bacpac", typeflag: tar.TypeSymlink}},
			format:  FormatTar,
		},
		{
			name:    "no BACPAC",
			entries: []tarEntry{{name: "readme.txt", body: "data"}},
			format:  FormatTar,
			wantErr: ErrNoBACPAC,
		},
		{
			name: "multiple BACPACs",
			entries: []tarEntry{
				{name: "one.bacpac", body: "one"},
				{name: "two.BACPAC", body: "two"},
			},
			format:  FormatTar,
			wantErr: ErrMultipleBACPAC,
		},
		{
			name:    "malformed tar",
			archive: []byte("not a tar archive"),
			format:  FormatTar,
		},
		{
			name:    "unsupported compression",
			archive: []byte{0x1f, 0x8b, 0x08, 0},
			format:  FormatTar,
			wantErr: ErrUnsupportedFormat,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := test.archive
			if archive == nil {
				archive = makeArchive(t, test.entries, test.format)
			}
			var output bytes.Buffer
			err := ExtractBACPAC(context.Background(), bytes.NewReader(archive), &output, test.format, testLimits)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("ExtractBACPAC() error = %v, want errors.Is(_, %v)", err, test.wantErr)
				}
			} else if err == nil {
				t.Fatal("ExtractBACPAC() error = nil, want failure")
			}
		})
	}
}

func TestExtractBACPACSizeLimits(t *testing.T) {
	archive := makeArchive(t, []tarEntry{{name: "database.bacpac", body: strings.Repeat("x", 128)}}, FormatTar)
	tests := []struct {
		name   string
		limits Limits
	}{
		{
			name:   "compressed",
			limits: Limits{MaxCompressedBytes: int64(len(archive) - 1), MaxUncompressedBytes: 1 << 20, MaxBACPACBytes: 1 << 18},
		},
		{
			name:   "uncompressed",
			limits: Limits{MaxCompressedBytes: 1 << 20, MaxUncompressedBytes: int64(len(archive) - 1), MaxBACPACBytes: 1 << 18},
		},
		{
			name:   "BACPAC",
			limits: Limits{MaxCompressedBytes: 1 << 20, MaxUncompressedBytes: 1 << 20, MaxBACPACBytes: 127},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ExtractBACPAC(context.Background(), bytes.NewReader(archive), io.Discard, FormatTar, test.limits)
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("ExtractBACPAC() error = %v, want ErrLimitExceeded", err)
			}
		})
	}
}

func TestExtractBACPACMalformedAndCanceledInput(t *testing.T) {
	t.Run("truncated gzip", func(t *testing.T) {
		archive := makeArchive(t, []tarEntry{{name: "database.bacpac", body: "content"}}, FormatTarGzip)
		archive = archive[:len(archive)-4]
		err := ExtractBACPAC(context.Background(), bytes.NewReader(archive), io.Discard, FormatTarGzip, testLimits)
		if err == nil {
			t.Fatal("ExtractBACPAC() error = nil, want malformed gzip failure")
		}
	})

	t.Run("gzip checksum", func(t *testing.T) {
		archive := makeArchive(t, []tarEntry{{name: "database.bacpac", body: "content"}}, FormatTarGzip)
		archive[len(archive)-8] ^= 0xff
		err := ExtractBACPAC(context.Background(), bytes.NewReader(archive), io.Discard, FormatTarGzip, testLimits)
		if err == nil {
			t.Fatal("ExtractBACPAC() error = nil, want gzip checksum failure")
		}
	})

	t.Run("nonpositive limits", func(t *testing.T) {
		limits := testLimits
		limits.MaxBACPACBytes = 0
		err := ExtractBACPAC(context.Background(), bytes.NewReader(nil), io.Discard, FormatTar, limits)
		if err == nil {
			t.Fatal("ExtractBACPAC() error = nil, want invalid limit failure")
		}
	})

	t.Run("canceled before reading", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		reader := &observedReader{reader: strings.NewReader("unused")}
		err := ExtractBACPAC(ctx, reader, io.Discard, FormatTar, testLimits)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ExtractBACPAC() error = %v, want context.Canceled", err)
		}
		if reader.reads != 0 {
			t.Fatalf("archive reads = %d, want 0", reader.reads)
		}
	})
}

type tarEntry struct {
	name     string
	body     string
	typeflag byte
}

func makeArchive(t *testing.T, entries []tarEntry, format Format) []byte {
	t.Helper()
	var output bytes.Buffer
	var writer io.Writer = &output
	var gzipWriter *gzip.Writer
	if format == FormatTarGzip {
		gzipWriter = gzip.NewWriter(&output)
		writer = gzipWriter
	}
	tarWriter := tar.NewWriter(writer)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Typeflag: typeflag, Mode: 0600, Size: int64(len(entry.body))}
		if typeflag == tar.TypeDir || typeflag == tar.TypeSymlink {
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader(%q): %v", entry.name, err)
		}
		if _, err := io.WriteString(tarWriter, entry.body); err != nil {
			t.Fatalf("write member %q: %v", entry.name, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if gzipWriter != nil {
		if err := gzipWriter.Close(); err != nil {
			t.Fatalf("close gzip writer: %v", err)
		}
	}
	return output.Bytes()
}

type observedReader struct {
	reader io.Reader
	reads  int
}

func (reader *observedReader) Read(buffer []byte) (int, error) {
	reader.reads++
	return reader.reader.Read(buffer)
}
