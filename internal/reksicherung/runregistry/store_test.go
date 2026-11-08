package runregistry

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestStorePersistsAndReopensRun(t *testing.T) {
	t.Parallel()

	path := t.TempDir() + "/state/runs.db"
	now := time.Now().UTC().Truncate(time.Second)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	record := mustNewRecord(t, "run-a", CleanupAutomatic, now, now.Add(time.Minute))
	created, err := store.Create(context.Background(), record)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Revision != 1 {
		t.Fatalf("created revision = %d, want 1", created.Revision)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.Get(context.Background(), "run-a")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != created.ID || got.Revision != created.Revision || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("Get() = %#v, want persisted %#v", got, created)
	}
}

func TestStoreUpdateEnforcesTransitionsAndCompareAndSwap(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	created, err := store.Create(context.Background(), mustNewRecord(t, "run-a", CleanupAutomatic, now, now.Add(time.Minute)))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	preparing, err := store.Update(context.Background(), created.ID, created.Revision, func(record *Record) error {
		record.Status = StatusPreparing
		record.DatabaseName = "reksicherung_abc"
		return nil
	})
	if err != nil {
		t.Fatalf("valid update error = %v", err)
	}
	if preparing.Revision != created.Revision+1 {
		t.Fatalf("revision = %d, want %d", preparing.Revision, created.Revision+1)
	}

	_, err = store.Update(context.Background(), preparing.ID, created.Revision, func(record *Record) error {
		record.Status = StatusRestoring
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error = %v, want ErrConflict", err)
	}

	_, err = store.Update(context.Background(), preparing.ID, preparing.Revision, func(record *Record) error {
		record.Status = StatusFinished
		record.Verdict = VerdictPass
		return nil
	})
	if !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("invalid transition error = %v, want ErrInvalidRecord", err)
	}

	_, err = store.Update(context.Background(), preparing.ID, preparing.Revision, func(record *Record) error {
		record.DatabaseName = "reksicherung_other"
		return nil
	})
	if !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("database name mutation error = %v, want ErrInvalidRecord", err)
	}
}

func TestStoreUpdateAllowsValidRunAndCleanupLifecycle(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	record, err := store.Create(context.Background(), mustNewRecord(t, "run-lifecycle", CleanupAutomatic, now, now.Add(time.Minute)))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	record = updateRecord(t, store, record, func(record *Record) {
		record.Status = StatusPreparing
		record.DatabaseName = "reksicherung_abc"
	})
	record = updateRecord(t, store, record, func(record *Record) { record.Status = StatusRestoring })
	record = updateRecord(t, store, record, func(record *Record) { record.Status = StatusValidating })
	record = updateRecord(t, store, record, func(record *Record) {
		record.Status = StatusFinalizing
		record.Verdict = VerdictPass
		record.CleanupStatus = CleanupSucceeded
	})
	record = updateRecord(t, store, record, func(record *Record) { record.Status = StatusFinished })

	if record.Status != StatusFinished || record.Verdict != VerdictPass || record.CleanupStatus != CleanupSucceeded {
		t.Fatalf("final record = %#v", record)
	}
}

func TestListExpiredIncludesOnlyAutomaticRunsWithPendingCleanup(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	records := []Record{
		mustNewRecord(t, "auto-b", CleanupAutomatic, expired.Add(-time.Minute), expired),
		mustNewRecord(t, "auto-a", CleanupAutomatic, expired.Add(-time.Minute), expired),
		mustNewRecord(t, "held", CleanupHold, expired.Add(-time.Minute), expired),
		mustNewRecord(t, "future", CleanupAutomatic, now, future),
		mustNewRecord(t, "no-container", CleanupAutomatic, expired.Add(-time.Minute), expired),
		mustNewRecord(t, "cleaned", CleanupAutomatic, expired.Add(-time.Minute), expired),
	}
	for _, record := range records {
		created, err := store.Create(context.Background(), record)
		if err != nil {
			t.Fatalf("Create(%s) error = %v", record.ID, err)
		}
		switch created.ID {
		case "auto-a", "auto-b", "held", "cleaned":
			created = updateRecord(t, store, created, func(record *Record) {
				record.DatabaseName = "reksicherung_" + record.ID
				if record.ID == "held" {
					record.CleanupStatus = CleanupRetained
				}
				if record.ID == "cleaned" {
					record.CleanupStatus = CleanupSucceeded
				}
			})
		}
	}

	got, err := store.ListExpired(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("ListExpired() error = %v", err)
	}
	if len(got) != 2 || got[0].ID != "auto-a" || got[1].ID != "auto-b" {
		t.Fatalf("ListExpired() IDs = %#v, want [auto-a auto-b]", recordIDs(got))
	}

	got, err = store.ListExpired(context.Background(), now, 1)
	if err != nil {
		t.Fatalf("ListExpired(limit=1) error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "auto-a" {
		t.Fatalf("ListExpired(limit=1) = %#v, want only auto-a", recordIDs(got))
	}
}

func TestConcurrentUpdateAllowsOnlyOneWriterForRevision(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	record, err := store.Create(context.Background(), mustNewRecord(t, "run-race", CleanupAutomatic, now, now.Add(time.Minute)))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	const writers = 8
	var wait sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	conflicts := 0
	for i := 0; i < writers; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := store.Update(context.Background(), record.ID, record.Revision, func(next *Record) error {
				next.AironRunID = fmt.Sprintf("airon-%d", index)
				return nil
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrConflict):
				conflicts++
			default:
				t.Errorf("Update() error = %v, want success or ErrConflict", err)
			}
		}(i)
	}
	wait.Wait()
	if successes != 1 || conflicts != writers-1 {
		t.Fatalf("successes/conflicts = %d/%d, want 1/%d", successes, conflicts, writers-1)
	}
}

func TestStoreHonorsCanceledContextAndMissingRuns(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Get(ctx, "run-missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := store.Get(context.Background(), "run-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrNotFound", err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir() + "/runs.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func mustNewRecord(t *testing.T, id string, mode CleanupMode, created, expiry time.Time) Record {
	t.Helper()
	record, err := NewRecord(id, mode, created, expiry)
	if err != nil {
		t.Fatalf("NewRecord() error = %v", err)
	}
	return record
}

func updateRecord(t *testing.T, store *Store, record Record, mutate func(*Record)) Record {
	t.Helper()
	updated, err := store.Update(context.Background(), record.ID, record.Revision, func(next *Record) error {
		mutate(next)
		return nil
	})
	if err != nil {
		t.Fatalf("Update(%s) error = %v", record.ID, err)
	}
	return updated
}

func recordIDs(records []Record) []string {
	ids := make([]string, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	return ids
}
