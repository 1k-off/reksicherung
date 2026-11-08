package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/runregistry"
	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

type fakeCleanupRuntime struct {
	removedID string
}

func (runtime *fakeCleanupRuntime) Remove(_ context.Context, target container.Container) error {
	runtime.removedID = target.ID
	return nil
}

func TestCLICleanupHeldRun(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "runs.db")
	seedHeldRun(t, registryPath)
	runtime := &fakeCleanupRuntime{}
	var stdout, stderr bytes.Buffer

	err := runCleanupWithDependencies(
		context.Background(),
		[]string{"-registry", registryPath, "-run-id", "run-123"},
		&stdout,
		&stderr,
		func(path string) (cleanupStore, error) { return runregistry.Open(path) },
		func() cleanupRuntime { return runtime },
	)
	if err != nil {
		t.Fatalf("runCleanupWithDependencies() error = %v, stderr = %q", err, stderr.String())
	}
	if runtime.removedID != "container-123" {
		t.Fatalf("removed ID = %q, want container-123", runtime.removedID)
	}
	if got, want := stdout.String(), "cleaned run run-123; container container-123 removed\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestCLICleanupRequiresExplicitRegistryAndRunID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runCleanupWithDependencies(
		context.Background(),
		[]string{"-run-id", "run-123"},
		&stdout,
		&stderr,
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("runCleanupWithDependencies() succeeded without registry path")
	}
}

func seedHeldRun(t *testing.T, path string) {
	t.Helper()
	store, err := runregistry.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record, err := runregistry.NewRecord("run-123", runregistry.CleanupHold, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	record.ContainerID = "container-123"
	record.Status = runregistry.StatusFinished
	record.Verdict = runregistry.VerdictPass
	record.CleanupStatus = runregistry.CleanupRetained
	if _, err := store.Create(context.Background(), record); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
