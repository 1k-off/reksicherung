package restic

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/1k-off/reksicherung/internal/reksicherung/config"
	"github.com/1k-off/reksicherung/internal/reksicherung/evidence"
	sqlserverrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/sqlserver"
	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

const (
	pipelineCleanupTimeout = 10 * time.Second
	defaultContainerDisk   = int64(10 << 30)
	defaultMaxBackupBytes  = int64(5 << 30)
	maxResticBackupBytes   = int64(1 << 40)
	defaultS3Endpoint      = "s3.amazonaws.com"
)

var (
	ErrInvalidPipelineConfig = errors.New("invalid Restic restore pipeline configuration")
	ErrInvalidRestoreRequest = errors.New("invalid Restic restore request")
	ErrBackupTooLarge        = errors.New("Restic backup file exceeds the configured byte limit")
	ErrEvidenceAppend        = errors.New("record Restic restore evidence failed")
)

// DatabaseOperations exposes only the SQL operations needed for a native .bak
// restore. Implementations remain bound to the run-scoped database.
type DatabaseOperations interface {
	WaitReady(context.Context) error
	RestoreBackup(context.Context, io.ReadCloser, string) error
	CheckIntegrity(context.Context) (sqlserverrestore.IntegrityResult, error)
}

type Dependencies struct {
	Runtime               container.Runtime
	NewResticRunner       func(container.Container) (Runner, error)
	NewDatabaseOperations func(container.Container, string, string) (DatabaseOperations, error)
	EgressNetwork         string
	DiskBytes             int64
	MaxBackupBytes        int64
}

// ResolvedSecrets contains values that the caller has already resolved from
// the catalog references. The caller is the secret-resolution boundary; this
// pipeline never contacts OpenBao or resolves references itself.
type ResolvedSecrets struct {
	RepositoryPassword string
	S3AccessKey        string
	S3SecretKey        string
	SQLServerPassword  string
}

type Request struct {
	RunID        string
	Source       config.BackupSource
	Secrets      ResolvedSecrets
	EvidenceSink EvidenceSink
}

// EvidenceSink records one run's structured events. The caller owns its
// lifecycle; Restore never closes it.
type EvidenceSink interface {
	Append(context.Context, evidence.Event) error
}

type Result struct {
	SnapshotID                string
	Database                  string
	IntegrityCommandCompleted bool
	IntegrityAssessment       string
	Container                 container.Container
	Retained                  bool
}

type Pipeline struct {
	dependencies Dependencies
}

func NewPipeline(dependencies Dependencies) (*Pipeline, error) {
	if dependencies.Runtime == nil || dependencies.NewResticRunner == nil ||
		dependencies.NewDatabaseOperations == nil {
		return nil, ErrInvalidPipelineConfig
	}
	if dependencies.EgressNetwork == "" || !container.ValidateNetworkName(dependencies.EgressNetwork) {
		return nil, ErrInvalidPipelineConfig
	}
	if dependencies.DiskBytes == 0 {
		dependencies.DiskBytes = defaultContainerDisk
	}
	if dependencies.DiskBytes < container.MinContainerDiskBytes ||
		dependencies.DiskBytes > container.MaxContainerDiskBytes {
		return nil, ErrInvalidPipelineConfig
	}
	if dependencies.MaxBackupBytes == 0 {
		dependencies.MaxBackupBytes = defaultMaxBackupBytes
	}
	if dependencies.MaxBackupBytes < 1 || dependencies.MaxBackupBytes > maxResticBackupBytes ||
		dependencies.MaxBackupBytes >= dependencies.DiskBytes {
		return nil, ErrInvalidPipelineConfig
	}
	return &Pipeline{dependencies: dependencies}, nil
}

