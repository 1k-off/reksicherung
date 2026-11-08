package runregistry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	defaultReconcileBatch = 100
	defaultCleanupTimeout = 30 * time.Second
)

// Reconciler retries cleanup for expired run leases. The registry claim is
// extended before invoking the dropper, so multiple service instances do not
// normally clean the same run concurrently. Database drops must still be
// idempotent for crash recovery. Reksicherung never stops or removes services.
type Reconciler struct {
	Store          *Store
	Databases      DatabaseDropper
	BatchLimit     int
	CleanupTimeout time.Duration
}

// CleanupOutcome describes one reconciliation attempt. Err is intentionally
// returned to the caller and is never persisted in the registry.
type CleanupOutcome struct {
	RunID  string
	Status CleanupStatus
	Err    error
}

// Reconcile claims expired cleanup leases and attempts their database drops.
// It records interrupted runs as inconclusive if no verdict exists.
func (reconciler Reconciler) Reconcile(ctx context.Context, now time.Time) ([]CleanupOutcome, error) {
	if ctx == nil {
		return nil, errors.New("run registry: reconcile context must not be nil")
	}
	if reconciler.Store == nil || reconciler.Databases == nil {
		return nil, errors.New("run registry: reconciler requires a store and database dropper")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	batchLimit := reconciler.BatchLimit
	if batchLimit == 0 {
		batchLimit = defaultReconcileBatch
	}
	cleanupTimeout := reconciler.CleanupTimeout
	if cleanupTimeout == 0 {
		cleanupTimeout = defaultCleanupTimeout
	}
	if batchLimit < 1 || cleanupTimeout < time.Second {
		return nil, errors.New("run registry: invalid reconcile batch limit or cleanup timeout")
	}

	expired, err := reconciler.Store.ListExpired(ctx, now, batchLimit)
	if err != nil {
		return nil, err
	}
	outcomes := make([]CleanupOutcome, 0, len(expired))
	for _, candidate := range expired {
		if err := ctx.Err(); err != nil {
			return outcomes, err
		}
		claimed, err := reconciler.Store.Update(ctx, candidate.ID, candidate.Revision, func(record *Record) error {
			record.CleanupStatus = CleanupInProgress
			record.LeaseExpiresAt = now.Add(cleanupTimeout).UTC()
			return nil
		})
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return outcomes, fmt.Errorf("run registry: claim cleanup for %s: %w", candidate.ID, err)
		}

		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		dropErr := reconciler.Databases.DropDatabase(cleanupCtx, claimed.ID)
		cancel()
		outcome := CleanupOutcome{RunID: candidate.ID}
		if dropErr != nil {
			outcome.Status = CleanupFailed
			outcome.Err = dropErr
			_, updateErr := reconciler.Store.Update(ctx, claimed.ID, claimed.Revision, func(record *Record) error {
				record.CleanupStatus = CleanupFailed
				if record.Status != StatusFinished {
					record.Status = StatusFinalizing
					if record.Verdict == VerdictUnknown {
						record.Verdict = VerdictInconclusive
					}
				}
				return nil
			})
			if updateErr != nil {
				return outcomes, errors.Join(
					fmt.Errorf("drop database for %s: %w", candidate.ID, dropErr),
					fmt.Errorf("record cleanup failure for %s: %w", candidate.ID, updateErr),
				)
			}
			outcomes = append(outcomes, outcome)
			continue
		}

		outcome.Status = CleanupSucceeded
		_, updateErr := reconciler.Store.Update(ctx, claimed.ID, claimed.Revision, func(record *Record) error {
			record.CleanupStatus = CleanupSucceeded
			if record.Verdict == VerdictUnknown {
				record.Verdict = VerdictInconclusive
			}
			record.Status = StatusFinished
			return nil
		})
		if updateErr != nil {
			outcome.Err = updateErr
			outcomes = append(outcomes, outcome)
			continue
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}
