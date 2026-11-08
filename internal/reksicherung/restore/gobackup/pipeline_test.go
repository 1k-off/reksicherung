package gobackup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/config"
	"github.com/1k-off/reksicherung/internal/reksicherung/evidence"
	backupstore "github.com/1k-off/reksicherung/internal/reksicherung/mcp/gobackup"
	sqlserverrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/sqlserver"
	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
	"github.com/1k-off/reksicherung/internal/reksicherung/storage"
)

const (
	pipelineTestRunID  = "pipeline-test"
	pipelineTestSecret = "pipeline-private-password"
)

func TestPipelineOrdersRestoreAndUsesBoundedRunResources(t *testing.T) {
	var events []string
	evidenceSink := &pipelineTestEvidence{runID: pipelineTestRunID}
	repository := &pipelineTestRepository{events: &events, selected: backupstore.Backup{ID: "nightly.tar.gz"}}
	runtime := &pipelineTestRuntime{events: &events}
	pipeline := newPipelineTest(t, runtime, repository, &pipelineTestImporter{events: &events}, &pipelineTestOperations{events: &events})

	request := pipelineTestRequest("latest", "")
	request.EvidenceSink = evidenceSink
	result, err := pipeline.Restore(context.Background(), request)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	wantEvents := []string{"latest", "start", "ready", "open", "import", "integrity", "remove"}
	if !equalStrings(events, wantEvents) {
		t.Fatalf("operation order = %v, want %v", events, wantEvents)
	}
	database, err := sqlserverrestore.DatabaseNameForRun(pipelineTestRunID)
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupID != "nightly.tar.gz" || result.Database != database ||
		!result.IntegrityCommandCompleted || result.Retained {
		t.Fatalf("Restore() result = %#v", result)
	}

	spec := runtime.spec
	if spec.RunID != pipelineTestRunID || spec.Image != "restore-image@sha256:abc" ||
		spec.CPUs != 1.5 || spec.MemoryBytes != 2048*1024*1024 ||
		spec.DiskBytes != 2*1024*1024*1024 || spec.Timeout != 90*time.Minute ||
		spec.RetainForReview {
		t.Fatalf("container spec = %#v", spec)
	}
	if len(spec.Environment) != 2 ||
		spec.Environment[0] != (container.EnvironmentVariable{Name: "ACCEPT_EULA", Value: "Y"}) ||
		spec.Environment[1] != (container.EnvironmentVariable{Name: "MSSQL_SA_PASSWORD", Value: pipelineTestSecret}) {
		t.Fatalf("container environment = %#v", spec.Environment)
	}
	if repository.openedID != "nightly.tar.gz" {
		t.Fatalf("opened artifact ID = %q", repository.openedID)
	}
	assertEvidenceSequence(t, evidenceSink.events, []string{
		"gobackup_selection:started", "gobackup_selection:succeeded",
		"container_creation:started", "container_creation:succeeded",
		"sql_readiness:started", "sql_readiness:succeeded",
		"archive_open:started", "archive_open:succeeded",
		"archive_import:started", "archive_import:succeeded",
		"dbcc_checkdb:started", "dbcc_checkdb:succeeded",
		"cleanup:started", "cleanup:succeeded",
	})
	for _, event := range evidenceSink.events {
		if event.Timestamp.Location() != time.UTC || event.Timestamp.IsZero() {
			t.Errorf("event timestamp is not UTC: %#v", event.Timestamp)
		}
		if event.Operation == "gobackup_selection" && event.Status == evidence.StatusSucceeded {
			if event.Metadata["source_id"] != "test-source" ||
				event.Metadata["backup_id"] != artifactEvidenceID("nightly.tar.gz") ||
				len(event.Metadata["backup_id"]) != 64 {
				t.Errorf("selection evidence identifiers = %#v", event.Metadata)
			}
		}
		if event.Operation == "container_creation" && event.Status == evidence.StatusSucceeded &&
			event.Metadata["resource_id"] != "container-id" {
			t.Errorf("container creation evidence lacks resource identifier: %#v", event)
		}
		if event.Operation == "dbcc_checkdb" && event.Status == evidence.StatusSucceeded &&
			event.Metadata["outcome"] != "command_completed" {
			t.Errorf("DBCC event does not describe command completion only: %#v", event)
		}
	}
	encoded, err := json.Marshal(evidenceSink.events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), pipelineTestSecret) || strings.Contains(string(encoded), "nightly.tar.gz") {
		t.Fatalf("evidence contains secret or raw object identity: %s", encoded)
	}
}

