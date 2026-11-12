package restic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testSnapshotID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestListSnapshotsBoundsResultsAndUsesArgvAndExplicitEnvironment(t *testing.T) {
	runner := &fakeRunner{output: snapshotJSON(testSnapshotID, "private/path/name")}
	service := newTestService(t, runner)

	result, err := service.ListSnapshots(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if len(result.Snapshots) != 1 || !result.Truncated {
		t.Fatalf("ListSnapshots() = %#v, want one result with truncation", result)
	}
	if got, want := runner.args, []string{"snapshots", "--json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Restic args = %q, want %q", got, want)
	}
	if runner.binary != "restic" {
		t.Fatalf("binary = %q, want restic", runner.binary)
	}
	if got, want := runner.env, []string{
		"RESTIC_REPOSITORY=s3:s3.example.invalid/archive",
		"RESTIC_PASSWORD=secret",
		"AWS_ACCESS_KEY_ID=access",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("process environment = %q, want explicit environment %q", got, want)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private/path/name") {
		t.Fatalf("snapshot result exposed a repository path: %s", encoded)
	}
}

func TestSelectSnapshotRequiresFullIDAndReturnsMetadata(t *testing.T) {
	var records []json.RawMessage
	if err := json.Unmarshal(snapshotJSON(testSnapshotID, "private/path/name"), &records); err != nil {
		t.Fatal(err)
	}
	selectedOutput, err := json.Marshal(records[:1])
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{output: selectedOutput}
	service := newTestService(t, runner)

	if _, err := service.SelectSnapshot(context.Background(), "short-id"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("SelectSnapshot(short ID) error = %v, want ErrInvalidInput", err)
	}
	selected, err := service.SelectSnapshot(context.Background(), testSnapshotID)
	if err != nil {
		t.Fatalf("SelectSnapshot() error = %v", err)
	}
	if selected.ID != testSnapshotID || selected.Hostname != "archive-host" || len(selected.Tags) != 1 {
		t.Fatalf("selected metadata = %#v", selected)
	}
	if got, want := runner.args, []string{"snapshots", "--json", testSnapshotID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Restic args = %q, want %q", got, want)
	}
}

func TestServiceRejectsMalformedOrUnexpectedResticJSON(t *testing.T) {
	tests := []struct {
		name   string
		output []byte
	}{
		{name: "unknown field", output: []byte(`[{"id":"` + testSnapshotID + `","unexpected":"x"}]`)},
		{name: "trailing JSON", output: append(snapshotJSON(testSnapshotID, ""), []byte(` {}`)...)},
		{name: "invalid timestamp", output: []byte(`[{"id":"` + testSnapshotID + `","time":"not-time"}]`)},
		{name: "invalid id", output: []byte(`[{"id":"short","time":"2025-01-02T03:04:05Z"}]`)},
		{name: "null instead of array", output: []byte(`null`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestService(t, &fakeRunner{output: test.output})
			if _, err := service.ListSnapshots(context.Background(), 10); !errors.Is(err, ErrFailed) {
				t.Fatalf("ListSnapshots() error = %v, want ErrFailed", err)
			}
		})
	}
}

func TestListSnapshotsTruncatesNewestFirst(t *testing.T) {
	output := snapshotJSON(testSnapshotID, "")
	output = append(output[:len(output)-1], []byte(`,{"id":"fedcfedcfedcfedcfedcfedcfedcfedcfedcfedcfedcfedcfedcfedcfedcfedc","time":"2024-12-31T03:04:05Z"}]`)...)
	service := newTestService(t, &fakeRunner{output: output})
	result, err := service.ListSnapshots(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if !result.Truncated || len(result.Snapshots) != 1 || result.Snapshots[0].ID != testSnapshotID {
		t.Fatalf("ListSnapshots() = %#v, want newest snapshot truncated to one", result)
	}
}

func TestListSnapshotsRejectsAboveHardMax(t *testing.T) {
	records := make([]map[string]any, 0, 102)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 102; i++ {
		records = append(records, map[string]any{
			"id":       fmt.Sprintf("%064x", i),
			"time":     base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
			"hostname": "archive-host",
			"tags":     []string{"daily"},
		})
	}
	output, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, &fakeRunner{output: output})
	if _, err := service.ListSnapshots(context.Background(), 1); !errors.Is(err, ErrFailed) {
		t.Fatalf("ListSnapshots(above hard max) error = %v, want ErrFailed", err)
	}
}

