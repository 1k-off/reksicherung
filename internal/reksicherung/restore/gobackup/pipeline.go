package gobackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/config"
	"github.com/1k-off/reksicherung/internal/reksicherung/evidence"
	backupstore "github.com/1k-off/reksicherung/internal/reksicherung/mcp/gobackup"
	sqlserverrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/sqlserver"
	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
	"github.com/1k-off/reksicherung/internal/reksicherung/storage"
)

const (
	pipelineCleanupTimeout = 10 * time.Second
	defaultContainerDisk   = int64(10 << 30)
)

var (
	// ErrInvalidPipelineConfig indicates that a pipeline dependency or bounded
	// archive/container limit is invalid.
	ErrInvalidPipelineConfig = errors.New("invalid GoBackup restore pipeline configuration")
	// ErrInvalidRestoreRequest indicates that a restore request is incomplete or
	// does not describe a GoBackup source.
	ErrInvalidRestoreRequest = errors.New("invalid GoBackup restore request")
	// ErrEvidenceAppend indicates that a restore lifecycle event could not be
	// recorded. The underlying recorder error is deliberately not exposed.
	ErrEvidenceAppend = errors.New("record GoBackup restore evidence failed")
)

// ArtifactRepository selects and opens GoBackup archives by their object key
// relative to the configured storage prefix.
type ArtifactRepository interface {
	Latest(context.Context) (backupstore.Backup, error)
	Select(context.Context, string) (backupstore.Backup, error)
	Open(context.Context, string) (io.ReadCloser, error)
}

// ImportOperation imports one archive into the run-scoped database. Implementors
// own and close the archive reader, as Importer does.
type ImportOperation interface {
	Import(context.Context, container.Container, io.ReadCloser, Format, ImportOptions) error
}

// DatabaseOperations is restricted to readiness and the fixed integrity check.
type DatabaseOperations interface {
	WaitReady(context.Context) error
	CheckIntegrity(context.Context) (sqlserverrestore.IntegrityResult, error)
}

// Dependencies are deliberately narrow seams for unit testing the orchestration
// without storage, SQL Server, or a container runtime.
type Dependencies struct {
	Runtime               container.Runtime
	NewArtifacts          func(context.Context, config.S3Storage, string, int64) (ArtifactRepository, error)
	NewImporter           func(runtime container.Runtime, limits Limits, timeout time.Duration) (ImportOperation, error)
	NewDatabaseOperations func(container.Container, string, string) (DatabaseOperations, error)
	DiskBytes             int64
	ArchiveLimits         Limits
}

// StoreFactory creates a storage client for the exact S3 coordinates and
// secret references selected by a catalog source. Implementations are
// responsible for resolving the configured references without logging values.
type StoreFactory func(context.Context, config.S3Storage, int64) (storage.Store, error)

// Pipeline composes storage selection, SQL Server lifecycle, import, integrity
// command, and automatic container cleanup.
type Pipeline struct {
	dependencies Dependencies
}

// Result identifies the selected archive and run-scoped database. A true
// IntegrityCommandCompleted means only that DBCC CHECKDB completed without a
// command error; it does not establish that the database has no consistency
// errors. Container is populated for both automatic and retained runs; for
// automatic cleanup it may refer to a container that has already been removed.
type Result struct {
	BackupID                  string
	Database                  string
	IntegrityCommandCompleted bool
	Container                 container.Container
	Retained                  bool
}

// Request supplies the validated source catalog entry and per-run SQL Server
// credential. Password is used only as an environment value and is never
// included in results or returned diagnostics.
type Request struct {
	RunID        string
	Source       config.BackupSource
	Password     string
	EvidenceSink EvidenceSink
}

// EvidenceSink records one run's structured events. The caller owns its
// lifecycle; Restore never closes it.
type EvidenceSink interface {
	Append(context.Context, evidence.Event) error
}

// NewPipeline validates the orchestration dependencies and their hard bounds.
func NewPipeline(dependencies Dependencies) (*Pipeline, error) {
	if dependencies.Runtime == nil || dependencies.NewArtifacts == nil ||
		dependencies.NewImporter == nil || dependencies.NewDatabaseOperations == nil ||
		dependencies.DiskBytes < container.MinContainerDiskBytes ||
		dependencies.DiskBytes > container.MaxContainerDiskBytes ||
		dependencies.ArchiveLimits.MaxCompressedBytes <= 0 ||
		dependencies.ArchiveLimits.MaxUncompressedBytes <= 0 ||
		dependencies.ArchiveLimits.MaxBACPACBytes <= 0 ||
		dependencies.ArchiveLimits.MaxBACPACBytes > maxArtifactBytes {
		return nil, ErrInvalidPipelineConfig
	}
	return &Pipeline{dependencies: dependencies}, nil
}

