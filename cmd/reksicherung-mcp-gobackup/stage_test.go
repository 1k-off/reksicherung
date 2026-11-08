package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
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
	result, err := stager.Stage(context.Background(), "run-123/backup")
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
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	member, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
