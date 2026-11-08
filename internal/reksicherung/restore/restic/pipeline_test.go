package restic

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/config"
	"github.com/1k-off/reksicherung/internal/reksicherung/evidence"
	sqlserverrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/sqlserver"
	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

const (
	pipelineRunID  = "restic-pipeline"
	pipelineSecret = "restic-test-secret"
)

func TestRepositoryAddressEncodesS3CoordinatesWithoutCredentials(t *testing.T) {
	tests := []struct {
		name     string
		storage  config.S3Storage
		expected string
	}{
		{
			name: "custom endpoint and escaped prefix",
			storage: config.S3Storage{
				Type: "s3", Endpoint: "https://objects.example.test", Bucket: "backup-data",
				Prefix: "tenant one/production%2F",
			},
			expected: "s3:https://objects.example.test/backup-data/tenant%20one/production%252F",
		},
		{
			name: "provider endpoint",
			storage: config.S3Storage{
				Type: "s3", Bucket: "backup-data", Prefix: "tenant/production",
			},
			expected: "s3:https://s3.amazonaws.com/backup-data/tenant/production",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := RepositoryAddress(test.storage)
			if err != nil {
				t.Fatalf("RepositoryAddress() error = %v", err)
			}
			if got != test.expected {
				t.Fatalf("RepositoryAddress() = %q, want %q", got, test.expected)
			}
			if strings.Contains(got, pipelineSecret) || strings.Contains(got, "@") {
				t.Fatalf("repository address contains credential syntax: %q", got)
			}
		})
	}
}

func TestRepositoryAddressRejectsInvalidOrCredentialBearingEndpoint(t *testing.T) {
	for _, storage := range []config.S3Storage{
		{Type: "s3", Bucket: "bad_bucket"},
		{Type: "s3", Bucket: "valid-bucket", Prefix: "a/../b"},
		{Type: "s3", Bucket: "valid-bucket", Endpoint: "https://user:secret@objects.example.test"},
		{Type: "s3", Bucket: "valid-bucket", Endpoint: "http://objects.example.test"},
		{Type: "s3", Bucket: "valid-bucket", Endpoint: "https://objects.example.test/path"},
	} {
		if _, err := RepositoryAddress(storage); !errors.Is(err, ErrInvalidRestoreRequest) {
			t.Errorf("RepositoryAddress(%#v) error = %v, want ErrInvalidRestoreRequest", storage, err)
		}
	}
}

func TestPipelineStreamsOnlyConfiguredBackupAndRecordsDBCCAsInconclusive(t *testing.T) {
	runtime := &pipelineRuntime{}
	operations := &pipelineOperations{}
	runner := &pipelineRunner{archive: strings.Repeat("backup-data-", 100)}
	pipeline := newPipeline(t, runtime, operations, runner)
	request := validPipelineRequest()

	result, err := pipeline.Restore(context.Background(), request)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if result.SnapshotID != newSnapshotID || result.Database == "" ||
		!result.IntegrityCommandCompleted || result.IntegrityAssessment != "inconclusive" || result.Retained {
		t.Fatalf("Restore() result = %#v", result)
	}
	if got := runner.invocations[len(runner.invocations)-1].Args; !equalArgs(got,
		[]string{"--repo", "s3:https://objects.example.test/backup-data/team%20one/prod", "dump", newSnapshotID, "/var/opt/mssql/backup/example.bak"}) {
		t.Fatalf("Restic dump argv = %#v", got)
	}
	if string(operations.backup) != runner.archive {
		t.Fatalf("restored bytes = %d bytes, want streamed fixture (%d bytes)", len(operations.backup), len(runner.archive))
	}
	if operations.database != request.Source.Database.Name {
		t.Fatalf("expected source database = %q, want catalog database %q", operations.database, request.Source.Database.Name)
	}
	wantEnvironment := []EnvironmentVariable{
		{Name: "RESTIC_PASSWORD", Value: pipelineSecret},
		{Name: "AWS_ACCESS_KEY_ID", Value: "access-key-value"},
		{Name: "AWS_SECRET_ACCESS_KEY", Value: "secret-key-value"},
		{Name: "AWS_DEFAULT_REGION", Value: "eu-central-1"},
	}
	if !equalEnvironment(runner.invocations[1].Environment, wantEnvironment) ||
		!equalEnvironment(runner.invocations[2].Environment, wantEnvironment) ||
		!equalEnvironment(runner.invocations[len(runner.invocations)-1].Environment, wantEnvironment) {
		t.Fatalf("Restic environment differs from explicit allowed set: %#v", runner.invocations)
	}
	if runtime.removeCalls != 1 || runtime.spec.RetainForReview || runtime.spec.Network != "reksicherung-egress" {
		t.Fatalf("cleanup state: spec=%#v remove calls=%d", runtime.spec, runtime.removeCalls)
	}
	evidenceSink := request.EvidenceSink.(*pipelineEvidence)
	if got := requestEvidenceEvents(evidenceSink); got != "container_creation:started,container_creation:succeeded,sql_readiness:started,sql_readiness:succeeded,restic_selection:started,restic_selection:succeeded,native_backup_restore:started,native_backup_restore:succeeded,dbcc_checkdb:started,dbcc_checkdb:succeeded,cleanup:started,cleanup:succeeded" {
		t.Fatalf("evidence event order = %s", got)
	}
	for _, invocation := range runner.invocations {
		if strings.Contains(strings.Join(invocation.Args, "\x00"), pipelineSecret) {
			t.Fatal("secret appeared in Restic argv")
		}
	}
}

