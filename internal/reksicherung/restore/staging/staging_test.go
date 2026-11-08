package staging

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAndOpenKeepsOpaqueArtifact(t *testing.T) {
	store, err := New(t.TempDir(), 64)
	if err != nil {
		t.Fatal(err)
	}
	source := &trackedReadCloser{Reader: strings.NewReader("native backup")}
	artifact, err := store.Save(context.Background(), source, KindBAK)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if !source.closed {
		t.Fatal("Save() did not close source")
	}
	if len(artifact.ID) != 32 || artifact.Kind != KindBAK || artifact.Size != int64(len("native backup")) {
		t.Fatalf("artifact = %#v", artifact)
	}
	file, opened, err := store.Open(artifact.ID, KindBAK)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "native backup" || opened != artifact {
		t.Fatalf("opened artifact = %q, %#v, %v", data, opened, err)
	}
}

func TestNewRequiresPrecreatedNonSymlinkDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created")
	if _, err := New(missing, 10); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New(missing) error = %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("New created the directory: %v", err)
	}
}

func TestSaveRejectsOverLimitAndDoesNotPublish(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(context.Background(), io.NopCloser(strings.NewReader("12345")), KindBAK); !errors.Is(err, ErrLimit) {
		t.Fatalf("Save() error = %v, want ErrLimit", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging directory = %#v, %v", entries, err)
	}
}

func TestOpenRejectsTraversalAndSymlink(t *testing.T) {
	store, err := New(t.TempDir(), 64)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", strings.Repeat("a", 31), strings.Repeat("A", 32)} {
		if _, _, err := store.Open(id, KindBAK); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Open(%q) error = %v", id, err)
		}
	}

	id := strings.Repeat("a", 32)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("not a staged file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(store.directory, id+"."+KindBAK)); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, _, err := store.Open(id, KindBAK); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("Open(symlink) error = %v", err)
	}
}

type trackedReadCloser struct {
	*strings.Reader
	closed bool
}

func (reader *trackedReadCloser) Close() error {
	reader.closed = true
	return nil
}
