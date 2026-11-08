package runregistry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestReconcileDropsExpiredDatabaseAndClosesInterruptedRun(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	record := createExpiringRun(t, store, "run-reconcile", CleanupAutomatic, "reksicherung_abc", now)
	dropper := &fakeDropper{}
	reconciler := Reconciler{Store: store, Databases: dropper}

	outcomes, err := reconciler.Reconcile(context.Background(), now)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].RunID != record.ID || outcomes[0].Status != CleanupSucceeded || outcomes[0].Err != nil {
		t.Fatalf("Reconcile() outcomes = %#v", outcomes)
	}
	if len(dropper.ids) != 1 || dropper.ids[0] != record.ID {
		t.Fatalf("dropped IDs = %#v, want %q", dropper.ids, record.ID)
	}
	got, err := store.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != StatusFinished || got.Verdict != VerdictInconclusive || got.CleanupStatus != CleanupSucceeded {
		t.Fatalf("reconciled record = %#v", got)
	}
}

func TestReconcileRetriesCleanupFailureAfterLeaseExpires(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	record := createExpiringRun(t, store, "run-retry", CleanupAutomatic, "reksicherung_def", now)
	dropper := &fakeDropper{failures: []error{errors.New("sql unavailable"), nil}}
	reconciler := Reconciler{Store: store, Databases: dropper, CleanupTimeout: 5 * time.Second}

	outcomes, err := reconciler.Reconcile(context.Background(), now)
	if err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != CleanupFailed || outcomes[0].Err == nil {
		t.Fatalf("first outcome = %#v, want cleanup failure", outcomes)
	}
	afterFailure, err := store.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Get() after failure: %v", err)
	}
	if afterFailure.Status != StatusFinalizing || afterFailure.Verdict != VerdictInconclusive || afterFailure.CleanupStatus != CleanupFailed {
		t.Fatalf("record after failure = %#v", afterFailure)
	}
	if len(dropper.ids) != 1 {
		t.Fatalf("drop calls = %d, want 1", len(dropper.ids))
	}

	retryAt := afterFailure.LeaseExpiresAt.Add(time.Second)
	outcomes, err = reconciler.Reconcile(context.Background(), retryAt)
	if err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != CleanupSucceeded {
		t.Fatalf("second outcome = %#v, want cleanup success", outcomes)
	}
	final, err := store.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Get() after success: %v", err)
	}
	if final.Status != StatusFinished || final.CleanupStatus != CleanupSucceeded || final.Verdict != VerdictInconclusive {
		t.Fatalf("final record = %#v", final)
	}
	if len(dropper.ids) != 2 {
		t.Fatalf("drop calls = %d, want 2", len(dropper.ids))
	}
}

func TestReconcileNeverAutomaticallyCleansHeldForReview(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	record := createExpiringRun(t, store, "run-held", CleanupHold, "reksicherung_held", now)
	_, err := store.Update(context.Background(), record.ID, record.Revision, func(record *Record) error {
		record.CleanupStatus = CleanupRetained
		return nil
	})
	if err != nil {
		t.Fatalf("mark held run retained: %v", err)
	}
	dropper := &fakeDropper{}
	outcomes, err := (Reconciler{Store: store, Databases: dropper}).Reconcile(context.Background(), now)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(outcomes) != 0 || len(dropper.ids) != 0 {
		t.Fatalf("Reconcile() outcomes/drops = %#v/%#v, want none", outcomes, dropper.ids)
	}
}

func TestConcurrentReconcilerClaimsCleanupOnlyOnce(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	createExpiringRun(t, store, "run-concurrent-cleanup", CleanupAutomatic, "reksicherung_race", now)
	dropper := &blockingDropper{started: make(chan struct{}), release: make(chan struct{})}
	reconciler := Reconciler{Store: store, Databases: dropper}

	firstDone := make(chan error, 1)
	go func() {
		_, err := reconciler.Reconcile(context.Background(), now)
		firstDone <- err
	}()
	<-dropper.started
	second, err := reconciler.Reconcile(context.Background(), now)
	if err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second outcomes = %#v, want no work while lease is held", second)
	}
	close(dropper.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if got := dropper.calls(); got != 1 {
		t.Fatalf("database drop calls = %d, want 1", got)
	}
}

func TestReconcileRespectsCanceledContextBeforeListing(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	dropper := &fakeDropper{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Reconciler{Store: store, Databases: dropper}).Reconcile(ctx, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconcile(canceled) error = %v, want context.Canceled", err)
	}
	if len(dropper.ids) != 0 {
		t.Fatalf("canceled reconciliation dropped databases: %#v", dropper.ids)
	}
}

func createExpiringRun(t *testing.T, store *Store, id string, mode CleanupMode, databaseName string, now time.Time) Record {
	t.Helper()
	createdAt := now.Add(-2 * time.Minute)
	record, err := store.Create(context.Background(), mustNewRecord(t, id, mode, createdAt, now.Add(-time.Minute)))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	updated, err := store.Update(context.Background(), id, record.Revision, func(record *Record) error {
		record.DatabaseName = databaseName
		return nil
	})
	if err != nil {
		t.Fatalf("set database name: %v", err)
	}
	return updated
}

type fakeDropper struct {
	mu       sync.Mutex
	ids      []string
	failures []error
}

func (dropper *fakeDropper) DropDatabase(_ context.Context, runID string) error {
	dropper.mu.Lock()
	defer dropper.mu.Unlock()
	dropper.ids = append(dropper.ids, runID)
	if len(dropper.failures) == 0 {
		return nil
	}
	err := dropper.failures[0]
	dropper.failures = dropper.failures[1:]
	return err
}

type blockingDropper struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	count   int
}

func (dropper *blockingDropper) DropDatabase(ctx context.Context, _ string) error {
	dropper.mu.Lock()
	dropper.count++
	dropper.mu.Unlock()
	close(dropper.started)
	select {
	case <-dropper.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (dropper *blockingDropper) calls() int {
	dropper.mu.Lock()
	defer dropper.mu.Unlock()
	return dropper.count
}