func TestPipelineCleansOnFailureAndRetainsForReview(t *testing.T) {
	for _, cleanup := range []string{"delete", "hold_for_review"} {
		t.Run(cleanup, func(t *testing.T) {
			runtime := &pipelineRuntime{}
			operations := &pipelineOperations{restoreErr: errors.New("SQL diagnostics contain " + pipelineSecret)}
			pipeline := newPipeline(t, runtime, operations, &pipelineRunner{archive: "archive"})
			request := validPipelineRequest()
			request.Source.Cleanup = cleanup

			_, err := pipeline.Restore(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), "restore native SQL Server backup failed") ||
				strings.Contains(err.Error(), pipelineSecret) || strings.Contains(err.Error(), "diagnostics") {
				t.Fatalf("Restore() error = %v, want sanitized restore error", err)
			}
			if cleanup == "delete" && runtime.removeCalls != 1 {
				t.Fatalf("delete cleanup calls = %d, want 1", runtime.removeCalls)
			}
			if cleanup == "hold_for_review" && (runtime.removeCalls != 0 || !runtime.spec.RetainForReview) {
				t.Fatalf("hold_for_review state: spec=%#v remove calls=%d", runtime.spec, runtime.removeCalls)
			}
			sink := request.EvidenceSink.(*pipelineEvidence)
			if !hasEvidenceEvent(sink.events, "native_backup_restore", evidence.StatusFailed, "native SQL Server backup restore failed") {
				t.Fatalf("restore failure evidence = %#v", sink.events)
			}
		})
	}
}

func TestPipelineSanitizesCleanupFailure(t *testing.T) {
	runtime := &pipelineRuntime{removeErr: errors.New("podman diagnostics contain " + pipelineSecret)}
	pipeline := newPipeline(t, runtime, &pipelineOperations{}, &pipelineRunner{archive: "archive"})

	_, err := pipeline.Restore(context.Background(), validPipelineRequest())
	if err == nil || !strings.Contains(err.Error(), "remove SQL Server restore container failed") ||
		strings.Contains(err.Error(), pipelineSecret) || strings.Contains(err.Error(), "diagnostics") {
		t.Fatalf("Restore() error = %v, want sanitized cleanup failure", err)
	}
}