// Restore selects one snapshot, streams only Source.BackupPath from that
// snapshot into the SQL Server adapter's fixed backup path, restores the
// run-scoped database, and issues DBCC CHECKDB. Command completion is recorded
// as inconclusive: this method does not parse SQL messages and never claims
// that integrity passed.
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
	repository, err := RepositoryAddress(request.Source.Storage)
	if err != nil {
		return Result{}, ErrInvalidRestoreRequest
	}
	environment := resticEnvironment(request.Source.Storage, request.Secrets)
	spec := container.Spec{
		RunID:           request.RunID,
		Image:           request.Source.Restore.Image,
		Network:         pipeline.dependencies.EgressNetwork,
		CPUs:            float64(request.Source.Restore.CPUMilli) / 1000,
		MemoryBytes:     int64(request.Source.Restore.MemoryMiB) * 1024 * 1024,
		DiskBytes:       pipeline.dependencies.DiskBytes,
		Timeout:         timeout,
		RetainForReview: request.Source.Cleanup == "hold_for_review",
		Environment: []container.EnvironmentVariable{
			{Name: "ACCEPT_EULA", Value: "Y"},
			{Name: "MSSQL_SA_PASSWORD", Value: request.Secrets.SQLServerPassword},
		},
	}
	if err := container.ValidateSpec(spec); err != nil {
		return Result{}, ErrInvalidRestoreRequest
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	containerStarted := time.Now()
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID,
		"container_creation", evidence.StatusStarted, map[string]string{
			"component":    "sqlserver",
			"cleanup_mode": request.Source.Cleanup,
		}, "", 0); err != nil {
		return Result{}, err
	}
	target, err := pipeline.dependencies.Runtime.Start(operationCtx, spec)
	if err != nil {
		failure := sanitizedContextOr(operationCtx, "start SQL Server restore container failed")
		return Result{}, finishFailedStep(ctx, request, "container_creation", containerStarted, failure, map[string]string{
			"component":    "sqlserver",
			"cleanup_mode": request.Source.Cleanup,
		})
	}
	result = Result{
		Database:  database,
		Container: target,
		Retained:  spec.RetainForReview,
	}
	defer func() {
		if spec.RetainForReview {
			if appendErr := appendEvidenceWithCleanupContext(ctx, request.EvidenceSink, request.RunID,
				"cleanup", evidence.StatusSkipped, map[string]string{
					"component":    "sqlserver",
					"cleanup_mode": "hold_for_review",
					"outcome":      "retained",
				}, "", 0); appendErr != nil {
				returnErr = errors.Join(returnErr, appendErr)
			}
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), pipelineCleanupTimeout)
		defer cleanupCancel()
		cleanupStarted := time.Now()
		startAppendErr := appendEvidence(cleanupCtx, request.EvidenceSink, request.RunID,
			"cleanup", evidence.StatusStarted, map[string]string{
				"component":    "sqlserver",
				"cleanup_mode": "delete",
			}, "", 0)
		if cleanupErr := pipeline.dependencies.Runtime.Remove(cleanupCtx, target); cleanupErr != nil {
			returnErr = errors.Join(returnErr, errors.New("remove SQL Server restore container failed"))
			if appendErr := appendEvidence(cleanupCtx, request.EvidenceSink, request.RunID,
				"cleanup", evidence.StatusFailed, map[string]string{
					"component":    "sqlserver",
					"cleanup_mode": "delete",
					"outcome":      "remove_failed",
				}, "container cleanup failed", time.Since(cleanupStarted)); appendErr != nil {
				returnErr = errors.Join(returnErr, appendErr)
			}
		} else if appendErr := appendEvidence(cleanupCtx, request.EvidenceSink, request.RunID,
			"cleanup", evidence.StatusSucceeded, map[string]string{
				"component":    "sqlserver",
				"cleanup_mode": "delete",
				"outcome":      "removed",
			}, "", time.Since(cleanupStarted)); appendErr != nil {
			returnErr = errors.Join(returnErr, appendErr)
		}
		if startAppendErr != nil {
			returnErr = errors.Join(returnErr, startAppendErr)
		}
	}()
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID,
		"container_creation", evidence.StatusSucceeded, map[string]string{
			"component":    "sqlserver",
			"cleanup_mode": request.Source.Cleanup,
			"result":       "created",
		}, "", time.Since(containerStarted)); err != nil {
		return result, err
	}

	readinessStarted := time.Now()
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID,
		"sql_readiness", evidence.StatusStarted, map[string]string{"component": "sqlserver"}, "", 0); err != nil {
		return result, err
	}
	operations, err := pipeline.dependencies.NewDatabaseOperations(target, database, request.Secrets.SQLServerPassword)
	if err != nil || operations == nil {
		failure := sanitizedContextOr(operationCtx, "configure SQL Server restore operations failed")
		return result, finishFailedStep(ctx, request, "sql_readiness", readinessStarted, failure, map[string]string{"component": "sqlserver"})
	}
	if err := operations.WaitReady(operationCtx); err != nil {
		failure := sanitizedContextOr(operationCtx, "wait for SQL Server readiness failed")
		return result, finishFailedStep(ctx, request, "sql_readiness", readinessStarted, failure, map[string]string{"component": "sqlserver"})
	}
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID,
		"sql_readiness", evidence.StatusSucceeded, map[string]string{
			"component": "sqlserver",
			"database":  database,
		}, "", time.Since(readinessStarted)); err != nil {
		return result, err
	}
	selectionStarted := time.Now()
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID,
		"restic_selection", evidence.StatusStarted, map[string]string{"provider": "restic"}, "", 0); err != nil {
		return result, err
	}
	runner, err := pipeline.dependencies.NewResticRunner(target)
	if err != nil || runner == nil {
		failure := sanitizedContextOr(operationCtx, "configure Restic restore failed")
		return result, finishFailedStep(ctx, request, "restic_selection", selectionStarted, failure, map[string]string{"provider": "restic"})
	}
	resticClient, err := New(Config{
		Repository:  repository,
		TargetRoot:  "/restore",
		TargetPath:  "/restore/file",
		Timeout:     timeout,
		Environment: environment,
	}, runner)
	if err != nil {
		return result, finishFailedStep(ctx, request, "restic_selection", selectionStarted, ErrInvalidRestoreRequest, map[string]string{"provider": "restic"})
	}
	selection := Selection{Policy: request.Source.Selection.Policy, ID: request.Source.Selection.ID}
	selected, archive, err := resticClient.OpenBackupFile(operationCtx, selection, request.Source.BackupPath)
	if err != nil {
		failure := sanitizedContextOr(operationCtx, "select or stream Restic backup file failed")
		return result, finishFailedStep(ctx, request, "restic_selection", selectionStarted, failure, map[string]string{"provider": "restic"})
	}
	result.SnapshotID = selected.ID
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID,
		"restic_selection", evidence.StatusSucceeded, map[string]string{
			"provider": "restic",
			"result":   request.Source.Selection.Policy,
		}, "", time.Since(selectionStarted)); err != nil {
		_ = archive.Close()
		return result, err
	}
	limitedArchive := &limitedArchiveReader{source: archive, remaining: pipeline.dependencies.MaxBackupBytes}
	restoreStarted := time.Now()
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID,
		"native_backup_restore", evidence.StatusStarted, map[string]string{
			"component": "sqlserver",
			"database":  database,
		}, "", 0); err != nil {
		_ = limitedArchive.Close()
		return result, err
	}
	restoreErr := operations.RestoreBackup(operationCtx, limitedArchive, request.Source.Database.Name)
	closeErr := limitedArchive.Close()
	if limitedArchive.exceeded {
		return result, finishFailedStep(ctx, request, "native_backup_restore", restoreStarted, ErrBackupTooLarge,
			map[string]string{"component": "sqlserver", "database": database})
	}
	if restoreErr != nil {
		failure := sanitizedContextOr(operationCtx, "restore native SQL Server backup failed")
		return result, finishFailedStep(ctx, request, "native_backup_restore", restoreStarted, failure,
			map[string]string{"component": "sqlserver", "database": database})
	}
	if closeErr != nil {
		failure := sanitizedContextOr(operationCtx, "close Restic backup stream failed")
		return result, finishFailedStep(ctx, request, "native_backup_restore", restoreStarted, failure,
			map[string]string{"component": "sqlserver", "database": database})
	}
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID,
		"native_backup_restore", evidence.StatusSucceeded, map[string]string{
			"component": "sqlserver",
			"database":  database,
			"result":    "restored",
		}, "", time.Since(restoreStarted)); err != nil {
		return result, err
	}
	integrityStarted := time.Now()
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID,
		"dbcc_checkdb", evidence.StatusStarted, map[string]string{
			"check_name": "DBCC_CHECKDB",
			"database":   database,
		}, "", 0); err != nil {
		return result, err
	}
	integrity, err := operations.CheckIntegrity(operationCtx)
	if err != nil {
		failure := sanitizedContextOr(operationCtx, "DBCC CHECKDB command failed")
		return result, finishFailedStep(ctx, request, "dbcc_checkdb", integrityStarted, failure,
			map[string]string{"check_name": "DBCC_CHECKDB", "database": database})
	}
	if !integrity.CommandCompleted || integrity.Database != database {
		return result, finishFailedStep(ctx, request, "dbcc_checkdb", integrityStarted,
			errors.New("DBCC CHECKDB did not complete for the run database"),
			map[string]string{"check_name": "DBCC_CHECKDB", "database": database})
	}
	if err := appendEvidence(operationCtx, request.EvidenceSink, request.RunID,
		"dbcc_checkdb", evidence.StatusSucceeded, map[string]string{
			"check_name": "DBCC_CHECKDB",
			"database":   database,
			"outcome":    "command_completed_inconclusive",
		}, "", time.Since(integrityStarted)); err != nil {
		return result, err
	}
	result.IntegrityCommandCompleted = integrity.CommandCompleted
	result.IntegrityAssessment = "inconclusive"
	return result, nil
}

