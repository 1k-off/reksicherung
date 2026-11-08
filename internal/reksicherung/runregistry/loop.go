package runregistry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Run repeatedly reconciles expired container leases. It performs one
// reconciliation immediately, then continues at interval until ctx is
// canceled. Per-run cleanup failures are delivered to onOutcome and retried
// after the next lease expiry; registry or storage errors stop the loop so a
// service supervisor can restart it.
func (reconciler Reconciler) Run(
	ctx context.Context,
	interval time.Duration,
	onOutcome func(CleanupOutcome),
) error {
	if ctx == nil {
		return errors.New("run registry: reconcile loop context must not be nil")
	}
	if interval < time.Second || interval > 24*time.Hour {
		return errors.New("run registry: reconcile interval must be between 1 second and 24 hours")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		outcomes, err := reconciler.Reconcile(ctx, time.Now().UTC())
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("run registry: reconciliation loop stopped: %w", err)
		}
		for _, outcome := range outcomes {
			if onOutcome != nil {
				onOutcome(outcome)
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
