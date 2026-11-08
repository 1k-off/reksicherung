package evidence

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWriterWritesDeterministicJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writer, err := NewWriter(path, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	event := Event{
		RunID:     "run-1",
		Timestamp: time.Date(2025, time.March, 4, 5, 6, 7, 8, time.FixedZone("offset", 2*60*60)),
		Operation: "restore",
		Status:    StatusSucceeded,
		Metadata: map[string]string{
			"source_id": "source-1",
			"provider":  "restic",
		},
	}
	if err := writer.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const want = "{\"run_id\":\"run-1\",\"timestamp\":\"2025-03-04T03:06:07.000000008Z\",\"operation\":\"restore\",\"status\":\"succeeded\",\"metadata\":{\"provider\":\"restic\",\"source_id\":\"source-1\"}}\n"
	if string(got) != want {
		t.Fatalf("unexpected JSONL:\n got: %s\nwant: %s", got, want)
	}
}

func TestWriterReopensAndAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	appendTestEvent(t, path, "run-2", "discover")
	appendTestEvent(t, path, "run-2", "restore")

	lines := readEventLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("got %d events, want 2", len(lines))
	}
	for i, operation := range []string{"discover", "restore"} {
		var event Event
		if err := json.Unmarshal(lines[i], &event); err != nil {
			t.Fatalf("decode line %d: %v", i, err)
		}
		if event.Operation != operation || event.RunID != "run-2" {
			t.Errorf("line %d = %#v, want operation %q for run-2", i, event, operation)
		}
	}
}

func TestWriterConcurrentAppends(t *testing.T) {
	const count = 128
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writer, err := NewWriter(path, "concurrent-run")
	if err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	errorsByAppend := make(chan error, count)
	for i := 0; i < count; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			event := NewEvent("concurrent-run", "check", StatusSucceeded)
			event.Metadata = map[string]string{"attempt": time.Duration(index).String()}
			errorsByAppend <- writer.Append(context.Background(), event)
		}(i)
	}
	wait.Wait()
	close(errorsByAppend)
	for err := range errorsByAppend {
		if err != nil {
			t.Errorf("append failed: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	lines := readEventLines(t, path)
	if len(lines) != count {
		t.Fatalf("got %d lines, want %d", len(lines), count)
	}
	for i, line := range lines {
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("line %d is malformed: %v: %s", i, err, line)
		}
	}
}

func TestWriterRejectsInvalidEvents(t *testing.T) {
	valid := Event{
		RunID:     "run-3",
		Timestamp: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
		Operation: "restore",
		Status:    StatusSucceeded,
	}
	tests := []struct {
		name string
		edit func(*Event)
	}{
		{name: "empty run ID", edit: func(event *Event) { event.RunID = "" }},
		{name: "run ID mismatch", edit: func(event *Event) { event.RunID = "other-run" }},
		{name: "zero timestamp", edit: func(event *Event) { event.Timestamp = time.Time{} }},
		{name: "invalid operation", edit: func(event *Event) { event.Operation = "../restore" }},
		{name: "invalid status", edit: func(event *Event) { event.Status = "done" }},
		{name: "failure without error", edit: func(event *Event) { event.Status = StatusFailed }},
		{name: "sensitive metadata key", edit: func(event *Event) { event.Metadata = map[string]string{"password": "not-recorded"} }},
		{name: "unknown metadata key", edit: func(event *Event) { event.Metadata = map[string]string{"description": "not allowed"} }},
		{name: "credential in metadata", edit: func(event *Event) { event.Metadata = map[string]string{"result": "password=hunter2"} }},
		{name: "credential in error", edit: func(event *Event) { event.Status, event.Error = StatusFailed, "request failed: token=secret-value" }},
		{name: "invalid control character", edit: func(event *Event) { event.Metadata = map[string]string{"result": "bad\x00value"} }},
	}
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writer, err := NewWriter(path, "run-3")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := valid
			event.Metadata = nil
			event.Error = ""
			test.edit(&event)
			if err := writer.Append(context.Background(), event); err == nil {
				t.Fatal("Append succeeded for invalid event")
			}
		})
	}
	if got := readEventLines(t, path); len(got) != 0 {
		t.Fatalf("invalid events wrote %d lines", len(got))
	}
}

