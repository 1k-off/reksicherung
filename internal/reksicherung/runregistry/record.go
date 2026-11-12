// Package runregistry persists restore-run lifecycle and database cleanup leases.
package runregistry

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

type Status string

const (
	StatusCreated    Status = "created"
	StatusPreparing  Status = "preparing"
	StatusRestoring  Status = "restoring"
	StatusValidating Status = "validating"
	StatusFinalizing Status = "finalizing"
	StatusFinished   Status = "finished"
)

type Verdict string

const (
	VerdictUnknown      Verdict = "unknown"
	VerdictPass         Verdict = "pass"
	VerdictFail         Verdict = "fail"
	VerdictInconclusive Verdict = "inconclusive"
)

type CleanupMode string

const (
	CleanupAutomatic CleanupMode = "automatic"
	CleanupHold      CleanupMode = "hold_for_review"
)

type CleanupStatus string

const (
	CleanupPending     CleanupStatus = "pending"
	CleanupInProgress  CleanupStatus = "in_progress"
	CleanupSucceeded   CleanupStatus = "succeeded"
	CleanupFailed      CleanupStatus = "failed"
	CleanupRetained    CleanupStatus = "retained"
	CleanupNotRequired CleanupStatus = "not_required"
)

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var databaseNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_$#-]{0,127}$`)

var (
	ErrNotFound      = errors.New("run registry: run not found")
	ErrConflict      = errors.New("run registry: revision conflict")
	ErrInvalidRecord = errors.New("run registry: invalid record")
)

// Record contains only non-secret run metadata. Secret values, prompts, and
// business data must be stored elsewhere and excluded from the run registry.
type Record struct {
	ID             string        `json:"id"`
	ExternalRunID     string        `json:"external_run_id,omitempty"`
	Status         Status        `json:"status"`
	Verdict        Verdict       `json:"verdict"`
	CleanupMode    CleanupMode   `json:"cleanup_mode"`
	CleanupStatus  CleanupStatus `json:"cleanup_status"`
	DatabaseName   string        `json:"database_name,omitempty"`
	LeaseExpiresAt time.Time     `json:"lease_expires_at"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	Revision       uint64        `json:"revision"`
}

// NewRecord creates a run that is initially eligible for cleanup if its lease
// expires before normal finalization. hold_for_review runs are excluded from
// automatic cleanup regardless of lease expiry.
func NewRecord(id string, cleanupMode CleanupMode, now, leaseExpiresAt time.Time) (Record, error) {
	record := Record{
		ID:             id,
		Status:         StatusCreated,
		Verdict:        VerdictUnknown,
		CleanupMode:    cleanupMode,
		CleanupStatus:  CleanupPending,
		Revision:       1,
		LeaseExpiresAt: leaseExpiresAt.UTC(),
		CreatedAt:      now.UTC(),
		UpdatedAt:      now.UTC(),
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Validate checks the persisted record's invariants.
func (record Record) Validate() error {
	if !runIDPattern.MatchString(record.ID) {
		return fmt.Errorf("%w: invalid run ID", ErrInvalidRecord)
	}
	if record.ExternalRunID != "" && !runIDPattern.MatchString(record.ExternalRunID) {
		return fmt.Errorf("%w: invalid external run ID", ErrInvalidRecord)
	}
	switch record.Status {
	case StatusCreated, StatusPreparing, StatusRestoring, StatusValidating, StatusFinalizing, StatusFinished:
	default:
		return fmt.Errorf("%w: invalid status %q", ErrInvalidRecord, record.Status)
	}
	switch record.Verdict {
	case VerdictUnknown, VerdictPass, VerdictFail, VerdictInconclusive:
	default:
		return fmt.Errorf("%w: invalid verdict %q", ErrInvalidRecord, record.Verdict)
	}
	switch record.CleanupMode {
	case CleanupAutomatic, CleanupHold:
	default:
		return fmt.Errorf("%w: invalid cleanup mode %q", ErrInvalidRecord, record.CleanupMode)
	}
	switch record.CleanupStatus {
	case CleanupPending, CleanupInProgress, CleanupSucceeded, CleanupFailed, CleanupRetained:
	case CleanupNotRequired:
	default:
		return fmt.Errorf("%w: invalid cleanup status %q", ErrInvalidRecord, record.CleanupStatus)
	}
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("%w: timestamps must not be zero", ErrInvalidRecord)
	}
	if record.UpdatedAt.Before(record.CreatedAt) {
		return fmt.Errorf("%w: updated time precedes creation time", ErrInvalidRecord)
	}
	if record.LeaseExpiresAt.Before(record.CreatedAt) {
		return fmt.Errorf("%w: lease expires before run creation", ErrInvalidRecord)
	}
	if record.Revision == 0 {
		return fmt.Errorf("%w: revision must be positive", ErrInvalidRecord)
	}
	if record.Status == StatusFinished && record.Verdict == VerdictUnknown {
		return fmt.Errorf("%w: finished run must have a verdict", ErrInvalidRecord)
	}
	if record.Status == StatusFinished &&
		record.CleanupStatus != CleanupSucceeded &&
		record.CleanupStatus != CleanupRetained &&
		record.CleanupStatus != CleanupNotRequired {
		return fmt.Errorf("%w: finished run must have completed, retained, or unnecessary cleanup", ErrInvalidRecord)
	}
	if record.CleanupStatus == CleanupRetained && record.CleanupMode != CleanupHold {
		return fmt.Errorf("%w: only held runs may have retained cleanup status", ErrInvalidRecord)
	}
	switch record.CleanupStatus {
	case CleanupInProgress, CleanupSucceeded, CleanupFailed, CleanupRetained:
		if record.DatabaseName == "" {
			return fmt.Errorf("%w: cleanup status %q requires a database name", ErrInvalidRecord, record.CleanupStatus)
		}
	case CleanupNotRequired:
		if record.DatabaseName != "" {
			return fmt.Errorf("%w: cleanup cannot be unnecessary when a database name exists", ErrInvalidRecord)
		}
	}
	if record.DatabaseName != "" && !databaseNamePattern.MatchString(record.DatabaseName) {
		return fmt.Errorf("%w: invalid database name", ErrInvalidRecord)
	}
	return nil
}

func canTransition(from, to Status) bool {
	if from == to {
		return true
	}
	if to == StatusFinalizing && from != StatusFinished {
		return true
	}
	switch from {
	case StatusCreated:
		return to == StatusPreparing
	case StatusPreparing:
		return to == StatusRestoring
	case StatusRestoring:
		return to == StatusValidating
	case StatusValidating:
		return false
	case StatusFinalizing:
		return to == StatusFinished
	default:
		return false
	}
}

func canTransitionCleanup(from, to CleanupStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case CleanupPending:
		return to == CleanupInProgress || to == CleanupSucceeded || to == CleanupFailed || to == CleanupRetained || to == CleanupNotRequired
	case CleanupInProgress:
		return to == CleanupSucceeded || to == CleanupFailed
	case CleanupFailed:
		return to == CleanupPending || to == CleanupInProgress || to == CleanupSucceeded
	case CleanupRetained:
		return to == CleanupPending || to == CleanupInProgress || to == CleanupSucceeded || to == CleanupFailed
	default:
		return false
	}
}
