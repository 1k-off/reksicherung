// Package mysql contains bounded operations for an operator-managed MySQL
// (or compatible) server: logical dump rewriting and run-scoped restore,
// completion-only integrity checks, and cleanup.
package mysql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const (
	restoreTimeout       = 6 * time.Hour
	createDBTimeout      = 30 * time.Second
	checkDBTimeout       = 30 * time.Minute
	dropDBTimeout        = 30 * time.Second
	maxDumpBytes         = int64(1 << 40)
	remoteOutputLimit    = 64 << 10
	maxDatabaseNameBytes = 64
)

var (
	ErrInvalidConfig        = errors.New("invalid MySQL restore configuration")
	ErrTimeout              = errors.New("MySQL operation timed out")
	ErrCanceled             = errors.New("MySQL operation canceled")
	ErrCommandFailed        = errors.New("MySQL command failed")
	ErrOutputLimit          = errors.New("MySQL command output exceeded limit")
	ErrInvalidDump          = errors.New("MySQL dump is invalid or unsupported")
	ErrDumpLimit            = errors.New("MySQL dump exceeded the configured size limit")
	ErrTransferFailed       = errors.New("MySQL dump transfer failed")
	ErrCredentialFileFailed = errors.New("MySQL credential file could not be managed")
)

// IntegrityResult reports command completion only. It does not state that the
// database is free of consistency errors.
type IntegrityResult struct {
	Database         string
	CommandCompleted bool
}

// DatabaseNameForRun returns the only database name that restore operations
// may modify for a run.
func DatabaseNameForRun(runID string) (string, error) {
	if !validRunID(runID) {
		return "", ErrInvalidConfig
	}
	digest := sha256.Sum256([]byte(runID))
	return "reksicherung_" + hex.EncodeToString(digest[:16]), nil
}

func validRunID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for index, char := range id {
		alphaNumeric := char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9'
		if index == 0 && !alphaNumeric {
			return false
		}
		if !alphaNumeric && char != '_' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

// ValidDatabaseName reports whether name is a conservative MySQL schema name
// that is safe to embed in backtick-quoted identifiers.
func ValidDatabaseName(name string) bool {
	return validDatabaseName(name)
}

func validDatabaseName(name string) bool {
	if name == "" || len(name) > maxDatabaseNameBytes {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '$' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func quoteIdentifier(name string) string {
	// validDatabaseName excludes the backtick delimiter.
	return "`" + name + "`"
}

func contextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return ErrCanceled
	}
	return ErrTimeout
}

func validRemoteValue(value string, max int) bool {
	if value == "" || len(value) > max || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
