package runregistry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

var (
	ErrNotHeldForReview    = errors.New("run registry: run is not held for review")
	ErrNoRetainedContainer = errors.New("run registry: held run has no retained container")
)

type heldRunStore interface {
	Get(context.Context, string) (Record, error)
	Update(context.Context, string, uint64, func(*Record) error) (Record, error)
}

type containerRemover interface {
	Remove(context.Context, container.Container) error
}

// CleanupHeld removes a container retained for human review. It records the
// cleanup attempt before removing the resource, so a restart can safely retry
// the idempotent removal if the process stops between those operations.
func CleanupHeld(ctx context.Context, store heldRunStore, containers containerRemover, runID string) (Record, error) {
	if ctx == nil || store == nil || containers == nil || runID == "" {
		return Record{}, errors.New("run registry: cleanup requires context, store, runtime, and run ID")
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
	if record.ContainerID == "" {
		return Record{}, ErrNoRetainedContainer
	}
	if record.CleanupStatus != CleanupRetained && record.CleanupStatus != CleanupFailed &&
		record.CleanupStatus != CleanupInProgress {
		return Record{}, errors.New("run registry: retained container cleanup is not pending")
	}

	record, err = store.Update(ctx, runID, record.Revision, func(record *Record) error {
		record.Status = StatusFinalizing
		record.CleanupStatus = CleanupInProgress
		return nil
	})
	if err != nil {
		return Record{}, err
	}

	removeErr := containers.Remove(ctx, container.Container{
		ID: record.ContainerID, Name: container.ContainerName(record.ID), RunID: record.ID,
	})
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if removeErr != nil {
		failed, updateErr := store.Update(updateCtx, runID, record.Revision, func(record *Record) error {
			record.CleanupStatus = CleanupFailed
			return nil
		})
		return failed, errors.Join(fmt.Errorf("remove retained container: %w", removeErr), updateErr)
	}

	finished, err := store.Update(updateCtx, runID, record.Revision, func(record *Record) error {
		record.CleanupStatus = CleanupSucceeded
		record.Status = StatusFinished
		return nil
	})
	if err != nil {
		return Record{}, fmt.Errorf("record retained-container cleanup: %w", err)
	}
	return finished, nil
}
