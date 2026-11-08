package restic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	oldSnapshotID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	newSnapshotID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testSecret    = "test-only-restic-secret"
)

const snapshotsJSON = `[
  {"id":"` + oldSnapshotID + `","short_id":"aaaaaaaa","time":"2024-01-01T00:00:00Z"},
  {"id":"` + newSnapshotID + `","short_id":"bbbbbbbb","time":"2024-02-01T00:00:00Z"}
]`
const backupFileNodeJSON = `{"message_type":"node","struct_type":"node","type":"file","path":"/var/backups/sql server/db.bak"}`

func TestRestoreSelectsLatestAndUsesSafeArgv(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{responses: [][]byte{[]byte("restic 0.17.3"), []byte(snapshotsJSON), nil}}
	client := newTestClient(t, runner)
	result, err := client.Restore(context.Background(), Selection{Policy: SelectionLatest})
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if result.Snapshot.ID != newSnapshotID || !result.Snapshot.Time.Equal(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("selected snapshot = %#v, want latest snapshot", result.Snapshot)
	}
	if result.Target != "/restore/sqlserver" {
		t.Fatalf("target = %q, want configured target", result.Target)
	}

	wantArgs := [][]string{
		{"version"},
		{"--repo", "s3:https://objects.example.test/backups", "snapshots", "--json"},
		{"--repo", "s3:https://objects.example.test/backups", "restore", newSnapshotID, "--target", "/restore/sqlserver"},
	}
	if got := runner.invocations; len(got) != len(wantArgs) {
		t.Fatalf("runner calls = %d, want %d", len(got), len(wantArgs))
	} else {
		for index, want := range wantArgs {
			if !reflect.DeepEqual(got[index].Args, want) {
				t.Errorf("argv[%d] = %#v, want %#v", index, got[index].Args, want)
			}
			if got[index].Executable != "restic" {
				t.Errorf("executable[%d] = %q, want restic", index, got[index].Executable)
			}
			if got[index].MaxOutputBytes != 1<<20 {
				t.Errorf("output limit[%d] = %d, want %d", index, got[index].MaxOutputBytes, 1<<20)
			}
			if strings.Join(got[index].Args, " ") == "" {
				t.Errorf("argv[%d] is empty", index)
			}
		}
	}
	for _, invocation := range runner.invocations {
		if strings.Contains(strings.Join(invocation.Args, "\x00"), testSecret) {
			t.Fatal("secret value appeared in argv")
		}
	}
	if len(runner.invocations[0].Environment) != 0 {
		t.Fatalf("preflight environment = %#v, want none", runner.invocations[0].Environment)
	}
	if len(runner.invocations[1].Environment) != 1 ||
		runner.invocations[1].Environment[0] != (EnvironmentVariable{Name: "RESTIC_PASSWORD", Value: testSecret}) {
		t.Fatalf("repository environment = %#v, want configured secret environment", runner.invocations[1].Environment)
	}
}

func TestRestoreSelectsExplicitSnapshot(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{responses: [][]byte{[]byte("restic 0.17.3"), []byte(snapshotsJSON), nil}}
	client := newTestClient(t, runner)
	result, err := client.Restore(context.Background(), Selection{Policy: SelectionID, ID: oldSnapshotID})
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if result.Snapshot.ID != oldSnapshotID {
		t.Fatalf("selected snapshot ID = %q, want %q", result.Snapshot.ID, oldSnapshotID)
	}
	if got := runner.invocations[2].Args[3]; got != oldSnapshotID {
		t.Fatalf("restore snapshot argv = %q, want %q", got, oldSnapshotID)
	}
}