func TestRedactAndRejectSecretValues(t *testing.T) {
	githubToken := "ghp_123456789012345678901234567890"
	input := "failed https://alice:pass123@example.test:443 with Bearer abc.def and password='hunter2' api_key=xyz AKIAIOSFODNN7EXAMPLE " + githubToken
	redacted := Redact(input)
	for _, secret := range []string{"pass123", "abc.def", "hunter2", "xyz", "AKIAIOSFODNN7EXAMPLE", githubToken} {
		if strings.Contains(redacted, secret) {
			t.Errorf("redaction retained %q in %q", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "password=[REDACTED]") ||
		!strings.Contains(redacted, "Bearer [REDACTED]") ||
		!strings.Contains(redacted, "https://[REDACTED]@example.test") {
		t.Errorf("redacted output missing expected markers: %q", redacted)
	}

	path := filepath.Join(t.TempDir(), "events.jsonl")
	writer, err := NewWriter(path, "redaction-run")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	event := NewEvent("redaction-run", "restore", StatusFailed)
	event.Error = Redact("restore failed password=hunter2")
	event.Metadata = map[string]string{"result": Redact("authorization: Bearer abc123")}
	if err := writer.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hunter2", "abc123"} {
		if strings.Contains(string(output), secret) {
			t.Errorf("persisted evidence contains secret %q: %s", secret, output)
		}
	}

	event.Error = "restore failed password=hunter2"
	if err := writer.Append(context.Background(), event); err == nil {
		t.Fatal("Append accepted an unredacted error")
	}
	event.Error = ""
	event.Status = StatusSucceeded
	event.Metadata = map[string]string{"result": githubToken}
	if err := writer.Append(context.Background(), event); err == nil {
		t.Fatal("Append accepted an unredacted token-like value")
	}
	if got := len(readEventLines(t, path)); got != 1 {
		t.Fatalf("unredacted error wrote data; got %d events", got)
	}
}

func TestWriterCanceledContextDoesNotAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writer, err := NewWriter(path, "cancel-run")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writer.Append(ctx, NewEvent("cancel-run", "restore", StatusStarted)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append error = %v, want context.Canceled", err)
	}
	if got := len(readEventLines(t, path)); got != 0 {
		t.Fatalf("canceled append wrote %d events", got)
	}
	if err := writer.Append(nil, NewEvent("cancel-run", "restore", StatusStarted)); err == nil {
		t.Fatal("Append accepted nil context")
	}
}

func TestWriterCloseIsSafeAndAppendAfterCloseFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writer, err := NewWriter(path, "close-run")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
	if err := writer.Append(context.Background(), NewEvent("close-run", "restore", StatusStarted)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Append error = %v, want ErrClosed", err)
	}
}

func TestWriterReportsOpenFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "events.jsonl")
	if _, err := NewWriter(path, "run-4"); err == nil {
		t.Fatal("NewWriter succeeded for missing parent directory")
	}
	if _, err := NewWriter(filepath.Join(t.TempDir(), "events.jsonl"), "../bad"); err == nil {
		t.Fatal("NewWriter accepted invalid run ID")
	}
}

func TestWriterStopsAfterAppendFailure(t *testing.T) {
	writer, err := NewWriter(filepath.Join(t.TempDir(), "events.jsonl"), "failed-write")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.file.Close(); err != nil {
		t.Fatal(err)
	}
	event := NewEvent("failed-write", "restore", StatusStarted)
	firstErr := writer.Append(context.Background(), event)
	if firstErr == nil {
		t.Fatal("Append succeeded with a closed underlying file")
	}
	if err := writer.Append(context.Background(), event); err == nil {
		t.Fatal("Append retried after a write failure")
	}
	_ = writer.Close()
}

func appendTestEvent(t *testing.T, path, runID, operation string) {
	t.Helper()
	writer, err := NewWriter(path, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(context.Background(), NewEvent(runID, operation, StatusStarted)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func readEventLines(t *testing.T, path string) [][]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var lines [][]byte
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, append([]byte(nil), scanner.Bytes()...))
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}