func TestPipelineExplicitSelectionAndRetainedMode(t *testing.T) {
	var events []string
	evidenceSink := &pipelineTestEvidence{runID: pipelineTestRunID}
	repository := &pipelineTestRepository{events: &events, selected: backupstore.Backup{ID: "chosen.tgz"}}
	runtime := &pipelineTestRuntime{events: &events}
	pipeline := newPipelineTest(t, runtime, repository, &pipelineTestImporter{events: &events}, &pipelineTestOperations{events: &events})

	request := pipelineTestRequest("id", "chosen.tgz")
	request.Source.Cleanup = "hold_for_review"
	request.EvidenceSink = evidenceSink
	result, err := pipeline.Restore(context.Background(), request)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	wantEvents := []string{"select", "start", "ready", "open", "import", "integrity"}
	if !equalStrings(events, wantEvents) {
		t.Fatalf("operation order = %v, want %v", events, wantEvents)
	}
	if !runtime.spec.RetainForReview || !result.Retained || result.Container.ID == "" {
		t.Fatalf("retained state: spec=%#v result=%#v", runtime.spec, result)
	}
	if repository.selectedID != "chosen.tgz" {
		t.Fatalf("selected ID = %q, want chosen.tgz", repository.selectedID)
	}
	last := evidenceSink.events[len(evidenceSink.events)-1]
	if last.Operation != "cleanup" || last.Status != evidence.StatusSkipped ||
		last.Metadata["outcome"] != "retained" || last.Metadata["resource_id"] != "container-id" {
		t.Fatalf("retained cleanup evidence = %#v", last)
	}
	if runtime.removeCalls != 0 {
		t.Fatalf("retained mode removed container %d times", runtime.removeCalls)
	}
}

func TestPipelineCleansAutomaticContainersAfterEveryPostStartFailure(t *testing.T) {
	tests := []struct {
		name      string
		failAt    string
		wantEvent string
	}{
		{name: "readiness", failAt: "ready", wantEvent: "wait for SQL Server readiness failed"},
		{name: "open", failAt: "open", wantEvent: "open GoBackup archive failed"},
		{name: "import", failAt: "import", wantEvent: "import GoBackup BACPAC failed"},
		{name: "integrity", failAt: "integrity", wantEvent: "DBCC CHECKDB command failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var events []string
			repository := &pipelineTestRepository{events: &events, selected: backupstore.Backup{ID: "nightly.tar"}}
			if test.failAt == "open" {
				repository.openErr = errors.New("storage diagnostic contains " + pipelineTestSecret)
			}
			runtime := &pipelineTestRuntime{events: &events}
			importer := &pipelineTestImporter{events: &events}
			if test.failAt == "import" {
				importer.err = errors.New("SqlPackage diagnostic contains " + pipelineTestSecret)
			}
			operations := &pipelineTestOperations{events: &events}
			if test.failAt == "ready" {
				operations.readyErr = errors.New("SQL diagnostic contains " + pipelineTestSecret)
			}
			if test.failAt == "integrity" {
				operations.integrityErr = errors.New("DBCC diagnostic contains " + pipelineTestSecret)
			}
			pipeline := newPipelineTest(t, runtime, repository, importer, operations)

			_, err := pipeline.Restore(context.Background(), pipelineTestRequest("latest", ""))
			if err == nil || !strings.Contains(err.Error(), test.wantEvent) {
				t.Fatalf("Restore() error = %v, want %q", err, test.wantEvent)
			}
			if strings.Contains(err.Error(), pipelineTestSecret) || strings.Contains(err.Error(), "diagnostic") {
				t.Fatalf("Restore() leaked runtime details: %v", err)
			}
			if runtime.removeCalls != 1 || events[len(events)-1] != "remove" {
				t.Fatalf("container cleanup calls=%d, events=%v", runtime.removeCalls, events)
			}
		})
	}
}

