// Package evidence records structured, run-scoped operation events.
//
// The JSON Lines writer only provides append-oriented application behavior. It
// does not make evidence tamper-proof or protect it from filesystem changes.
package evidence

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Status is the outcome or lifecycle state of an operation event.
type Status string

const (
	StatusStarted   Status = "started"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
	StatusSkipped   Status = "skipped"
)

// Event describes one operation lifecycle step. RunID must match the writer's
// run ID. Timestamps are serialized in UTC with RFC 3339 nanosecond precision.
type Event struct {
	RunID     string            `json:"run_id"`
	Timestamp time.Time         `json:"timestamp"`
	Operation string            `json:"operation"`
	Status    Status            `json:"status"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Error     string            `json:"error,omitempty"`
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Metadata keys are intentionally restricted to common non-secret evidence
// fields. Add a key only when its purpose and safe value format are understood.
var metadataKeys = map[string]struct{}{
	"attempt": {}, "backup_id": {}, "check_name": {}, "component": {},
	"duration_ms": {}, "model": {}, "outcome": {}, "provider": {},
	"resource_id": {}, "result": {}, "source_id": {}, "version": {},
}

var sensitiveMetadataKeyPattern = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api.?key|access.?key|credential|authorization|private.?key|connection.?string)`)

// Validate checks event fields and rejects metadata that is not explicitly
// allowed or values that look like embedded credentials.
func (event Event) Validate() error {
	if !identifierPattern.MatchString(event.RunID) {
		return fmt.Errorf("evidence: invalid run ID")
	}
	if event.Timestamp.IsZero() {
		return fmt.Errorf("evidence: timestamp must not be zero")
	}
	if !validText(event.Operation, 128) || !identifierPattern.MatchString(event.Operation) {
		return fmt.Errorf("evidence: invalid operation")
	}
	switch event.Status {
	case StatusStarted, StatusSucceeded, StatusFailed, StatusCanceled, StatusSkipped:
	default:
		return fmt.Errorf("evidence: invalid status %q", event.Status)
	}
	if event.Status == StatusFailed && strings.TrimSpace(event.Error) == "" {
		return fmt.Errorf("evidence: failed event requires an error")
	}
	if event.Error != "" {
		if !validText(event.Error, 4096) {
			return fmt.Errorf("evidence: invalid error text")
		}
		if looksSecretBearing(event.Error) {
			return fmt.Errorf("evidence: error appears to contain a secret; redact it before recording")
		}
	}
	if len(event.Metadata) > len(metadataKeys) {
		return fmt.Errorf("evidence: too many metadata fields")
	}
	for key, value := range event.Metadata {
		if sensitiveMetadataKeyPattern.MatchString(key) {
			return fmt.Errorf("evidence: sensitive metadata key %q is prohibited", key)
		}
		if _, allowed := metadataKeys[key]; !allowed {
			return fmt.Errorf("evidence: metadata key %q is not allowed", key)
		}
		if !validText(value, 4096) {
			return fmt.Errorf("evidence: invalid metadata value for %q", key)
		}
		if looksSecretBearing(value) {
			return fmt.Errorf("evidence: metadata value for %q appears to contain a secret; redact it before recording", key)
		}
	}
	return nil
}

func validText(value string, maxBytes int) bool {
	if len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}
