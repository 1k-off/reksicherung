package runregistry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotHeldForReview   = errors.New("run registry: run is not held for review")
	ErrNoRetainedDatabase = errors.New("run registry: held run has no retained database")
)

type heldRunStore interface {
	Get(context.Context, string) (Record, error)
	Update(context.Context, string, uint64, func(*Record) error) (Record, error)
}

// DatabaseDropper drops only a run-scoped restored database on the operator-
// managed SQL Server. Implementations must treat an already-absent database as
// success and must never stop or remove services.
type DatabaseDropper interface {
	DropDatabase(context.Context, string) error
}

// CleanupHeld drops a database retained for human review. It records the
// cleanup attempt before dropping, so a restart can safely retry the
// idempotent drop if the process stops between those operations.
func CleanupHeld(ctx context.Context, store heldRunStore, databases DatabaseDropper, runID string) (Record, error) {
	if ctx == nil || store == nil || databases == nil || runID == "" {
		return Record{}, errors.New("run registry: cleanup requires context, store, database dropper, and run ID")
	}

	record, err := store.Get(ctx, runID)
	if err != nil {
		return Record{}, err
	}
	if record.CleanupMode != CleanupHold {
		return Record{}, ErrNotHeldForReview
	}
	if record.CleanupStatus == CleanupSucceeded {
		return record, nil
	}
	if record.Status != StatusFinished && record.Status != StatusFinalizing {
		return Record{}, errors.New("run registry: run is not in a cleanable state")
	}
	if record.DatabaseName == "" {
		return Record{}, ErrNoRetainedDatabase
	}
	if record.CleanupStatus != CleanupRetained && record.CleanupStatus != CleanupFailed &&
		record.CleanupStatus != CleanupInProgress {
		return Record{}, errors.New("run registry: retained database cleanup is not pending")
	}

	record, err = store.Update(ctx, runID, record.Revision, func(record *Record) error {
		record.Status = StatusFinalizing
		record.CleanupStatus = CleanupInProgress
		return nil
	})
	if err != nil {
		return Record{}, err
	}

	dropErr := databases.DropDatabase(ctx, record.ID)
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if dropErr != nil {
		failed, updateErr := store.Update(updateCtx, runID, record.Revision, func(record *Record) error {
			record.CleanupStatus = CleanupFailed
			return nil
		})
		return failed, errors.Join(fmt.Errorf("drop retained database: %w", dropErr), updateErr)
	}

	finished, err := store.Update(updateCtx, runID, record.Revision, func(record *Record) error {
		record.CleanupStatus = CleanupSucceeded
		record.Status = StatusFinished
		return nil
	})
	return finished, err
}