func TestServiceBoundsAndClassifiesRunnerErrors(t *testing.T) {
	service := newTestService(t, &fakeRunner{err: context.DeadlineExceeded})
	if _, err := service.ListSnapshots(context.Background(), 101); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ListSnapshots(over-limit) error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.ListSnapshots(context.Background(), 1); !errors.Is(err, ErrTimeout) {
		t.Fatalf("ListSnapshots(timeout) error = %v, want ErrTimeout", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	service = newTestService(t, &fakeRunner{err: context.Canceled})
	if _, err := service.ListSnapshots(canceled, 1); !errors.Is(err, ErrCanceled) {
		t.Fatalf("ListSnapshots(canceled) error = %v, want ErrCanceled", err)
	}
}

func TestNewRejectsUnsafeEnvironmentAndInvalidBounds(t *testing.T) {
	for _, options := range []Options{
		{Repository: "repo", Password: "password", Environment: []string{"RESTIC_PASSWORD_COMMAND=unsafe"}},
		{Repository: "repo", Password: "password", Environment: []string{"AWS_REGION=one", "AWS_REGION=two"}},
		{Repository: "repo", Password: "password", MaxSnapshots: hardMaxCount + 1},
		{Repository: "repo", Password: "password", Timeout: maxTimeout + time.Second},
	} {
		if _, err := NewWithRunner(&fakeRunner{}, options); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("NewWithRunner(%#v) error = %v, want ErrInvalidInput", options, err)
		}
	}
}

func TestBoundedBufferRejectsOversizedOutput(t *testing.T) {
	buffer := boundedBuffer{limit: 4}
	if _, err := buffer.Write([]byte("1234")); err != nil {
		t.Fatalf("Write(exact limit) error = %v", err)
	}
	if _, err := buffer.Write([]byte("5")); err == nil || !buffer.exceeded {
		t.Fatal("Write(over limit) did not report bounded-output failure")
	}
}

func TestServiceRejectsOversizedRunnerOutput(t *testing.T) {
	service := newTestService(t, &fakeRunner{output: []byte(strings.Repeat("x", maxCommandOutput+1))})
	if _, err := service.ListSnapshots(context.Background(), 1); !errors.Is(err, ErrFailed) {
		t.Fatalf("ListSnapshots(oversized output) error = %v, want ErrFailed", err)
	}
}

func TestSnapshotMetadataOutputFitsOneBoundedFrame(t *testing.T) {
	records := make([]snapshotRecord, hardMaxCount)
	for i := range records {
		records[i] = snapshotRecord{
			ID: testSnapshotID, Time: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
			Hostname: strings.Repeat("<", maxHostnameBytes),
			Tags:     []string{strings.Repeat("<", maxTagBytes), strings.Repeat("<", maxTagBytes), strings.Repeat("<", maxTagBytes), strings.Repeat("<", maxTagBytes)},
		}
	}
	snapshots, err := snapshotMetadata(records)
	if err != nil {
		t.Fatalf("snapshotMetadata() error = %v", err)
	}
	encoded, err := json.Marshal(SnapshotList{Snapshots: snapshots})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= 64<<10 {
		t.Fatalf("maximum snapshot metadata is %d bytes, exceeds the 64 KiB frame budget", len(encoded))
	}
}

func newTestService(t *testing.T, runner Runner) *Service {
	t.Helper()
	service, err := NewWithRunner(runner, Options{
		Binary: "restic", Repository: "s3:s3.example.invalid/archive",
		Password: "secret", Environment: []string{"AWS_ACCESS_KEY_ID=access"},
		MaxSnapshots: 100, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewWithRunner() error = %v", err)
	}
	return service
}

type fakeRunner struct {
	binary string
	args   []string
	env    []string
	output []byte
	err    error
}

func (runner *fakeRunner) Run(_ context.Context, binary string, args, env []string) ([]byte, error) {
	runner.binary = binary
	runner.args = append([]string(nil), args...)
	runner.env = append([]string(nil), env...)
	return runner.output, runner.err
}

func snapshotJSON(id, path string) []byte {
	pathJSON := "[]"
	if path != "" {
		pathJSON = `["` + path + `"]`
	}
	return []byte(`[
		{"id":"` + id + `","short_id":"01234567","time":"2025-01-02T03:04:05Z",
		 "hostname":"archive-host","username":"backup","uid":1000,"gid":1000,
		 "paths":` + pathJSON + `,"tags":["daily"],"tree":"tree-id","parent":"",
		 "original":"","program_version":"0.17.0","summary":null,
		 "excludes":[],"includes":[],"struct_type":"snapshot"},
		{"id":"abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		 "short_id":"abcdefab","time":"2025-01-01T03:04:05Z","hostname":"old-host",
		 "username":"backup","uid":1000,"gid":1000,"paths":[],"tags":[],
		 "tree":"old-tree","parent":"","original":"","program_version":"0.17.0",
		 "summary":null,"excludes":[],"includes":[],"struct_type":"snapshot"}
	]`)
}