func TestPipelineRejectsUnsafePathAndSecretsBeforeStartingContainer(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Request)
	}{
		{name: "traversal", change: func(request *Request) { request.Source.BackupPath = "/var/backups/../secret.bak" }},
		{name: "glob", change: func(request *Request) { request.Source.BackupPath = "/var/backups/*.bak" }},
		{name: "relative path", change: func(request *Request) { request.Source.BackupPath = "backup.bak" }},
		{name: "missing resolved secret", change: func(request *Request) { request.Secrets.RepositoryPassword = "" }},
		{name: "secret with newline", change: func(request *Request) { request.Secrets.S3SecretKey = "secret\nvalue" }},
		{name: "invalid selection", change: func(request *Request) { request.Source.Selection = config.Selection{Policy: "id", ID: "short"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &pipelineRuntime{}
			pipeline := newPipeline(t, runtime, &pipelineOperations{}, &pipelineRunner{})
			request := validPipelineRequest()
			test.change(&request)
			if _, err := pipeline.Restore(context.Background(), request); !errors.Is(err, ErrInvalidRestoreRequest) {
				t.Fatalf("Restore() error = %v, want ErrInvalidRestoreRequest", err)
			}
			if runtime.startCalls != 0 {
				t.Fatalf("container start calls = %d, want zero", runtime.startCalls)
			}
		})
	}
}

func TestPipelineEnforcesConfiguredArchiveLimitWithoutBuffering(t *testing.T) {
	runtime := &pipelineRuntime{}
	operations := &pipelineOperations{}
	pipeline := newPipeline(t, runtime, operations, &pipelineRunner{archive: "123456789"})
	pipeline.dependencies.MaxBackupBytes = 8

	_, err := pipeline.Restore(context.Background(), validPipelineRequest())
	if !errors.Is(err, ErrBackupTooLarge) {
		t.Fatalf("Restore() error = %v, want ErrBackupTooLarge", err)
	}
	if runtime.removeCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", runtime.removeCalls)
	}
}

func TestPipelineRequiresEvidenceBeforeSideEffects(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Request)
	}{
		{
			name: "missing sink",
			change: func(request *Request) {
				request.EvidenceSink = nil
			},
		},
		{
			name: "append failure",
			change: func(request *Request) {
				request.EvidenceSink.(*pipelineEvidence).err = errors.New("private evidence failure")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &pipelineRuntime{}
			pipeline := newPipeline(t, runtime, &pipelineOperations{}, &pipelineRunner{})
			request := validPipelineRequest()
			test.change(&request)
			if _, err := pipeline.Restore(context.Background(), request); err == nil {
				t.Fatal("Restore() error = nil, want evidence validation/append failure")
			}
			if runtime.startCalls != 0 {
				t.Fatalf("container start calls = %d, want zero", runtime.startCalls)
			}
		})
	}
}