// NewStoragePipeline constructs the production adapters around per-source
// storage clients and the container runtime. StoreFactory receives the catalog
// S3 coordinates for each restore, so a pipeline cannot accidentally reuse a
// client bound to a different bucket or endpoint. DiskBytes is the bounded
// writable-layer limit; zero selects the 10 GiB default.
func NewStoragePipeline(newStore StoreFactory, runtime container.Runtime, diskBytes int64, limits Limits) (*Pipeline, error) {
	if newStore == nil || runtime == nil {
		return nil, ErrInvalidPipelineConfig
	}
	if diskBytes == 0 {
		diskBytes = defaultContainerDisk
	}
	dependencies := Dependencies{
		Runtime:       runtime,
		DiskBytes:     diskBytes,
		ArchiveLimits: limits,
		NewArtifacts: func(ctx context.Context, sourceStorage config.S3Storage, prefix string, maxObjectBytes int64) (ArtifactRepository, error) {
			store, err := newStore(ctx, sourceStorage, maxObjectBytes)
			if err != nil {
				return nil, err
			}
			if store == nil {
				return nil, ErrInvalidPipelineConfig
			}
			return backupstore.New(store, backupstore.Config{
				Prefix:         prefix,
				MaxObjectBytes: maxObjectBytes,
			})
		},
		NewImporter: func(runtime container.Runtime, limits Limits, timeout time.Duration) (ImportOperation, error) {
			return NewImporter(runtime, limits, timeout)
		},
		NewDatabaseOperations: func(target container.Container, database, password string) (DatabaseOperations, error) {
			return sqlserverrestore.New(runtime, target, database, password)
		},
	}
	return NewPipeline(dependencies)
}