// RepositoryAddress creates Restic's documented S3 repository form from only
// the catalog coordinates. Credentials are never embedded in the address.
func RepositoryAddress(storage config.S3Storage) (string, error) {
	if storage.Type != "s3" || !validBucketName(storage.Bucket) || !validObjectPrefix(storage.Prefix) {
		return "", ErrInvalidRestoreRequest
	}
	location := storage.Bucket
	if storage.Prefix != "" {
		location += "/" + escapeObjectPrefix(storage.Prefix)
	}
	if storage.Endpoint == "" {
		return "s3:https://" + defaultS3Endpoint + "/" + location, nil
	}
	endpoint, err := url.Parse(storage.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" ||
		endpoint.Fragment != "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") ||
		(endpoint.Path != "" && endpoint.Path != "/") ||
		(endpoint.Scheme == "http" && !isLoopbackEndpoint(endpoint.Hostname())) {
		return "", ErrInvalidRestoreRequest
	}
	return "s3:" + endpoint.Scheme + "://" + endpoint.Host + "/" + location, nil
}

func validateRequest(request Request) error {
	if request.RunID == "" || !validBackupPath(request.Source.BackupPath) ||
		request.Secrets.RepositoryPassword == "" ||
		request.Secrets.S3AccessKey == "" || request.Secrets.S3SecretKey == "" ||
		request.Secrets.SQLServerPassword == "" ||
		!validSecretValue(request.Secrets.RepositoryPassword) ||
		!validSecretValue(request.Secrets.S3AccessKey) ||
		!validSecretValue(request.Secrets.S3SecretKey) ||
		!validSecretValue(request.Secrets.SQLServerPassword) {
		return ErrInvalidRestoreRequest
	}
	catalog := config.Catalog{
		APIVersion: config.APIVersion,
		Kind:       config.Kind,
		Backups:    []config.BackupSource{request.Source},
	}
	if request.Source.Provider != "restic" || catalog.Validate() != nil {
		return ErrInvalidRestoreRequest
	}
	if !validSelection(Selection{
		Policy: request.Source.Selection.Policy,
		ID:     request.Source.Selection.ID,
	}) {
		return ErrInvalidRestoreRequest
	}
	return nil
}

