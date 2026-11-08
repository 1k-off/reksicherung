package runregistry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type fakeDatabaseDropper struct {
	droppedRunID string
	err          error
}

func (dropper *fakeDatabaseDropper) DropDatabase(_ context.Context, runID string) error {
	dropper.droppedRunID = runID
	return dropper.err
}

func TestCleanupHeldRemovesAndRecordsDatabase(t *testing.T) {
	store := openCleanupTestStore(t)
	createHeldRun(t, store)
	dropper := &fakeDatabaseDropper{}

	record, err := CleanupHeld(context.Background(), store, dropper, "run-123")
	if err != nil {
		t.Fatalf("CleanupHeld() error = %v", err)
	}
	if dropper.droppedRunID != "run-123" {
		t.Fatalf("dropped run ID = %q, want run-123", dropper.droppedRunID)
	}
	if record.Status != StatusFinished || record.CleanupStatus != CleanupSucceeded {
		t.Fatalf("cleanup result = status %q, cleanup %q", record.Status, record.CleanupStatus)
	}
}

func TestCleanupHeldRecordsFailureAndAllowsRetry(t *testing.T) {
	store := openCleanupTestStore(t)
	createHeldRun(t, store)
	dropper := &fakeDatabaseDropper{err: errors.New("sql server unavailable")}

	record, err := CleanupHeld(context.Background(), store, dropper, "run-123")
	if err == nil {
		t.Fatal("CleanupHeld() error = nil, want drop error")
	}
	if record.Status != StatusFinalizing || record.CleanupStatus != CleanupFailed {
		t.Fatalf("failed cleanup state = status %q, cleanup %q", record.Status, record.CleanupStatus)
	}

	dropper.err = nil
	record, err = CleanupHeld(context.Background(), store, dropper, "run-123")
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
	record.DatabaseName = "reksicherung_abc"
	record.Status = StatusFinished
	record.Verdict = VerdictPass
	record.CleanupStatus = CleanupSucceeded
	if _, err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	dropper := &fakeDatabaseDropper{}
	if _, err := CleanupHeld(context.Background(), store, dropper, record.ID); !errors.Is(err, ErrNotHeldForReview) {
		t.Fatalf("CleanupHeld(automatic) error = %v, want ErrNotHeldForReview", err)
	}
	if dropper.droppedRunID != "" {
		t.Fatalf("automatic database was dropped: %q", dropper.droppedRunID)
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
	record.DatabaseName = "reksicherung_abc"
	record.Status = StatusFinished
	record.Verdict = VerdictPass
	record.CleanupStatus = CleanupRetained
	if _, err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}
