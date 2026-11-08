package gobackup

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

const (
	artifactName        = "reksicherung-import.bacpac"
	artifactPath        = "/var/opt/mssql/backup/" + artifactName
	maxArtifactBytes    = int64(1 << 40)
	maxSqlPackageOutput = 4 << 20
	cleanupTimeout      = 10 * time.Second
)

// Runtime is the small part of the container runtime needed for a GoBackup
// import. Implementations must consume or close the transfer reader and honor
// contexts for each operation.
type Runtime interface {
	Transfer(context.Context, container.Container, string, io.ReadCloser, int64) error
	Exec(context.Context, container.Container, []string) ([]byte, error)
	ExecWithEnvironment(context.Context, container.Container, []string, []container.EnvironmentVariable) ([]byte, error)
}

// Importer imports one BACPAC archive into a SQL Server container. The target
// container must be a disposable, isolated environment owned by the current run.
type Importer struct {
	runtime Runtime
	limits  Limits
	timeout time.Duration
}

// ImportOptions contains the per-run SQL Server target credentials. The server
// itself is always the local SQL Server in the target container.
type ImportOptions struct {
	Database string
	Username string
	Password string
}

var sqlIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

const sqlPackageImportScript = `exec /usr/bin/timeout --signal=TERM --kill-after=10s "${REKSICHERUNG_SQLPACKAGE_TIMEOUT}s" /opt/sqlpackage/sqlpackage \
  /Action:Import \
  /SourceFile:/var/opt/mssql/backup/reksicherung-import.bacpac \
  /TargetServerName:localhost \
  /TargetDatabaseName:"$REKSICHERUNG_DB_NAME" \
  /TargetUser:"$REKSICHERUNG_DB_USER" \
  /TargetPassword:"$REKSICHERUNG_DB_PASSWORD" \
  /TargetEncryptConnection:True \
  /TargetTrustServerCertificate:True`

// NewImporter constructs an importer with explicit archive limits and a
// deadline for transfer and SqlPackage execution. Timeout must be positive and
// no greater than 24 hours.
func NewImporter(runtime Runtime, limits Limits, timeout time.Duration) (*Importer, error) {
	if runtime == nil {
		return nil, errors.New("GoBackup restore runtime must not be nil")
	}
	if limits.MaxCompressedBytes <= 0 || limits.MaxUncompressedBytes <= 0 || limits.MaxBACPACBytes <= 0 {
		return nil, errors.New("all GoBackup archive limits must be positive")
	}
	if limits.MaxBACPACBytes > maxArtifactBytes {
		return nil, errors.New("maximum BACPAC size exceeds container runtime transfer limit")
	}
	if timeout <= 0 || timeout > 24*time.Hour {
		return nil, errors.New("GoBackup import timeout must be positive and no greater than 24 hours")
	}
	return &Importer{runtime: runtime, limits: limits, timeout: timeout}, nil
}