func resticEnvironment(storage config.S3Storage, resolved ResolvedSecrets) []EnvironmentVariable {
	return []EnvironmentVariable{
		{Name: "RESTIC_PASSWORD", Value: resolved.RepositoryPassword},
		{Name: "AWS_ACCESS_KEY_ID", Value: resolved.S3AccessKey},
		{Name: "AWS_SECRET_ACCESS_KEY", Value: resolved.S3SecretKey},
		{Name: "AWS_DEFAULT_REGION", Value: storage.Region},
	}
}

func validSecretValue(value string) bool {
	if len(value) > 4096 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validBucketName(bucket string) bool {
	if len(bucket) < 3 || len(bucket) > 63 || bucket[0] == '.' || bucket[0] == '-' ||
		bucket[len(bucket)-1] == '.' || bucket[len(bucket)-1] == '-' ||
		strings.Contains(bucket, "..") || net.ParseIP(bucket) != nil {
		return false
	}
	for _, label := range strings.Split(bucket, ".") {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
				return false
			}
		}
	}
	return true
}

func validObjectPrefix(prefix string) bool {
	if prefix == "" {
		return true
	}
	if strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "\\") || len(prefix) > 1024 {
		return false
	}
	for _, segment := range strings.Split(prefix, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, character := range segment {
			if unicode.IsControl(character) {
				return false
			}
		}
	}
	return true
}