// Restore selects and opens the requested GoBackup artifact, starts an isolated
// SQL Server container, waits for readiness, imports the BACPAC, and runs the
// fixed DBCC CHECKDB operation. Containers are always removed in automatic
// mode, including when any step fails. hold_for_review leaves the container
// untouched on both success and failure.
func (pipeline *Pipeline) Restore(ctx context.Context, request Request) (result Result, returnErr error) {
	if ctx == nil {
		return Result{}, errors.New("context must not be nil")
	}
	if pipeline == nil {
		return Result{}, ErrInvalidPipelineConfig
	}
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	if request.EvidenceSink == nil {
		return Result{}, ErrInvalidRestoreRequest
	}
	database, err := sqlserverrestore.DatabaseNameForRun(request.RunID)
	if err != nil {
		return Result{}, ErrInvalidRestoreRequest
	}
	timeout, err := time.ParseDuration(request.Source.Restore.Timeout)
	if err != nil {
		return Result{}, ErrInvalidRestoreRequest
	}
	spec := container.Spec{
		RunID:           request.RunID,
		Image:           request.Source.Restore.Image,
		CPUs:            float64(request.Source.Restore.CPUMilli) / 1000,
		MemoryBytes:     int64(request.Source.Restore.MemoryMiB) * 1024 * 1024,
		DiskBytes:       pipeline.dependencies.DiskBytes,
		Timeout:         timeout,
		RetainForReview: request.Source.Cleanup == "hold_for_review",
		Environment: []container.EnvironmentVariable{
			{Name: "ACCEPT_EULA", Value: "Y"},
			{Name: "MSSQL_SA_PASSWORD", Value: request.Password},
		},
	}
	if err := container.ValidateSpec(spec); err != nil {
		return Result{}, ErrInvalidRestoreRequest
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	selectionStarted := time.Now()
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID, "gobackup_selection", evidence.StatusStarted, nil, "", 0); err != nil {
		return Result{}, err
	}
	prefix := request.Source.Storage.Prefix + "/"
	artifacts, err := pipeline.dependencies.NewArtifacts(
		operationCtx,
		request.Source.Storage,
		prefix,
		pipeline.dependencies.ArchiveLimits.MaxCompressedBytes,
	)
	if err != nil || artifacts == nil {
		if operationCtx.Err() != nil {
			return Result{}, finishFailedStep(ctx, request, "gobackup_selection", selectionStarted, operationCtx.Err(), nil)
		}
		return Result{}, finishFailedStep(ctx, request, "gobackup_selection", selectionStarted, errors.New("configure GoBackup artifact storage failed"), nil)
	}

	var selected backupstore.Backup
	switch request.Source.Selection.Policy {
	case "latest":
		selected, err = artifacts.Latest(operationCtx)
	case "id":
		selected, err = artifacts.Select(operationCtx, request.Source.Selection.ID)
	default:
		return Result{}, ErrInvalidRestoreRequest
	}
	if err != nil {
		if operationCtx.Err() != nil {
			return Result{}, finishFailedStep(ctx, request, "gobackup_selection", selectionStarted, operationCtx.Err(), nil)
		}
		return Result{}, finishFailedStep(ctx, request, "gobackup_selection", selectionStarted, errors.New("select GoBackup archive failed"), nil)
	}
	if selected.Size < 0 || selected.Size > pipeline.dependencies.ArchiveLimits.MaxCompressedBytes {
		return Result{}, finishFailedStep(ctx, request, "gobackup_selection", selectionStarted, ErrLimitExceeded, nil)
	}
	format, err := archiveFormat(selected.ID)
	if err != nil {
		return Result{}, finishFailedStep(ctx, request, "gobackup_selection", selectionStarted, ErrUnsupportedFormat, nil)
	}
	if err := appendEvidence(ctx, request.EvidenceSink, request.RunID, "gobackup_selection", evidence.StatusSucceeded, map[string]string{
		"provider":  "gobackup",
		"result":    request.Source.Selection.Policy,
		"source_id": request.Source.ID,
		"backup_id": artifactEvidenceID(selected.ID),
	}, "", time.Since(selectionStarted)); err != nil {
		return Result{}, err
	}
	importer, err := pipeline.dependencies.NewImporter(pipeline.dependencies.Runtime, pipeline.dependencies.ArchiveLimits, timeout)
	if err != nil || importer == nil {
		if operationCtx.Err() != nil {
			return Result{}, operationCtx.Err()
		}
		return Result{}, errors.New("configure GoBackup importer failed")
	}

	containerStarted := time.Now()
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID, "container_creation", evidence.StatusStarted, map[string]string{"component": "sqlserver"}, "", 0); err != nil {
		return Result{}, err
	}
	target, err := pipeline.dependencies.Runtime.Start(operationCtx, spec)
	if err != nil {
		if operationCtx.Err() != nil {
			return Result{}, finishFailedStep(ctx, request, "container_creation", containerStarted, operationCtx.Err(), map[string]string{"component": "sqlserver"})
		}
		return Result{}, finishFailedStep(ctx, request, "container_creation", containerStarted, errors.New("start SQL Server restore container failed"), map[string]string{"component": "sqlserver"})
	}
	result = Result{
		BackupID:  selected.ID,
		Database:  database,
		Container: target,
		Retained:  spec.RetainForReview,
	}
	defer func() {
		if spec.RetainForReview {
			if appendErr := appendEvidenceWithCleanupContext(ctx, request.EvidenceSink, request.RunID, "cleanup", evidence.StatusSkipped, map[string]string{
				"outcome":     "retained",
				"resource_id": target.ID,
			}, "", 0); appendErr != nil {
				returnErr = errors.Join(returnErr, appendErr)
			}
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), pipelineCleanupTimeout)
		defer cleanupCancel()
		cleanupStarted := time.Now()
		startAppendErr := appendEvidence(cleanupCtx, request.EvidenceSink, request.RunID, "cleanup", evidence.StatusStarted, map[string]string{
			"component":   "sqlserver",
			"resource_id": target.ID,
		}, "", 0)
		if cleanupErr := pipeline.dependencies.Runtime.Remove(cleanupCtx, target); cleanupErr != nil {
			returnErr = errors.Join(returnErr, errors.New("remove SQL Server restore container failed"))
			if appendErr := appendEvidence(cleanupCtx, request.EvidenceSink, request.RunID, "cleanup", evidence.StatusFailed, map[string]string{
				"component":   "sqlserver",
				"outcome":     "remove_failed",
				"resource_id": target.ID,
			}, "container cleanup failed", time.Since(cleanupStarted)); appendErr != nil {
				returnErr = errors.Join(returnErr, appendErr)
			}
		} else if appendErr := appendEvidence(cleanupCtx, request.EvidenceSink, request.RunID, "cleanup", evidence.StatusSucceeded, map[string]string{
			"component":   "sqlserver",
			"outcome":     "removed",
			"resource_id": target.ID,
		}, "", time.Since(cleanupStarted)); appendErr != nil {
			returnErr = errors.Join(returnErr, appendErr)
		}
		if startAppendErr != nil {
			returnErr = errors.Join(returnErr, startAppendErr)
		}
	}()
	if err := appendEvidence(ctx, request.EvidenceSink, request.RunID, "container_creation", evidence.StatusSucceeded, map[string]string{
		"component":   "sqlserver",
		"result":      "created",
		"resource_id": target.ID,
	}, "", time.Since(containerStarted)); err != nil {
		return result, err
	}

	operations, err := pipeline.dependencies.NewDatabaseOperations(target, database, request.Password)
	if err != nil || operations == nil {
		if operationCtx.Err() != nil {
			return result, operationCtx.Err()
		}
		return result, errors.New("configure SQL Server restore operations failed")
	}
	readinessStarted := time.Now()
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID, "sql_readiness", evidence.StatusStarted, map[string]string{"component": "sqlserver"}, "", 0); err != nil {
		return result, err
	}
	if err := operations.WaitReady(operationCtx); err != nil {
		if operationCtx.Err() != nil {
			return result, finishFailedStep(ctx, request, "sql_readiness", readinessStarted, operationCtx.Err(), map[string]string{"component": "sqlserver"})
		}
		return result, finishFailedStep(ctx, request, "sql_readiness", readinessStarted, errors.New("wait for SQL Server readiness failed"), map[string]string{"component": "sqlserver"})
	}
	if err := appendEvidence(ctx, request.EvidenceSink, request.RunID, "sql_readiness", evidence.StatusSucceeded, map[string]string{"component": "sqlserver", "result": "ready"}, "", time.Since(readinessStarted)); err != nil {
		return result, err
	}

	archiveOpenStarted := time.Now()
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID, "archive_open", evidence.StatusStarted, map[string]string{"provider": "gobackup"}, "", 0); err != nil {
		return result, err
	}
	archive, err := artifacts.Open(operationCtx, selected.ID)
	if err != nil || archive == nil {
		if operationCtx.Err() != nil {
			return result, finishFailedStep(ctx, request, "archive_open", archiveOpenStarted, operationCtx.Err(), map[string]string{"provider": "gobackup"})
		}
		return result, finishFailedStep(ctx, request, "archive_open", archiveOpenStarted, errors.New("open GoBackup archive failed"), map[string]string{"provider": "gobackup"})
	}
	if err := appendEvidence(ctx, request.EvidenceSink, request.RunID, "archive_open", evidence.StatusSucceeded, map[string]string{"provider": "gobackup", "result": "opened"}, "", time.Since(archiveOpenStarted)); err != nil {
		if closeErr := archive.Close(); closeErr != nil {
			return result, errors.Join(err, errors.New("close GoBackup archive failed"))
		}
		return result, err
	}
	importStarted := time.Now()
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID, "archive_import", evidence.StatusStarted, map[string]string{"provider": "gobackup"}, "", 0); err != nil {
		if closeErr := archive.Close(); closeErr != nil {
			return result, errors.Join(err, errors.New("close GoBackup archive failed"))
		}
		return result, err
	}
	importErr := importer.Import(operationCtx, target, archive, format, ImportOptions{
		Database: database,
		Username: "sa",
		Password: request.Password,
	})
	if importErr != nil {
		if operationCtx.Err() != nil {
			return result, finishFailedStep(ctx, request, "archive_import", importStarted, operationCtx.Err(), map[string]string{"provider": "gobackup"})
		}
		return result, finishFailedStep(ctx, request, "archive_import", importStarted, errors.New("import GoBackup BACPAC failed"), map[string]string{"provider": "gobackup"})
	}
	if err := appendEvidence(ctx, request.EvidenceSink, request.RunID, "archive_import", evidence.StatusSucceeded, map[string]string{"provider": "gobackup", "result": "imported"}, "", time.Since(importStarted)); err != nil {
		return result, err
	}

	dbccStarted := time.Now()
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID, "dbcc_checkdb", evidence.StatusStarted, map[string]string{"check_name": "DBCC_CHECKDB"}, "", 0); err != nil {
		return result, err
	}
	integrityResult, err := operations.CheckIntegrity(operationCtx)
	if err != nil {
		if operationCtx.Err() != nil {
			return result, finishFailedStep(ctx, request, "dbcc_checkdb", dbccStarted, operationCtx.Err(), map[string]string{"check_name": "DBCC_CHECKDB"})
		}
		return result, finishFailedStep(ctx, request, "dbcc_checkdb", dbccStarted, errors.New("DBCC CHECKDB command failed"), map[string]string{"check_name": "DBCC_CHECKDB"})
	}
	if !integrityResult.CommandCompleted {
		return result, finishFailedStep(ctx, request, "dbcc_checkdb", dbccStarted,
			errors.New("DBCC CHECKDB command did not complete"), map[string]string{"check_name": "DBCC_CHECKDB"})
	}
	if integrityResult.Database != database {
		return result, finishFailedStep(ctx, request, "dbcc_checkdb", dbccStarted,
			errors.New("DBCC CHECKDB reported an unexpected target"), map[string]string{"check_name": "DBCC_CHECKDB"})
	}
	if err := appendEvidence(ctx, request.EvidenceSink, request.RunID, "dbcc_checkdb", evidence.StatusSucceeded, map[string]string{
		"check_name": "DBCC_CHECKDB",
		"outcome":    "command_completed",
	}, "", time.Since(dbccStarted)); err != nil {
		return result, err
	}
	result.IntegrityCommandCompleted = true
	return result, nil
}

