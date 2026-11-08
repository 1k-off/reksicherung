package runregistry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

type fakeContainerRemover struct {
	removedID string
	err       error
}

func (remover *fakeContainerRemover) Remove(_ context.Context, target container.Container) error {
	remover.removedID = target.ID
	return remover.err
}

func TestCleanupHeldRemovesAndRecordsContainer(t *testing.T) {
	store := openCleanupTestStore(t)
	createHeldRun(t, store)
	remover := &fakeContainerRemover{}

	record, err := CleanupHeld(context.Background(), store, remover, "run-123")
	if err != nil {
		t.Fatalf("CleanupHeld() error = %v", err)
	}
	if remover.removedID != "container-123" {
		t.Fatalf("removed ID = %q, want container-123", remover.removedID)
	}
	if record.Status != StatusFinished || record.CleanupStatus != CleanupSucceeded {
		t.Fatalf("cleanup result = status %q, cleanup %q", record.Status, record.CleanupStatus)
	}
}

func TestCleanupHeldRecordsFailureAndAllowsRetry(t *testing.T) {
	store := openCleanupTestStore(t)
	createHeldRun(t, store)
	remover := &fakeContainerRemover{err: errors.New("runtime unavailable")}

	record, err := CleanupHeld(context.Background(), store, remover, "run-123")
	if err == nil {
		t.Fatal("CleanupHeld() error = nil, want removal error")
	}
	if record.Status != StatusFinalizing || record.CleanupStatus != CleanupFailed {
		t.Fatalf("failed cleanup state = status %q, cleanup %q", record.Status, record.CleanupStatus)
	}

	remover.err = nil
	record, err = CleanupHeld(context.Background(), store, remover, "run-123")
	if err != nil {
		t.Fatalf("CleanupHeld(retry) error = %v", err)
	}
	if record.Status != StatusFinished || record.CleanupStatus != CleanupSucceeded {
		t.Fatalf("retry result = status %q, cleanup %q", record.Status, record.CleanupStatus)
	}
}

func TestCleanupHeldRejectsAutomaticRun(t *testing.T) {
	store := openCleanupTestStore(t)
	now := time.Now().UTC()
	record, err := NewRecord("run-123", CleanupAutomatic, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	record.ContainerID = "container-123"
	record.Status = StatusFinished
	record.Verdict = VerdictPass
	record.CleanupStatus = CleanupSucceeded
	if _, err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	remover := &fakeContainerRemover{}
	if _, err := CleanupHeld(context.Background(), store, remover, record.ID); !errors.Is(err, ErrNotHeldForReview) {
		t.Fatalf("CleanupHeld(automatic) error = %v, want ErrNotHeldForReview", err)
	}
	if remover.removedID != "" {
		t.Fatalf("automatic container was removed: %q", remover.removedID)
	}
}

func openCleanupTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func createHeldRun(t *testing.T, store *Store) {
	t.Helper()
	now := time.Now().UTC()
	record, err := NewRecord("run-123", CleanupHold, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	record.ContainerID = "container-123"
	record.Status = StatusFinished
	record.Verdict = VerdictPass
	record.CleanupStatus = CleanupRetained
	if _, err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}
