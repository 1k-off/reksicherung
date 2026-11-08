package runregistry

import (
	"context"
	"testing"
	"time"
)

func TestReconcilerRunPerformsStartupPassAndStopsOnCancellation(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	createExpiringRun(t, store, "run-loop", CleanupAutomatic, "container-loop", now)
	remover := &fakeRemover{}
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan CleanupOutcome, 1)

	err := (Reconciler{Store: store, Containers: remover}).Run(ctx, time.Hour, func(outcome CleanupOutcome) {
		outcomes <- outcome
		cancel()
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	select {
	case outcome := <-outcomes:
		if outcome.RunID != "run-loop" || outcome.Status != CleanupSucceeded {
			t.Fatalf("outcome = %#v, want successful startup reconciliation", outcome)
		}
	default:
		t.Fatal("Run() did not reconcile before waiting for its ticker")
	}
}

func TestReconcilerRunRejectsInvalidInterval(t *testing.T) {
	for _, interval := range []time.Duration{0, time.Millisecond, 24*time.Hour + time.Second} {
		if err := (Reconciler{}).Run(context.Background(), interval, nil); err == nil {
			t.Errorf("Run(interval=%s) error = nil, want validation error", interval)
		}
	}
}