func finishFailedStep(ctx context.Context, request Request, operation string, started time.Time, operationErr error, metadata map[string]string) error {
	status := evidence.StatusFailed
	eventError := "restore operation failed"
	if errors.Is(operationErr, context.Canceled) || errors.Is(operationErr, context.DeadlineExceeded) {
		status = evidence.StatusCanceled
		eventError = ""
	}
	appendErr := appendEvidenceWithCleanupContext(ctx, request.EvidenceSink, request.RunID, operation, status, metadata, eventError, time.Since(started))
	if appendErr != nil {
		return errors.Join(operationErr, appendErr)
	}
	return operationErr
}

func appendEvidenceWithCleanupContext(ctx context.Context, sink EvidenceSink, runID, operation string, status evidence.Status, metadata map[string]string, eventError string, duration time.Duration) error {
	appendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pipelineCleanupTimeout)
	defer cancel()
	return appendEvidence(appendCtx, sink, runID, operation, status, metadata, eventError, duration)
}

func appendEvidence(ctx context.Context, sink EvidenceSink, runID, operation string, status evidence.Status, metadata map[string]string, eventError string, duration time.Duration) error {
	if metadata != nil {
		copied := make(map[string]string, len(metadata)+1)
		for key, value := range metadata {
			copied[key] = value
		}
		metadata = copied
	}
	if duration > 0 {
		if metadata == nil {
			metadata = make(map[string]string, 1)
		}
		metadata["duration_ms"] = strconv.FormatInt(duration.Milliseconds(), 10)
	}
	event := evidence.Event{
		RunID:     runID,
		Timestamp: time.Now().UTC(),
		Operation: operation,
		Status:    status,
		Metadata:  metadata,
		Error:     eventError,
	}
	if err := event.Validate(); err != nil {
		return ErrEvidenceAppend
	}
	if err := sink.Append(ctx, event); err != nil {
		return ErrEvidenceAppend
	}
	return nil
}