func TestPipelineSurfacesSanitizedCleanupFailure(t *testing.T) {
	var events []string
	repository := &pipelineTestRepository{events: &events, selected: backupstore.Backup{ID: "nightly.tar"}}
	runtime := &pipelineTestRuntime{events: &events, removeErr: errors.New("podman detail " + pipelineTestSecret)}
	pipeline := newPipelineTest(t, runtime, repository, &pipelineTestImporter{events: &events}, &pipelineTestOperations{events: &events})

	_, err := pipeline.Restore(context.Background(), pipelineTestRequest("latest", ""))
	if err == nil || !strings.Contains(err.Error(), "remove SQL Server restore container failed") {
		t.Fatalf("Restore() error = %v, want sanitized cleanup failure", err)
	}
	if strings.Contains(err.Error(), pipelineTestSecret) || strings.Contains(err.Error(), "podman detail") {
		t.Fatalf("Restore() leaked cleanup error: %v", err)
	}
}

func TestPipelineCancellationStillCleansUpWithoutCanceledContext(t *testing.T) {
	var events []string
	evidenceSink := &pipelineTestEvidence{runID: pipelineTestRunID}
	ctx, cancel := context.WithCancel(context.Background())
	repository := &pipelineTestRepository{events: &events, selected: backupstore.Backup{ID: "nightly.tar"}}
	runtime := &pipelineTestRuntime{events: &events}
	operations := &pipelineTestOperations{
		events: &events,
		wait: func(context.Context) error {
			cancel()
			return context.Canceled
		},
	}
	pipeline := newPipelineTest(t, runtime, repository, &pipelineTestImporter{events: &events}, operations)

	request := pipelineTestRequest("latest", "")
	request.EvidenceSink = evidenceSink
	_, err := pipeline.Restore(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Restore() error = %v, want context.Canceled", err)
	}
	if runtime.removeCalls != 1 || runtime.removeContextErr != nil {
		t.Fatalf("cleanup calls=%d cleanup context error=%v", runtime.removeCalls, runtime.removeContextErr)
	}
	assertEvidenceSequence(t, evidenceSink.events, []string{
		"gobackup_selection:started", "gobackup_selection:succeeded",
		"container_creation:started", "container_creation:succeeded",
		"sql_readiness:started", "sql_readiness:canceled",
		"cleanup:started", "cleanup:succeeded",
	})
}

func TestPipelineEvidenceAppendFailureIsFailSafe(t *testing.T) {
	t.Run("before selection side effect", func(t *testing.T) {
		var events []string
		sink := &pipelineTestEvidence{
			runID: pipelineTestRunID,
			fail: func(event evidence.Event) bool {
				return event.Operation == "gobackup_selection" && event.Status == evidence.StatusStarted
			},
		}
		runtime := &pipelineTestRuntime{events: &events}
		repository := &pipelineTestRepository{events: &events, selected: backupstore.Backup{ID: "nightly.tar"}}
		pipeline := newPipelineTest(t, runtime, repository, &pipelineTestImporter{events: &events}, &pipelineTestOperations{events: &events})
		request := pipelineTestRequest("latest", "")
		request.EvidenceSink = sink

		_, err := pipeline.Restore(context.Background(), request)
		if !errors.Is(err, ErrEvidenceAppend) {
			t.Fatalf("Restore() error = %v, want ErrEvidenceAppend", err)
		}
		if len(events) != 0 || runtime.startCalls != 0 {
			t.Fatalf("side effects started after evidence failure: events=%v starts=%d", events, runtime.startCalls)
		}
	})

	t.Run("after container creation still cleans up", func(t *testing.T) {
		var events []string
		sink := &pipelineTestEvidence{
			runID: pipelineTestRunID,
			fail: func(event evidence.Event) bool {
				return event.Operation == "container_creation" && event.Status == evidence.StatusSucceeded
			},
		}
		runtime := &pipelineTestRuntime{events: &events}
		repository := &pipelineTestRepository{events: &events, selected: backupstore.Backup{ID: "nightly.tar"}}
		pipeline := newPipelineTest(t, runtime, repository, &pipelineTestImporter{events: &events}, &pipelineTestOperations{events: &events})
		request := pipelineTestRequest("latest", "")
		request.EvidenceSink = sink

		_, err := pipeline.Restore(context.Background(), request)
		if !errors.Is(err, ErrEvidenceAppend) {
			t.Fatalf("Restore() error = %v, want ErrEvidenceAppend", err)
		}
		if runtime.removeCalls != 1 || events[len(events)-1] != "remove" {
			t.Fatalf("container was not cleaned after evidence failure: calls=%d events=%v", runtime.removeCalls, events)
		}
		assertEvidenceSequence(t, sink.events, []string{
			"gobackup_selection:started", "gobackup_selection:succeeded",
			"container_creation:started", "cleanup:started", "cleanup:succeeded",
		})
	})
}