// Import extracts the sole BACPAC member into a runtime-managed artifact path,
// then runs a fixed SqlPackage import command in the target container. The
// importer owns and closes archive. Its Close method must unblock a pending
// Read so cancellation and the import deadline can stop archive streaming.
func (importer *Importer) Import(ctx context.Context, target container.Container, archive io.ReadCloser, format Format, options ImportOptions) (returnErr error) {
	if archive == nil {
		return errors.New("GoBackup archive reader must not be nil")
	}
	if ctx == nil {
		_ = archive.Close()
		return errors.New("context must not be nil")
	}
	if err := validateImportOptions(options); err != nil {
		_ = archive.Close()
		return err
	}

	operationCtx, cancel := context.WithTimeout(ctx, importer.timeout)
	defer cancel()

	artifactMayExist := false
	defer func() {
		if !artifactMayExist {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cleanupCancel()
		_, err := importer.runtime.Exec(cleanupCtx, target, []string{"rm", "-f", "--", artifactPath})
		if err != nil {
			returnErr = errors.Join(returnErr, errors.New("remove BACPAC artifact from restore container failed"))
		}
	}()

	var closeArchiveOnce sync.Once
	closeArchive := func() {
		closeArchiveOnce.Do(func() { _ = archive.Close() })
	}
	reader, writer := io.Pipe()
	extractDone := make(chan error, 1)
	go func() {
		err := ExtractBACPAC(operationCtx, archive, writer, format, importer.limits)
		_ = writer.CloseWithError(err)
		closeArchive()
		extractDone <- err
	}()

	// An archive reader may block independently of context cancellation. Closing
	// it is the documented mechanism for interrupting that read.
	stopArchiveCloser := make(chan struct{})
	archiveCloseDone := make(chan struct{})
	go func() {
		defer close(archiveCloseDone)
		select {
		case <-operationCtx.Done():
			closeArchive()
		case <-stopArchiveCloser:
		}
	}()

	artifactMayExist = true
	transferErr := importer.runtime.Transfer(operationCtx, target, artifactName, reader, importer.limits.MaxBACPACBytes)
	if transferErr != nil {
		_ = reader.Close()
		cancel()
		closeArchive()
	}
	extractErr := <-extractDone
	close(stopArchiveCloser)
	<-archiveCloseDone

	if err := operationCtx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	if extractErr != nil && !errors.Is(extractErr, io.ErrClosedPipe) {
		return sanitizedArchiveError(extractErr)
	}
	if transferErr != nil {
		return errors.New("transfer BACPAC to restore container failed")
	}
	if extractErr != nil {
		return sanitizedArchiveError(extractErr)
	}

	deadline, _ := operationCtx.Deadline()
	remaining := time.Until(deadline)
	var importErr error
	if err := operationCtx.Err(); err != nil {
		importErr = err
	} else if remaining <= 0 {
		importErr = context.DeadlineExceeded
	} else {
		environment := []container.EnvironmentVariable{
			{Name: "REKSICHERUNG_DB_NAME", Value: options.Database},
			{Name: "REKSICHERUNG_DB_USER", Value: options.Username},
			{Name: "REKSICHERUNG_DB_PASSWORD", Value: options.Password},
			{Name: "REKSICHERUNG_SQLPACKAGE_TIMEOUT", Value: formatTimeout(remaining)},
		}
		output, err := importer.runtime.ExecWithEnvironment(operationCtx, target, []string{"sh", "-c", sqlPackageImportScript}, environment)
		importErr = err
		if len(output) > maxSqlPackageOutput {
			importErr = errors.New("SqlPackage output exceeded its size limit")
		}
	}

	if importErr != nil {
		if ctxErr := operationCtx.Err(); ctxErr != nil {
			importErr = ctxErr
		} else {
			importErr = errors.New("SqlPackage BACPAC import failed")
		}
	}
	return importErr
}

func formatTimeout(timeout time.Duration) string {
	return strconv.FormatFloat(timeout.Seconds(), 'f', 9, 64)
}

func validateImportOptions(options ImportOptions) error {
	if !sqlIdentifier.MatchString(options.Database) {
		return errors.New("database name must be a 1-128 character ASCII SQL identifier")
	}
	if !sqlIdentifier.MatchString(options.Username) {
		return errors.New("SQL Server username must be a 1-128 character ASCII identifier")
	}
	if options.Password == "" || len(options.Password) > 4096 || strings.ContainsAny(options.Password, "\x00\r\n") {
		return errors.New("SQL Server password must be 1-4096 bytes and contain no NUL or line breaks")
	}
	return container.ValidateEnvironment([]container.EnvironmentVariable{
		{Name: "REKSICHERUNG_DB_NAME", Value: options.Database},
		{Name: "REKSICHERUNG_DB_USER", Value: options.Username},
		{Name: "REKSICHERUNG_DB_PASSWORD", Value: options.Password},
	})
}

func sanitizedArchiveError(err error) error {
	switch {
	case errors.Is(err, ErrUnsupportedFormat):
		return ErrUnsupportedFormat
	case errors.Is(err, ErrLimitExceeded):
		return ErrLimitExceeded
	case errors.Is(err, ErrUnsafePath):
		return ErrUnsafePath
	case errors.Is(err, ErrNoBACPAC):
		return ErrNoBACPAC
	case errors.Is(err, ErrMultipleBACPAC):
		return ErrMultipleBACPAC
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return errors.New("read GoBackup BACPAC archive failed")
	}
}