func newPipeline(t *testing.T, runtime *pipelineRuntime, operations *pipelineOperations, runner *pipelineRunner) *Pipeline {
	t.Helper()
	pipeline, err := NewPipeline(Dependencies{
		Runtime: runtime,
		NewResticRunner: func(container.Container) (Runner, error) {
			return runner, nil
		},
		NewDatabaseOperations: func(_ container.Container, database string, _ string) (DatabaseOperations, error) {
			operations.targetDatabase = database
			return operations, nil
		},
		EgressNetwork:  "reksicherung-egress",
		DiskBytes:      2 * 1024 * 1024 * 1024,
		MaxBackupBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("NewPipeline() error = %v", err)
	}
	return pipeline
}

func validPipelineRequest() Request {
	return Request{
		RunID:        pipelineRunID,
		EvidenceSink: &pipelineEvidence{},
		Source: config.BackupSource{
			ID:                    "sql-production",
			Provider:              "restic",
			RepositoryPasswordRef: "openbao://secret/reksicherung/restic#password",
			BackupName:            "sql-production",
			BackupPath:            "/var/opt/mssql/backup/example.bak",
			Database:              config.Database{Type: "sqlserver", Name: "Example"},
			Storage: config.S3Storage{
				Type: "s3", Endpoint: "https://objects.example.test", Bucket: "backup-data",
				Region: "eu-central-1", Prefix: "team one/prod",
				Credentials: config.S3Credentials{
					AccessKeyRef: "openbao://secret/reksicherung/s3#access",
					SecretKeyRef: "openbao://secret/reksicherung/s3#secret",
				},
			},
			Selection: config.Selection{Policy: "latest"},
			Restore: config.Restore{
				Image:    "restore-image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				CPUMilli: 1500, MemoryMiB: 2048, Timeout: "2h",
			},
			Cleanup: "delete",
		},
		Secrets: ResolvedSecrets{
			RepositoryPassword: pipelineSecret,
			S3AccessKey:        "access-key-value",
			S3SecretKey:        "secret-key-value",
			SQLServerPassword:  "sql-server-password",
		},
	}
}

type pipelineEvidence struct {
	events []evidence.Event
	err    error
}

func (sink *pipelineEvidence) Append(_ context.Context, event evidence.Event) error {
	if sink.err != nil {
		return sink.err
	}
	sink.events = append(sink.events, event)
	return nil
}

func requestEvidenceEvents(sink *pipelineEvidence) string {
	var events []string
	for _, event := range sink.events {
		events = append(events, event.Operation+":"+string(event.Status))
	}
	return strings.Join(events, ",")
}

func hasEvidenceEvent(events []evidence.Event, operation string, status evidence.Status, eventError string) bool {
	for _, event := range events {
		if event.Operation == operation && event.Status == status && event.Error == eventError {
			return true
		}
	}
	return false
}

type pipelineRuntime struct {
	spec        container.Spec
	startCalls  int
	removeCalls int
	removeErr   error
}

func (runtime *pipelineRuntime) Start(_ context.Context, spec container.Spec) (container.Container, error) {
	runtime.startCalls++
	runtime.spec = spec
	return container.Container{ID: "0123456789ab", Name: container.ContainerName(spec.RunID), RunID: spec.RunID}, nil
}
func (runtime *pipelineRuntime) Remove(context.Context, container.Container) error {
	runtime.removeCalls++
	return runtime.removeErr
}
func (*pipelineRuntime) Exec(context.Context, container.Container, []string) ([]byte, error) {
	return nil, nil
}
func (*pipelineRuntime) ExecWithEnvironment(context.Context, container.Container, []string, []container.EnvironmentVariable) ([]byte, error) {
	return nil, nil
}
func (*pipelineRuntime) Transfer(context.Context, container.Container, string, io.ReadCloser, int64) error {
	return nil
}

type pipelineOperations struct {
	backup         []byte
	restoreErr     error
	database       string
	targetDatabase string
}

func (*pipelineOperations) WaitReady(context.Context) error { return nil }
func (operations *pipelineOperations) RestoreBackup(_ context.Context, archive io.ReadCloser, expectedSourceDatabase string) error {
	defer archive.Close()
	operations.database = expectedSourceDatabase
	backup, err := io.ReadAll(archive)
	operations.backup = backup
	if err != nil {
		return err
	}
	return operations.restoreErr
}
func (operations *pipelineOperations) CheckIntegrity(context.Context) (sqlserverrestore.IntegrityResult, error) {
	return sqlserverrestore.IntegrityResult{Database: operations.targetDatabase, CommandCompleted: true}, nil
}

type pipelineRunner struct {
	invocations []Invocation
	archive     string
}

func (runner *pipelineRunner) Run(_ context.Context, invocation Invocation) ([]byte, error) {
	runner.invocations = append(runner.invocations, invocation)
	switch {
	case len(invocation.Args) == 1 && invocation.Args[0] == "version":
		return []byte("restic 0.17.3"), nil
	case len(invocation.Args) > 2 && invocation.Args[2] == "snapshots":
		return []byte(snapshotsJSON), nil
	case len(invocation.Args) > 2 && invocation.Args[2] == "ls":
		return []byte(`{"message_type":"node","struct_type":"node","type":"file","path":"/var/opt/mssql/backup/example.bak"}`), nil
	default:
		return nil, errors.New("unexpected Restic command")
	}
}

func (runner *pipelineRunner) Stream(_ context.Context, invocation Invocation) (io.ReadCloser, error) {
	runner.invocations = append(runner.invocations, invocation)
	return io.NopCloser(strings.NewReader(runner.archive)), nil
}

func equalArgs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalEnvironment(left, right []EnvironmentVariable) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