func TestPipelineCleanupStillRunsWhenCleanupEvidenceAppendFails(t *testing.T) {
	var events []string
	sink := &pipelineTestEvidence{
		runID: pipelineTestRunID,
		fail: func(event evidence.Event) bool {
			return event.Operation == "cleanup" && event.Status == evidence.StatusStarted
		},
	}
	runtime := &pipelineTestRuntime{events: &events}
	repository := &pipelineTestRepository{events: &events, selected: backupstore.Backup{ID: "nightly.tar"}}
	pipeline := newPipelineTest(t, runtime, repository, &pipelineTestImporter{events: &events}, &pipelineTestOperations{events: &events})
	request := pipelineTestRequest("latest", "")
	request.EvidenceSink = sink

	_, err := pipeline.Restore(context.Background(), request)
	if !errors.Is(err, ErrEvidenceAppend) {
		t.Fatalf("Restore() error = %v, want ErrEvidenceAppend", err)
	}
	if runtime.removeCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1 despite evidence failure", runtime.removeCalls)
	}
}

func TestPipelineRecordsSanitizedFailureAndCleanupFailure(t *testing.T) {
	var events []string
	sink := &pipelineTestEvidence{runID: pipelineTestRunID}
	repository := &pipelineTestRepository{
		events: &events, selected: backupstore.Backup{ID: "nightly.tar"},
		openErr: errors.New("provider runtime detail " + pipelineTestSecret),
	}
	runtime := &pipelineTestRuntime{events: &events, removeErr: errors.New("runtime detail " + pipelineTestSecret)}
	pipeline := newPipelineTest(t, runtime, repository, &pipelineTestImporter{events: &events}, &pipelineTestOperations{events: &events})
	request := pipelineTestRequest("latest", "")
	request.EvidenceSink = sink

	_, err := pipeline.Restore(context.Background(), request)
	if err == nil || strings.Contains(err.Error(), pipelineTestSecret) || strings.Contains(err.Error(), "runtime detail") {
		t.Fatalf("Restore() error is missing or unsafe: %v", err)
	}
	assertEvidenceSequence(t, sink.events, []string{
		"gobackup_selection:started", "gobackup_selection:succeeded",
		"container_creation:started", "container_creation:succeeded",
		"sql_readiness:started", "sql_readiness:succeeded",
		"archive_open:started", "archive_open:failed",
		"cleanup:started", "cleanup:failed",
	})
	encoded, marshalErr := json.Marshal(sink.events)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), pipelineTestSecret) || strings.Contains(string(encoded), "provider runtime detail") {
		t.Fatalf("failure evidence leaked runtime details: %s", encoded)
	}
	for _, event := range sink.events {
		if event.Status == evidence.StatusFailed && event.Error != "restore operation failed" && event.Error != "container cleanup failed" {
			t.Errorf("failed event has non-generic error: %#v", event)
		}
	}
}

func TestPipelineRequiresEvidenceRunIDMatch(t *testing.T) {
	var events []string
	runtime := &pipelineTestRuntime{events: &events}
	pipeline := newPipelineTest(t, runtime, &pipelineTestRepository{events: &events}, &pipelineTestImporter{events: &events}, &pipelineTestOperations{events: &events})
	request := pipelineTestRequest("latest", "")
	request.EvidenceSink = &pipelineTestEvidence{runID: "other-run"}

	_, err := pipeline.Restore(context.Background(), request)
	if !errors.Is(err, ErrEvidenceAppend) {
		t.Fatalf("Restore() error = %v, want sanitized evidence append failure", err)
	}
	if runtime.startCalls != 0 || len(events) != 0 {
		t.Fatalf("run ID mismatch allowed side effects: starts=%d events=%v", runtime.startCalls, events)
	}
}