func TestOpenBackupFileSelectsSnapshotAndStreamsOneExactPath(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		responses:  [][]byte{[]byte("restic 0.17.3"), []byte(snapshotsJSON), []byte(backupFileNodeJSON)},
		streamData: "large-native-backup",
	}
	client := newTestClient(t, runner)
	selected, stream, err := client.OpenBackupFile(context.Background(),
		Selection{Policy: SelectionID, ID: oldSnapshotID}, "/var/backups/sql server/db.bak")
	if err != nil {
		t.Fatalf("OpenBackupFile() error = %v", err)
	}
	defer stream.Close()
	output, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll(stream) error = %v", err)
	}
	if selected.ID != oldSnapshotID || string(output) != runner.streamData {
		t.Fatalf("selected=%#v streamed=%q", selected, output)
	}
	wantArgs := []string{
		"--repo", "s3:https://objects.example.test/backups",
		"dump", oldSnapshotID, "/var/backups/sql server/db.bak",
	}
	if got := runner.invocations[len(runner.invocations)-1].Args; !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("dump argv = %#v, want %#v", got, wantArgs)
	}
	if strings.Contains(strings.Join(runner.invocations[len(runner.invocations)-1].Args, "\x00"), testSecret) {
		t.Fatal("Restic password appeared in dump argv")
	}
}

func TestOpenBackupFileRejectsUnsafePathBeforeRunning(t *testing.T) {
	t.Parallel()

	for _, backupPath := range []string{
		"", "relative.bak", "/var/backups/../outside.bak",
		"/var/backups/*.bak", "/var/backups/[db].bak", "/var/backups/db.bacpac",
	} {
		runner := &fakeRunner{}
		client := newTestClient(t, runner)
		if _, _, err := client.OpenBackupFile(context.Background(), Selection{Policy: SelectionLatest}, backupPath); !errors.Is(err, ErrInvalidBackupPath) {
			t.Errorf("OpenBackupFile(path %q) error = %v, want ErrInvalidBackupPath", backupPath, err)
		}
		if len(runner.invocations) != 0 {
			t.Errorf("OpenBackupFile(path %q) ran %d commands", backupPath, len(runner.invocations))
		}
	}
}

func TestOpenBackupFileSanitizesStreamingFailures(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		responses: [][]byte{[]byte("restic"), []byte(snapshotsJSON), []byte(backupFileNodeJSON)},
		streamErr: errors.New("private repository diagnostic " + testSecret),
	}
	client := newTestClient(t, runner)
	_, _, err := client.OpenBackupFile(context.Background(), Selection{Policy: SelectionLatest}, "/var/backups/sql server/db.bak")
	if !errors.Is(err, ErrCommandFailed) || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("OpenBackupFile() error = %v, want sanitized ErrCommandFailed", err)
	}
}

