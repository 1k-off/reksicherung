package runregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

var runsBucket = []byte("runs")

// Store is a durable, transactional run registry backed by bbolt. The database
// contains only run metadata and run-scoped database names, never credentials
// or backup contents.
type Store struct {
	db        *bolt.DB
	closeOnce sync.Once
	closeErr  error
}

// Open creates or opens a registry file. Parent directories are created with
// owner-only permissions on POSIX filesystems.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("run registry: database path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("run registry: create database directory: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("run registry: open database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run registry: restrict database file permissions: %w", err)
	}
	store := &Store{db: db}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(runsBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run registry: initialize database: %w", err)
	}
	return store, nil
}

// Close closes the underlying database. It is safe to call more than once.
func (store *Store) Close() error {
	store.closeOnce.Do(func() {
		store.closeErr = store.db.Close()
	})
	return store.closeErr
}

// Create stores a new run record at revision 1.
func (store *Store) Create(ctx context.Context, record Record) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	if record.Revision != 1 {
		return Record{}, fmt.Errorf("%w: new record revision must be one", ErrInvalidRecord)
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return Record{}, fmt.Errorf("run registry: encode record: %w", err)
	}
	err = store.db.Update(func(tx *bolt.Tx) error {
		if err := contextError(ctx); err != nil {
			return err
		}
		bucket := tx.Bucket(runsBucket)
		if bucket.Get([]byte(record.ID)) != nil {
			return fmt.Errorf("%w: run ID already exists", ErrConflict)
		}
		return bucket.Put([]byte(record.ID), data)
	})
	if err != nil {
		return Record{}, fmt.Errorf("run registry: create run: %w", err)
	}
	return record, nil
}

// Get loads one run by ID.
func (store *Store) Get(ctx context.Context, id string) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	var record Record
	err := store.db.View(func(tx *bolt.Tx) error {
		if err := contextError(ctx); err != nil {
			return err
		}
		data := tx.Bucket(runsBucket).Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &record)
	})
	if err != nil {
		return Record{}, fmt.Errorf("run registry: get run: %w", err)
	}
	if err := record.Validate(); err != nil {
		return Record{}, fmt.Errorf("run registry: stored record is invalid: %w", err)
	}
	return record, nil
}

// Update applies a compare-and-swap update. The callback must only mutate its
// in-memory record; it must not perform I/O because it executes in a database
// transaction.
func (store *Store) Update(ctx context.Context, id string, expectedRevision uint64, mutate func(*Record) error) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	if mutate == nil {
		return Record{}, errors.New("run registry: mutation callback must not be nil")
	}
	var updated Record
	err := store.db.Update(func(tx *bolt.Tx) error {
		if err := contextError(ctx); err != nil {
			return err
		}
		bucket := tx.Bucket(runsBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		var current Record
		if err := json.Unmarshal(data, &current); err != nil {
			return fmt.Errorf("decode current record: %w", err)
		}
		if current.Revision != expectedRevision {
			return ErrConflict
		}
		next := current
		if err := mutate(&next); err != nil {
			return err
		}
		if err := validateUpdate(current, next); err != nil {
			return err
		}
		next.Revision = current.Revision + 1
		next.UpdatedAt = time.Now().UTC()
		if err := next.Validate(); err != nil {
			return err
		}
		nextData, err := json.Marshal(next)
		if err != nil {
			return fmt.Errorf("encode updated record: %w", err)
		}
		if err := bucket.Put([]byte(id), nextData); err != nil {
			return err
		}
		updated = next
		return nil
	})
	if err != nil {
		return Record{}, fmt.Errorf("run registry: update run: %w", err)
	}
	return updated, nil
}

// ListExpired returns automatic-cleanup runs whose lease has expired and which
// still have a restored database to drop. Held-for-review runs are never selected.
func (store *Store) ListExpired(ctx context.Context, now time.Time, limit int) ([]Record, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, errors.New("run registry: result limit must be positive")
	}
	var records []Record
	err := store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(runsBucket).Cursor()
		for _, value := cursor.First(); value != nil; _, value = cursor.Next() {
			if err := contextError(ctx); err != nil {
				return err
			}
			var record Record
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("decode run record: %w", err)
			}
			if err := record.Validate(); err != nil {
				return fmt.Errorf("stored record is invalid: %w", err)
			}
			if record.CleanupMode != CleanupAutomatic ||
				(record.CleanupStatus != CleanupPending && record.CleanupStatus != CleanupFailed && record.CleanupStatus != CleanupInProgress) ||
				record.DatabaseName == "" ||
				record.LeaseExpiresAt.After(now) {
				continue
			}
			records = append(records, record)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("run registry: list expired runs: %w", err)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].LeaseExpiresAt.Equal(records[j].LeaseExpiresAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].LeaseExpiresAt.Before(records[j].LeaseExpiresAt)
	})
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func validateUpdate(current, next Record) error {
	if next.ID != current.ID {
		return fmt.Errorf("%w: run ID is immutable", ErrInvalidRecord)
	}
	if !next.CreatedAt.Equal(current.CreatedAt) {
		return fmt.Errorf("%w: creation time is immutable", ErrInvalidRecord)
	}
	if next.CleanupMode != current.CleanupMode {
		return fmt.Errorf("%w: cleanup mode is immutable", ErrInvalidRecord)
	}
	canFinishWithCleanup := next.Status == StatusFinished &&
		next.Verdict != VerdictUnknown &&
		(next.CleanupStatus == CleanupSucceeded || next.CleanupStatus == CleanupRetained || next.CleanupStatus == CleanupNotRequired)
	canStartHeldCleanup := current.Status == StatusFinished &&
		current.CleanupMode == CleanupHold &&
		current.CleanupStatus == CleanupRetained &&
		next.Status == StatusFinalizing &&
		next.CleanupStatus == CleanupInProgress
	if next.Status == StatusFinished && !canFinishWithCleanup {
		return fmt.Errorf("%w: finished run requires a verdict and completed or retained cleanup", ErrInvalidRecord)
	}
	if !canTransition(current.Status, next.Status) && !canFinishWithCleanup && !canStartHeldCleanup {
		return fmt.Errorf("%w: invalid status transition %q to %q", ErrInvalidRecord, current.Status, next.Status)
	}
	if !canTransitionCleanup(current.CleanupStatus, next.CleanupStatus) {
		return fmt.Errorf("%w: invalid cleanup transition %q to %q", ErrInvalidRecord, current.CleanupStatus, next.CleanupStatus)
	}
	if current.Verdict != VerdictUnknown && next.Verdict != current.Verdict {
		return fmt.Errorf("%w: verdict is immutable after it is set", ErrInvalidRecord)
	}
	if current.ExternalRunID != "" && next.ExternalRunID != current.ExternalRunID {
		return fmt.Errorf("%w: external run ID is immutable after it is set", ErrInvalidRecord)
	}
	if current.DatabaseName != "" && next.DatabaseName != current.DatabaseName {
		return fmt.Errorf("%w: database name is immutable after it is set", ErrInvalidRecord)
	}
	if next.LeaseExpiresAt.Before(current.LeaseExpiresAt) {
		return fmt.Errorf("%w: lease cannot be shortened", ErrInvalidRecord)
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("run registry: context must not be nil")
	}
	return ctx.Err()
}
