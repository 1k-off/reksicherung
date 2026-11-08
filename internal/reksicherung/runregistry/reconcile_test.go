package runregistry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

func TestReconcileRemovesExpiredContainerAndClosesInterruptedRun(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	record := createExpiringRun(t, store, "run-reconcile", CleanupAutomatic, "container-123", now)
	remover := &fakeRemover{}
	reconciler := Reconciler{Store: store, Containers: remover}

	outcomes, err := reconciler.Reconcile(context.Background(), now)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].RunID != record.ID || outcomes[0].Status != CleanupSucceeded || outcomes[0].Err != nil {
		t.Fatalf("Reconcile() outcomes = %#v", outcomes)
	}
	if len(remover.ids) != 1 || remover.ids[0] != record.ContainerID {
		t.Fatalf("removed IDs = %#v, want %q", remover.ids, record.ContainerID)
	}
	if len(remover.targets) != 1 ||
		remover.targets[0].Name != container.ContainerName(record.ID) ||
		remover.targets[0].RunID != record.ID {
		t.Fatalf("removed handle = %#v, want run-scoped name and ID", remover.targets)
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
	record := createExpiringRun(t, store, "run-retry", CleanupAutomatic, "container-456", now)
	remover := &fakeRemover{failures: []error{errors.New("runtime unavailable"), nil}}
	reconciler := Reconciler{Store: store, Containers: remover, CleanupTimeout: 5 * time.Second}

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
	if len(remover.ids) != 1 {
		t.Fatalf("removal calls = %d, want 1", len(remover.ids))
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
	if len(remover.ids) != 2 {
		t.Fatalf("removal calls = %d, want 2", len(remover.ids))
	}
}

func TestReconcileNeverAutomaticallyCleansHeldForReview(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	record := createExpiringRun(t, store, "run-held", CleanupHold, "container-held", now)
	_, err := store.Update(context.Background(), record.ID, record.Revision, func(record *Record) error {
		record.CleanupStatus = CleanupRetained
		return nil
	})
	if err != nil {
		t.Fatalf("mark held run retained: %v", err)
	}
	remover := &fakeRemover{}
	outcomes, err := (Reconciler{Store: store, Containers: remover}).Reconcile(context.Background(), now)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(outcomes) != 0 || len(remover.ids) != 0 {
		t.Fatalf("Reconcile() outcomes/removes = %#v/%#v, want none", outcomes, remover.ids)
	}
}

func TestConcurrentReconcilerClaimsCleanupOnlyOnce(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	createExpiringRun(t, store, "run-concurrent-cleanup", CleanupAutomatic, "container-race", now)
	remover := &blockingRemover{started: make(chan struct{}), release: make(chan struct{})}
	reconciler := Reconciler{Store: store, Containers: remover}

	firstDone := make(chan error, 1)
	go func() {
		_, err := reconciler.Reconcile(context.Background(), now)
		firstDone <- err
	}()
	<-remover.started
	second, err := reconciler.Reconcile(context.Background(), now)
	if err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second outcomes = %#v, want no work while lease is held", second)
	}
	close(remover.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if got := remover.calls(); got != 1 {
		t.Fatalf("container remove calls = %d, want 1", got)
	}
}

func TestReconcileRespectsCanceledContextBeforeListing(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	remover := &fakeRemover{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Reconciler{Store: store, Containers: remover}).Reconcile(ctx, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconcile(canceled) error = %v, want context.Canceled", err)
	}
	if len(remover.ids) != 0 {
		t.Fatalf("canceled reconciliation removed containers: %#v", remover.ids)
	}
}

func createExpiringRun(t *testing.T, store *Store, id string, mode CleanupMode, containerID string, now time.Time) Record {
	t.Helper()
	createdAt := now.Add(-2 * time.Minute)
	record, err := store.Create(context.Background(), mustNewRecord(t, id, mode, createdAt, now.Add(-time.Minute)))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	updated, err := store.Update(context.Background(), id, record.Revision, func(record *Record) error {
		record.ContainerID = containerID
		return nil
	})
	if err != nil {
		t.Fatalf("set container ID: %v", err)
	}
	return updated
}

type fakeRemover struct {
	mu       sync.Mutex
	ids      []string
	targets  []container.Container
	failures []error
}

func (remover *fakeRemover) Remove(_ context.Context, target container.Container) error {
	remover.mu.Lock()
	defer remover.mu.Unlock()
	remover.ids = append(remover.ids, target.ID)
	remover.targets = append(remover.targets, target)
	if len(remover.failures) == 0 {
		return nil
	}
	err := remover.failures[0]
	remover.failures = remover.failures[1:]
	return err
}

type blockingRemover struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	count   int
}

func (remover *blockingRemover) Remove(ctx context.Context, _ container.Container) error {
	remover.mu.Lock()
	remover.count++
	remover.mu.Unlock()
	close(remover.started)
	select {
	case <-remover.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (remover *blockingRemover) calls() int {
	remover.mu.Lock()
	defer remover.mu.Unlock()
	return remover.count
}