func TestOpenBackupFileRequiresExactlyOneRegularNode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{name: "directory", data: `{"message_type":"node","type":"dir","path":"/var/backups/sql server/db.bak"}`},
		{name: "other path", data: `{"message_type":"node","type":"file","path":"/var/backups/other.bak"}`},
		{name: "multiple nodes", data: `{"message_type":"node","type":"file","path":"/var/backups/sql server/db.bak"}` + "\n" +
			`{"message_type":"node","type":"file","path":"/var/backups/sql server/child.bak"}`},
		{name: "duplicate path key", data: `{"message_type":"node","type":"file","path":"/var/backups/sql server/db.bak","path":"/other.bak"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{responses: [][]byte{
				[]byte("restic"), []byte(snapshotsJSON), []byte(test.data),
			}}
			client := newTestClient(t, runner)
			_, _, err := client.OpenBackupFile(context.Background(), Selection{Policy: SelectionLatest}, "/var/backups/sql server/db.bak")
			if !errors.Is(err, ErrBackupNotFile) {
				t.Fatalf("OpenBackupFile() error = %v, want ErrBackupNotFile", err)
			}
			if len(runner.invocations) != 3 {
				t.Fatalf("runner calls = %d, want preflight, snapshots, and path inspection", len(runner.invocations))
			}
		})
	}
}

func TestRestoreRejectsInvalidSelectionsBeforeRunning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		selection Selection
	}{
		{name: "unknown policy", selection: Selection{Policy: "newest"}},
		{name: "latest with ID", selection: Selection{Policy: SelectionLatest, ID: oldSnapshotID}},
		{name: "short ID", selection: Selection{Policy: SelectionID, ID: "abc"}},
		{name: "shell metacharacters", selection: Selection{Policy: SelectionID, ID: oldSnapshotID + "; touch /tmp/injected"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeRunner{}
			client := newTestClient(t, runner)
			if _, err := client.Restore(context.Background(), test.selection); !errors.Is(err, ErrInvalidSelection) {
				t.Fatalf("Restore() error = %v, want ErrInvalidSelection", err)
			}
			if len(runner.invocations) != 0 {
				t.Fatalf("runner calls = %d, want 0", len(runner.invocations))
			}
		})
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*Config)
	}{
		{name: "target traversal", edit: func(config *Config) { config.TargetPath = "/restore/../production" }},
		{name: "target outside root", edit: func(config *Config) { config.TargetPath = "/etc/restore" }},
		{name: "noncanonical target", edit: func(config *Config) { config.TargetPath = "/restore//sqlserver" }},
		{name: "repository credentials", edit: func(config *Config) { config.Repository = "s3:https://user:password@objects.example.test/backups" }},
		{name: "repository query", edit: func(config *Config) { config.Repository = "s3:https://objects.example.test/backups?access_key=secret" }},
		{name: "remote plaintext endpoint", edit: func(config *Config) { config.Repository = "s3:http://objects.example.test/backups" }},
		{name: "repository option", edit: func(config *Config) { config.Repository = "--repo" }},
		{name: "too-large output bound", edit: func(config *Config) { config.MaxOutputBytes = maxOutputBytes + 1 }},
		{name: "unapproved environment variable", edit: func(config *Config) {
			config.Environment = append(config.Environment, EnvironmentVariable{Name: "PATH", Value: "/tmp"})
		}},
		{name: "duplicate environment name", edit: func(config *Config) {
			config.Environment = append(config.Environment, EnvironmentVariable{Name: "RESTIC_PASSWORD", Value: "another"})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := testConfig()
			test.edit(&config)
			if _, err := New(config, &fakeRunner{}); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New() error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestNewAllowsLoopbackHTTPForLocalS3Development(t *testing.T) {
	t.Parallel()

	config := testConfig()
	config.Repository = "s3:http://127.0.0.1:9000/backups"
	if _, err := New(config, &fakeRunner{}); err != nil {
		t.Fatalf("New(loopback HTTP) error = %v, want nil", err)
	}
}

func TestParseSnapshotsRejectsMalformedOrUnexpectedJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{name: "invalid JSON", data: `[{"id":`},
		{name: "wrong root", data: `{}`},
		{name: "empty ID", data: `[{"id":"","time":"2024-01-01T00:00:00Z"}]`},
		{name: "short ID", data: `[{"id":"abc","time":"2024-01-01T00:00:00Z"}]`},
		{name: "invalid timestamp", data: `[{"id":"` + oldSnapshotID + `","time":"yesterday"}]`},
		{name: "unknown field", data: `[{"id":"` + oldSnapshotID + `","time":"2024-01-01T00:00:00Z","password":"secret"}]`},
		{name: "duplicate key", data: `[{"id":"` + oldSnapshotID + `","id":"` + newSnapshotID + `","time":"2024-01-01T00:00:00Z"}]`},
		{name: "duplicate snapshot ID", data: `[{"id":"` + oldSnapshotID + `","time":"2024-01-01T00:00:00Z"},{"id":"` + oldSnapshotID + `","time":"2024-01-02T00:00:00Z"}]`},
		{name: "trailing value", data: `[] []`},
		{name: "null", data: `null`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseSnapshots([]byte(test.data)); !errors.Is(err, ErrInvalidSnapshotOutput) {
				t.Fatalf("parseSnapshots() error = %v, want ErrInvalidSnapshotOutput", err)
			}
		})
	}
}

func TestRestoreReportsNotFoundAndDoesNotRestore(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{responses: [][]byte{[]byte("restic"), []byte(snapshotsJSON)}}
	client := newTestClient(t, runner)
	_, err := client.Restore(context.Background(), Selection{Policy: SelectionID, ID: strings.Repeat("c", 64)})
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("Restore() error = %v, want ErrSnapshotNotFound", err)
	}
	if len(runner.invocations) != 2 {
		t.Fatalf("runner calls = %d, want preflight and list only", len(runner.invocations))
	}
}

func TestRestoreBoundsOutputAndHidesRunnerErrors(t *testing.T) {
	t.Parallel()

	t.Run("returned oversized output", func(t *testing.T) {
		t.Parallel()
		config := testConfig()
		config.MaxOutputBytes = 16
		runner := &fakeRunner{responses: [][]byte{[]byte(strings.Repeat("x", 17))}}
		client, err := New(config, runner)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		_, err = client.Restore(context.Background(), Selection{Policy: SelectionLatest})
		if !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("Restore() error = %v, want ErrOutputLimit", err)
		}
	})

	t.Run("runner error is opaque", func(t *testing.T) {
		t.Parallel()
		runner := &fakeRunner{errors: []error{fmt.Errorf("command failed with %s", testSecret)}}
		client := newTestClient(t, runner)
		_, err := client.Restore(context.Background(), Selection{Policy: SelectionLatest})
		if !errors.Is(err, ErrCommandFailed) {
			t.Fatalf("Restore() error = %v, want ErrCommandFailed", err)
		}
		if strings.Contains(err.Error(), testSecret) {
			t.Fatal("runner error exposed secret value")
		}
	})
}

func TestRestoreHonorsCancellationAndTimeout(t *testing.T) {
	t.Parallel()

	t.Run("caller cancellation", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runner := &fakeRunner{waitForContext: true}
		client := newTestClient(t, runner)
		_, err := client.Restore(ctx, Selection{Policy: SelectionLatest})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Restore() error = %v, want context.Canceled", err)
		}
	})

	t.Run("request timeout", func(t *testing.T) {
		t.Parallel()
		config := testConfig()
		config.Timeout = 10 * time.Millisecond
		runner := &fakeRunner{waitForContext: true}
		client, err := New(config, runner)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		_, err = client.Restore(context.Background(), Selection{Policy: SelectionLatest})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Restore() error = %v, want context.DeadlineExceeded", err)
		}
	})
}

func newTestClient(t *testing.T, runner *fakeRunner) *Client {
	t.Helper()
	client, err := New(testConfig(), runner)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func testConfig() Config {
	return Config{
		Repository: "s3:https://objects.example.test/backups",
		TargetRoot: "/restore",
		TargetPath: "/restore/sqlserver",
		Timeout:    time.Minute,
		Environment: []EnvironmentVariable{
			{Name: "RESTIC_PASSWORD", Value: testSecret},
		},
	}
}

type fakeRunner struct {
	invocations    []Invocation
	responses      [][]byte
	errors         []error
	streamData     string
	streamErr      error
	waitForContext bool
}

func (runner *fakeRunner) Run(ctx context.Context, invocation Invocation) ([]byte, error) {
	copied := invocation
	copied.Args = append([]string(nil), invocation.Args...)
	copied.Environment = append([]EnvironmentVariable(nil), invocation.Environment...)
	runner.invocations = append(runner.invocations, copied)

	if runner.waitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	index := len(runner.invocations) - 1
	if index < len(runner.errors) && runner.errors[index] != nil {
		return nil, runner.errors[index]
	}
	if index < len(runner.responses) {
		return runner.responses[index], nil
	}
	return nil, nil
}

func (runner *fakeRunner) Stream(_ context.Context, invocation Invocation) (io.ReadCloser, error) {
	copied := invocation
	copied.Args = append([]string(nil), invocation.Args...)
	copied.Environment = append([]EnvironmentVariable(nil), invocation.Environment...)
	runner.invocations = append(runner.invocations, copied)
	if runner.streamErr != nil {
		return nil, runner.streamErr
	}
	return io.NopCloser(strings.NewReader(runner.streamData)), nil
}