func TestPipelineKeepsRetainedContainerOnFailure(t *testing.T) {
	var events []string
	repository := &pipelineTestRepository{events: &events, selected: backupstore.Backup{ID: "nightly.tar"}}
	runtime := &pipelineTestRuntime{events: &events}
	operations := &pipelineTestOperations{events: &events, readyErr: errors.New("not ready")}
	pipeline := newPipelineTest(t, runtime, repository, &pipelineTestImporter{events: &events}, operations)
	request := pipelineTestRequest("latest", "")
	request.Source.Cleanup = "hold_for_review"

	if _, err := pipeline.Restore(context.Background(), request); err == nil {
		t.Fatal("Restore() error = nil, want readiness error")
	}
	if runtime.removeCalls != 0 {
		t.Fatalf("Remove() calls = %d, want zero for hold_for_review", runtime.removeCalls)
	}
	if !runtime.spec.RetainForReview {
		t.Fatalf("container spec did not preserve hold_for_review: %#v", runtime.spec)
	}
}

func TestStorageBackedPipelineComposesSelectionOpenImportAndSQLOperations(t *testing.T) {
	archive := makePipelineTar(t, "database.bacpac", "test BACPAC")
	objects := []storage.Object{
		{Key: "backups/sql/older.tar", Size: 1, LastModified: time.Unix(1, 0)},
		{Key: "backups/sql/newer.tar", Size: int64(len(archive)), LastModified: time.Unix(2, 0)},
	}
	store := &pipelineTestStore{objects: objects, bodies: map[string][]byte{"backups/sql/newer.tar": archive}}
	runtime := &pipelineTestRuntime{}
	pipeline, err := NewStoragePipeline(func(_ context.Context, source config.S3Storage, maxObjectBytes int64) (storage.Store, error) {
		if source.Type != "s3" || source.Prefix != "backups/sql" || maxObjectBytes != 1<<20 {
			t.Errorf("storage factory config = (%#v, %d)", source, maxObjectBytes)
		}
		return store, nil
	}, runtime, container.MinContainerDiskBytes, Limits{
		MaxCompressedBytes:   1 << 20,
		MaxUncompressedBytes: 1 << 20,
		MaxBACPACBytes:       1 << 20,
	})
	if err != nil {
		t.Fatalf("NewStoragePipeline() error = %v", err)
	}

	result, err := pipeline.Restore(context.Background(), pipelineTestRequest("latest", ""))
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if store.openedKey != "backups/sql/newer.tar" {
		t.Fatalf("opened key = %q, want latest object", store.openedKey)
	}
	if result.BackupID != "newer.tar" || !result.IntegrityCommandCompleted {
		t.Fatalf("Restore() result = %#v", result)
	}
	if string(runtime.transferredArtifact) != "test BACPAC" {
		t.Fatalf("transferred BACPAC = %q", runtime.transferredArtifact)
	}
	if runtime.removeCalls != 1 {
		t.Fatalf("container cleanup calls = %d, want 1", runtime.removeCalls)
	}
}

func TestPipelineRejectsInvalidRequestsBeforeStartingContainer(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Request)
	}{
		{name: "wrong provider", change: func(request *Request) { request.Source.Provider = "restic" }},
		{name: "missing prefix", change: func(request *Request) { request.Source.Storage.Prefix = "" }},
		{name: "invalid selection", change: func(request *Request) { request.Source.Selection.Policy = "anything" }},
		{name: "missing explicit ID", change: func(request *Request) { request.Source.Selection.Policy = "id" }},
		{name: "invalid cleanup", change: func(request *Request) { request.Source.Cleanup = "unknown" }},
		{name: "missing evidence sink", change: func(request *Request) { request.EvidenceSink = nil }},
		{name: "oversized password", change: func(request *Request) { request.Password = strings.Repeat("x", 4097) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var events []string
			runtime := &pipelineTestRuntime{events: &events}
			pipeline := newPipelineTest(t, runtime, &pipelineTestRepository{events: &events}, &pipelineTestImporter{events: &events}, &pipelineTestOperations{events: &events})
			request := pipelineTestRequest("latest", "")
			test.change(&request)
			if _, err := pipeline.Restore(context.Background(), request); !errors.Is(err, ErrInvalidRestoreRequest) {
				t.Fatalf("Restore() error = %v, want ErrInvalidRestoreRequest", err)
			}
			if runtime.startCalls != 0 {
				t.Fatalf("Start() calls = %d, want zero", runtime.startCalls)
			}
		})
	}
}

