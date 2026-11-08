package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

var (
	// ErrClosed is returned when an append is attempted after Close.
	ErrClosed = errors.New("evidence: writer is closed")
	// ErrRunIDMismatch indicates that an event belongs to a different run.
	ErrRunIDMismatch = errors.New("evidence: event run ID does not match writer")
)

// Writer appends JSON Lines events for one run. Its mutex serializes writes
// from concurrent callers. Closing is safe to call more than once.
type Writer struct {
	mu      sync.Mutex
	file    *os.File
	runID   string
	closed  bool
	failure error
}

// NewWriter opens path for append-only JSON Lines output. The file is created
// with owner-only permissions when it does not already exist. Existing content
// is preserved; callers can reopen the same path to continue a run.
func NewWriter(path, runID string) (*Writer, error) {
	if !identifierPattern.MatchString(runID) {
		return nil, fmt.Errorf("evidence: invalid run ID")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("evidence: open event log: %w", err)
	}
	return &Writer{file: file, runID: runID}, nil
}

// Append validates and serializes event, then appends one complete JSON line.
// Cancellation is checked before waiting for the writer lock and again before
// writing. Events are not written if the context is canceled before that point.
func (writer *Writer) Append(ctx context.Context, event Event) error {
	if ctx == nil {
		return fmt.Errorf("evidence: context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.RunID != writer.runID {
		return ErrRunIDMismatch
	}
	if err := event.Validate(); err != nil {
		return err
	}

	event.Timestamp = event.Timestamp.UTC()
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("evidence: encode event: %w", err)
	}
	line = append(line, '\n')

	writer.mu.Lock()
	defer writer.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if writer.closed {
		return ErrClosed
	}
	if writer.failure != nil {
		return fmt.Errorf("evidence: writer cannot continue after an append failure: %w", writer.failure)
	}
	for len(line) > 0 {
		n, writeErr := writer.file.Write(line)
		if writeErr != nil {
			writer.failure = fmt.Errorf("evidence: append event: %w", writeErr)
			return writer.failure
		}
		if n == 0 {
			writer.failure = fmt.Errorf("evidence: append event: %w", os.ErrInvalid)
			return writer.failure
		}
		line = line[n:]
	}
	return nil
}

// Close syncs and closes the log. It is safe to call multiple times.
func (writer *Writer) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return nil
	}
	writer.closed = true
	syncErr := writer.file.Sync()
	closeErr := writer.file.Close()
	return errors.Join(wrapFileError("sync", syncErr), wrapFileError("close", closeErr))
}

func wrapFileError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("evidence: %s event log: %w", operation, err)
}

// NewEvent creates an event timestamped at the current time. Callers that need
// a deterministic timestamp can construct Event directly.
func NewEvent(runID, operation string, status Status) Event {
	return Event{
		RunID:     runID,
		Timestamp: time.Now().UTC(),
		Operation: operation,
		Status:    status,
	}
}