func artifactEvidenceID(objectID string) string {
	sum := sha256.Sum256([]byte(objectID))
	return hex.EncodeToString(sum[:])
}

func validateRequest(request Request) error {
	if request.RunID == "" || !validIdentifier(request.Source.ID) || request.Source.Provider != "gobackup" ||
		request.Source.Storage.Type != "s3" || request.Source.Storage.Prefix == "" ||
		request.Source.Storage.Prefix[0] == '/' || strings.Contains(request.Source.Storage.Prefix, `\`) ||
		request.Password == "" || len(request.Password) > 4096 ||
		strings.ContainsAny(request.Password, "\x00\r\n") {
		return ErrInvalidRestoreRequest
	}
	switch request.Source.Cleanup {
	case "delete", "hold_for_review":
	default:
		return ErrInvalidRestoreRequest
	}
	switch request.Source.Selection.Policy {
	case "latest":
		if request.Source.Selection.ID != "" {
			return ErrInvalidRestoreRequest
		}
	case "id":
		if request.Source.Selection.ID == "" {
			return ErrInvalidRestoreRequest
		}
	default:
		return ErrInvalidRestoreRequest
	}
	return nil
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		alphaNumeric := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
		if index == 0 && !alphaNumeric {
			return false
		}
		if !alphaNumeric && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func archiveFormat(id string) (Format, error) {
	lowerID := strings.ToLower(id)
	switch {
	case strings.HasSuffix(lowerID, ".tar.gz"), strings.HasSuffix(lowerID, ".tgz"):
		return FormatTarGzip, nil
	case strings.HasSuffix(lowerID, ".tar"):
		return FormatTar, nil
	default:
		return "", ErrUnsupportedFormat
	}
}