func newPipelineTest(t *testing.T, runtime *pipelineTestRuntime, repository *pipelineTestRepository, importer *pipelineTestImporter, operations *pipelineTestOperations) *Pipeline {
	t.Helper()
	pipeline, err := NewPipeline(Dependencies{
		Runtime:       runtime,
		DiskBytes:     2 * 1024 * 1024 * 1024,
		ArchiveLimits: testLimits,
		NewArtifacts: func(_ context.Context, source config.S3Storage, prefix string, maxObjectBytes int64) (ArtifactRepository, error) {
			if source.Type != "s3" || source.Prefix != "backups/sql" ||
				prefix != "backups/sql/" || maxObjectBytes != testLimits.MaxCompressedBytes {
				t.Errorf("artifact repository config = (%#v, %q, %d)", source, prefix, maxObjectBytes)
			}
			return repository, nil
		},
		NewImporter: func(_ container.Runtime, limits Limits, timeout time.Duration) (ImportOperation, error) {
			if limits != testLimits || timeout != 90*time.Minute {
				t.Errorf("importer config = (%#v, %v)", limits, timeout)
			}
			return importer, nil
		},
		NewDatabaseOperations: func(_ container.Container, database, password string) (DatabaseOperations, error) {
			want, err := sqlserverrestore.DatabaseNameForRun(pipelineTestRunID)
			if err != nil || database != want || password != pipelineTestSecret {
				t.Errorf("SQL operation binding = (%q, %q), want (%q, test password)", database, password, want)
			}
			return operations, nil
		},
	})
	if err != nil {
		t.Fatalf("NewPipeline() error = %v", err)
	}
	return pipeline
}

func pipelineTestRequest(policy, id string) Request {
	return Request{
		RunID:        pipelineTestRunID,
		EvidenceSink: &pipelineTestEvidence{runID: pipelineTestRunID},
		Source: config.BackupSource{
			ID:       "test-source",
			Provider: "gobackup",
			Storage: config.S3Storage{
				Type:   "s3",
				Prefix: "backups/sql",
			},
			Selection: config.Selection{Policy: policy, ID: id},
			Restore: config.Restore{
				Image:     "restore-image@sha256:abc",
				CPUMilli:  1500,
				MemoryMiB: 2048,
				Timeout:   "90m",
			},
			Cleanup: "delete",
		},
		Password: pipelineTestSecret,
	}
}

type pipelineTestEvidence struct {
	runID  string
	events []evidence.Event
	fail   func(evidence.Event) bool
}

