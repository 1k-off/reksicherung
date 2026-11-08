package main

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/restore/artifact"
	resticrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/restic"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
)

func TestResticArtifactStagerSelectsOneFileAndResolvesArchive(t *testing.T) {
	payload := []byte("backup payload")
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	member, err := writer.Create("database.bak")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	store, err := staging.New(directory, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakeResticFileOpener{
		snapshot: resticrestore.Snapshot{ID: strings.Repeat("a", 64)},
		data:     archive.Bytes(),
	}
	stager, err := newResticArtifactStager(source, store, artifact.Limits{
		MaxInputBytes: 1 << 20, MaxExpandedBytes: 1 << 20, MaxTemporaryBytes: 2 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := stager.Stage(context.Background(), source.snapshot.ID, "/backups/db.zip")
	if err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if source.selection.Policy != resticrestore.SelectionID || source.selection.ID != source.snapshot.ID ||
		source.path != "/backups/db.zip" || result.SnapshotID != source.snapshot.ID ||
		result.Kind != staging.KindBAK || result.Size != int64(len(payload)) {
		t.Fatalf("stage args/result = %#v / %#v", source, result)
	}
	file, _, err := store.Open(result.ArtifactID, result.Kind)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("staged data = %q, error = %v", got, err)
	}
}

type fakeResticFileOpener struct {
	snapshot  resticrestore.Snapshot
	selection resticrestore.Selection
	path      string
	data      []byte
}

func (opener *fakeResticFileOpener) OpenBackupFile(_ context.Context, selection resticrestore.Selection, path string) (resticrestore.Snapshot, io.ReadCloser, error) {
	opener.selection, opener.path = selection, path
	return opener.snapshot, io.NopCloser(bytes.NewReader(opener.data)), nil
}