func escapeObjectPrefix(prefix string) string {
	segments := strings.Split(prefix, "/")
	for index := range segments {
		segments[index] = url.PathEscape(segments[index])
	}
	return strings.Join(segments, "/")
}

func isLoopbackEndpoint(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sanitizedContextOr(ctx context.Context, message string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New(message)
}

func finishFailedStep(ctx context.Context, request Request, operation string, started time.Time, operationErr error, metadata map[string]string) error {
	status := evidence.StatusFailed
	eventError := evidenceFailureReason(operation, operationErr)
	if errors.Is(operationErr, context.Canceled) || errors.Is(operationErr, context.DeadlineExceeded) {
		status = evidence.StatusCanceled
		if errors.Is(operationErr, context.DeadlineExceeded) {
			eventError = "restore operation timed out"
		} else {
			eventError = ""
		}
	}
	appendErr := appendEvidenceWithCleanupContext(ctx, request.EvidenceSink, request.RunID,
		operation, status, metadata, eventError, time.Since(started))
	if appendErr != nil {
		return errors.Join(operationErr, appendErr)
	}
	return operationErr
}

func evidenceFailureReason(operation string, err error) string {
	switch {
	case errors.Is(err, ErrBackupTooLarge):
		return "configured restore size limit exceeded"
	case errors.Is(err, context.DeadlineExceeded):
		return "restore operation timed out"
	}
	switch operation {
	case "container_creation":
		return "restore container creation failed"
	case "sql_readiness":
		return "SQL Server readiness check failed"
	case "restic_selection":
		return "Restic snapshot or backup file selection failed"
	case "native_backup_restore":
		return "native SQL Server backup restore failed"
	case "dbcc_checkdb":
		return "DBCC CHECKDB command did not complete successfully"
	default:
		return "restore operation failed"
	}
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

type limitedArchiveReader struct {
	source    io.ReadCloser
	remaining int64
	exceeded  bool
	closed    bool
}

func (reader *limitedArchiveReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if reader.remaining > 0 {
		if int64(len(buffer)) > reader.remaining {
			buffer = buffer[:int(reader.remaining)]
		}
		count, err := reader.source.Read(buffer)
		reader.remaining -= int64(count)
		return count, err
	}
	var probe [1]byte
	count, err := io.ReadFull(reader.source, probe[:])
	if count > 0 {
		reader.exceeded = true
		return 0, ErrBackupTooLarge
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, io.EOF
	}
	return 0, err
}

func (reader *limitedArchiveReader) Close() error {
	if reader.closed {
		return nil
	}
	reader.closed = true
	return reader.source.Close()
}

var _ io.ReadCloser = (*limitedArchiveReader)(nil)