func (sink *pipelineTestEvidence) Append(ctx context.Context, event evidence.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.RunID != sink.runID {
		return evidence.ErrRunIDMismatch
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if sink.fail != nil && sink.fail(event) {
		return errors.New("evidence recorder detail " + pipelineTestSecret)
	}
	sink.events = append(sink.events, event)
	return nil
}

func assertEvidenceSequence(t *testing.T, events []evidence.Event, want []string) {
	t.Helper()
	got := make([]string, len(events))
	for index, event := range events {
		got[index] = string(event.Operation) + ":" + string(event.Status)
	}
	if !equalStrings(got, want) {
		t.Fatalf("evidence sequence = %v, want %v", got, want)
	}
}

type pipelineTestRepository struct {
	events     *[]string
	selected   backupstore.Backup
	selectedID string
	openedID   string
	openErr    error
}

func (repository *pipelineTestRepository) Latest(context.Context) (backupstore.Backup, error) {
	*repository.events = append(*repository.events, "latest")
	return repository.selected, nil
}

func (repository *pipelineTestRepository) Select(_ context.Context, id string) (backupstore.Backup, error) {
	*repository.events = append(*repository.events, "select")
	repository.selectedID = id
	return repository.selected, nil
}

func (repository *pipelineTestRepository) Open(_ context.Context, id string) (io.ReadCloser, error) {
	*repository.events = append(*repository.events, "open")
	repository.openedID = id
	if repository.openErr != nil {
		return nil, repository.openErr
	}
	return io.NopCloser(strings.NewReader("archive")), nil
}

type pipelineTestImporter struct {
	events *[]string
	err    error
}

func (importer *pipelineTestImporter) Import(_ context.Context, _ container.Container, archive io.ReadCloser, _ Format, options ImportOptions) error {
	*importer.events = append(*importer.events, "import")
	defer archive.Close()
	if options.Database == "" || options.Username != "sa" || options.Password != pipelineTestSecret {
		return errors.New("unexpected importer options")
	}
	return importer.err
}

type pipelineTestOperations struct {
	events       *[]string
	readyErr     error
	integrityErr error
	wait         func(context.Context) error
}

func (operations *pipelineTestOperations) WaitReady(ctx context.Context) error {
	*operations.events = append(*operations.events, "ready")
	if operations.wait != nil {
		return operations.wait(ctx)
	}
	return operations.readyErr
}

func (operations *pipelineTestOperations) CheckIntegrity(context.Context) (sqlserverrestore.IntegrityResult, error) {
	*operations.events = append(*operations.events, "integrity")
	if operations.integrityErr != nil {
		return sqlserverrestore.IntegrityResult{}, operations.integrityErr
	}
	return sqlserverrestore.IntegrityResult{CommandCompleted: true}, nil
}

type pipelineTestRuntime struct {
	events              *[]string
	spec                container.Spec
	target              container.Container
	startErr            error
	removeErr           error
	startCalls          int
	removeCalls         int
	removeContextErr    error
	transferredArtifact []byte
}

func (runtime *pipelineTestRuntime) Start(_ context.Context, spec container.Spec) (container.Container, error) {
	runtime.startCalls++
	runtime.spec = spec
	if runtime.events != nil {
		*runtime.events = append(*runtime.events, "start")
	}
	if runtime.startErr != nil {
		return container.Container{}, runtime.startErr
	}
	runtime.target = container.Container{ID: "container-id", Name: container.ContainerName(spec.RunID), RunID: spec.RunID}
	return runtime.target, nil
}

func (runtime *pipelineTestRuntime) Remove(ctx context.Context, _ container.Container) error {
	runtime.removeCalls++
	if err := ctx.Err(); err != nil {
		runtime.removeContextErr = err
	}
	if runtime.events != nil {
		*runtime.events = append(*runtime.events, "remove")
	}
	return runtime.removeErr
}

func (runtime *pipelineTestRuntime) Exec(context.Context, container.Container, []string) ([]byte, error) {
	return nil, nil
}

func (runtime *pipelineTestRuntime) ExecWithEnvironment(context.Context, container.Container, []string, []container.EnvironmentVariable) ([]byte, error) {
	return nil, nil
}

func (runtime *pipelineTestRuntime) Transfer(_ context.Context, _ container.Container, _ string, source io.ReadCloser, _ int64) error {
	defer source.Close()
	artifact, err := io.ReadAll(source)
	runtime.transferredArtifact = artifact
	return err
}

type pipelineTestStore struct {
	objects   []storage.Object
	bodies    map[string][]byte
	openedKey string
}

func (store *pipelineTestStore) List(_ context.Context, options storage.ListOptions) (storage.ListPage, error) {
	objects := make([]storage.Object, 0, len(store.objects))
	for _, object := range store.objects {
		if strings.HasPrefix(object.Key, options.Prefix) {
			objects = append(objects, object)
		}
	}
	return storage.ListPage{Objects: objects}, nil
}

func (store *pipelineTestStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	store.openedKey = key
	body, ok := store.bodies[key]
	if !ok {
		return nil, storage.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (store *pipelineTestStore) Stat(_ context.Context, key string) (storage.Object, error) {
	for _, object := range store.objects {
		if object.Key == key {
			return object, nil
		}
	}
	return storage.Object{}, storage.ErrObjectNotFound
}

func makePipelineTar(t *testing.T, name, body string) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func equalStrings(left, right []string) bool {
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
