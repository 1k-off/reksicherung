// Package sqlserver runs narrowly scoped SQL Server maintenance commands in an
// already managed restore container.
package sqlserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

const (
	sqlcmdPath           = "/opt/mssql-tools18/bin/sqlcmd"
	maxOutputBytes       = 64 << 10
	readinessTimeout     = 2 * time.Minute
	commandTimeout       = 15 * time.Second
	checkDBTimeout       = 30 * time.Minute
	dropDBTimeout        = 30 * time.Second
	nativeRestoreTimeout = 6 * time.Hour
	fileListTimeout      = 30 * time.Second
	maxNativeBackupBytes = int64(1 << 40)
	pollInterval         = time.Second
)

const (
	nativeBackupName = "reksicherung-native-restore.bak"
	nativeBackupPath = "/var/opt/mssql/backup/" + nativeBackupName
	maxBackupFiles   = 16
	fileListColumns  = 22
)

var (
	// ErrInvalidConfig indicates that the supplied target, credentials, or
	// managed-container handle is invalid.
	ErrInvalidConfig = errors.New("invalid SQL Server restore configuration")
	// ErrNotReady indicates that SQL Server did not become ready in time.
	ErrNotReady = errors.New("SQL Server did not become ready")
	// ErrTimeout indicates that a bounded SQL Server operation timed out.
	ErrTimeout = errors.New("SQL Server operation timed out")
	// ErrCanceled indicates that a bounded SQL Server operation was canceled.
	ErrCanceled = errors.New("SQL Server operation canceled")
	// ErrCommandFailed indicates that sqlcmd failed. Runtime and SQL diagnostics
	// are intentionally not exposed.
	ErrCommandFailed = errors.New("SQL Server command failed")
	// ErrOutputLimit indicates that sqlcmd exceeded the adapter's output limit.
	ErrOutputLimit = errors.New("SQL Server command output exceeded limit")
	// ErrInvalidBackupMetadata indicates that FILELISTONLY output was malformed
	// or described a backup this bounded restore operation does not support.
	ErrInvalidBackupMetadata = errors.New("SQL Server backup metadata is invalid or unsupported")
	// ErrBackupTransferFailed indicates that the bounded backup transfer failed.
	ErrBackupTransferFailed = errors.New("SQL Server backup transfer failed")
)

type commandRuntime interface {
	ExecWithEnvironment(context.Context, container.Container, []string, []container.EnvironmentVariable) ([]byte, error)
}

type backupTransferRuntime interface {
	Transfer(context.Context, container.Container, string, io.ReadCloser, int64) error
}

// IntegrityResult contains only the outcome and the database bound at
// construction. sqlcmd output is never returned to callers.
type IntegrityResult struct {
	Database         string
	CommandCompleted bool
}

// Operations is bound to one managed container and one validated database.
// Its methods do not accept a database name, preventing callers from widening
// the operation's scope.
type Operations struct {
	runtime  commandRuntime
	target   container.Container
	database string
	password string
}

// New binds SQL operations to a managed container and the database derived
// from that container's run ID. Password is supplied to sqlcmd only through
// ExecWithEnvironment.
func New(runtime commandRuntime, target container.Container, database, password string) (*Operations, error) {
	expectedDatabase, databaseErr := DatabaseNameForRun(target.RunID)
	if runtime == nil || target.ID == "" || target.RunID == "" ||
		target.Name != container.ContainerName(target.RunID) ||
		databaseErr != nil || database != expectedDatabase || password == "" ||
		strings.ContainsAny(password, "\x00\r\n") {
		return nil, ErrInvalidConfig
	}
	return &Operations{
		runtime:  runtime,
		target:   target,
		database: database,
		password: password,
	}, nil
}

// RestoreBackup transfers one bounded native SQL Server backup into the fixed
// restore-container path, reads its file list, and restores only into the
// database bound to this Operations value. The archive is always closed.
func (operations *Operations) RestoreBackup(ctx context.Context, archive io.ReadCloser) error {
	if archive == nil {
		return ErrInvalidConfig
	}
	ownedArchive := &closeOnceReadCloser{ReadCloser: archive}
	archive = ownedArchive
	defer archive.Close()

	transferRuntime, ok := operations.runtime.(backupTransferRuntime)
	if !ok {
		return ErrInvalidConfig
	}
	operationCtx, cancel, err := boundedContext(ctx, nativeRestoreTimeout)
	if err != nil {
		return err
	}
	defer cancel()

	if err := transferRuntime.Transfer(operationCtx, operations.target,
		nativeBackupName, archive, maxNativeBackupBytes); err != nil {
		if operationCtx.Err() != nil {
			if ctx.Err() != nil {
				return contextError(ctx.Err())
			}
			return ErrTimeout
		}
		return ErrBackupTransferFailed
	}

	fileListQuery := "RESTORE FILELISTONLY FROM DISK = " + quoteStringLiteral(nativeBackupPath) + ";"
	fileListCtx, fileListCancel := boundedChildContext(operationCtx, fileListTimeout)
	output, err := operations.executeWithOptions(fileListCtx, fileListQuery, "-h", "-1", "-W", "-s", "|")
	if err != nil {
		sanitizedErr := sanitizeRestoreCommandError(fileListCtx, err)
		fileListCancel()
		return sanitizedErr
	}
	fileListCancel()
	files, err := parseBackupFileList(output)
	if err != nil {
		return err
	}

	query, err := restoreQuery(operations.database, files)
	if err != nil {
		return ErrInvalidBackupMetadata
	}
	if _, err := operations.executeBounded(operationCtx, nativeRestoreTimeout, query); err != nil {
		return sanitizeRestoreCommandError(operationCtx, err)
	}
	return nil
}

// DatabaseNameForRun returns the only database name that restore operations
// may inspect or drop for a run. Binding database cleanup to the runtime run ID
// prevents a caller from accidentally dropping an unrelated database.
func DatabaseNameForRun(runID string) (string, error) {
	if !validRunID(runID) {
		return "", ErrInvalidConfig
	}
	digest := sha256.Sum256([]byte(runID))
	return "reksicherung_" + hex.EncodeToString(digest[:16]), nil
}

// WaitReady polls the SQL Server instance using the SQLCMD shipped in the
// supported base image. It checks instance readiness through master, not
// whether the run-scoped restore database exists. Runtime errors are treated
// as startup-not-ready and are discarded rather than exposed.
func (operations *Operations) WaitReady(ctx context.Context) error {
	operationCtx, cancel, err := boundedContext(ctx, readinessTimeout)
	if err != nil {
		return err
	}
	defer cancel()

	for {
		commandCtx, commandCancel := boundedChildContext(operationCtx, commandTimeout)
		_, execErr := operations.execute(commandCtx, "SELECT 1;")
		commandCancel()
		if execErr == nil {
			return nil
		}
		if err := operationCtx.Err(); err != nil {
			if ctx.Err() != nil {
				return contextError(ctx.Err())
			}
			return ErrNotReady
		}
		if errors.Is(execErr, ErrOutputLimit) {
			return ErrOutputLimit
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-operationCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return contextError(ctx.Err())
			}
			return ErrNotReady
		case <-timer.C:
		}
	}
}

// CheckIntegrity runs the fixed, non-repair DBCC CHECKDB command against the
// database selected when Operations was constructed.
func (operations *Operations) CheckIntegrity(ctx context.Context) (IntegrityResult, error) {
	operationCtx, cancel, err := boundedContext(ctx, checkDBTimeout)
	if err != nil {
		return IntegrityResult{}, err
	}
	defer cancel()

	query := "DBCC CHECKDB (" + quoteIdentifier(operations.database) + ") WITH NO_INFOMSGS;"
	if _, err := operations.executeBounded(operationCtx, checkDBTimeout, query); err != nil {
		if errors.Is(err, ErrOutputLimit) {
			return IntegrityResult{}, ErrOutputLimit
		}
		if operationCtx.Err() != nil {
			return IntegrityResult{}, contextError(operationCtx.Err())
		}
		if errors.Is(err, ErrTimeout) {
			return IntegrityResult{}, ErrTimeout
		}
		return IntegrityResult{}, ErrCommandFailed
	}
	return IntegrityResult{Database: operations.database, CommandCompleted: true}, nil
}

// DropDatabase drops only the bound database, if it exists, to support restore
// cleanup. It does not return SQL or runtime output.
func (operations *Operations) DropDatabase(ctx context.Context) error {
	operationCtx, cancel, err := boundedContext(ctx, dropDBTimeout)
	if err != nil {
		return err
	}
	defer cancel()

	query := "IF DB_ID(N'" + operations.database + "') IS NOT NULL BEGIN " +
		"ALTER DATABASE " + quoteIdentifier(operations.database) +
		" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE " +
		quoteIdentifier(operations.database) + "; END;"
	_, err = operations.executeBounded(operationCtx, commandTimeout, query)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrOutputLimit) {
		return ErrOutputLimit
	}
	if operationCtx.Err() != nil {
		return contextError(operationCtx.Err())
	}
	if errors.Is(err, ErrTimeout) {
		return ErrTimeout
	}
	return ErrCommandFailed
}

func (operations *Operations) executeBounded(ctx context.Context, timeout time.Duration, query string) ([]byte, error) {
	commandCtx, cancel := boundedChildContext(ctx, timeout)
	defer cancel()
	return operations.execute(commandCtx, query)
}

func (operations *Operations) execute(ctx context.Context, query string) ([]byte, error) {
	return operations.executeWithOptions(ctx, query)
}

func (operations *Operations) executeWithOptions(ctx context.Context, query string, options ...string) ([]byte, error) {
	command := []string{
		sqlcmdPath,
		"-b",
		"-C",
		"-S", "localhost",
		"-U", "sa",
		"-d", "master",
	}
	command = append(command, options...)
	command = append(command, "-Q", query)
	output, err := operations.runtime.ExecWithEnvironment(ctx, operations.target, command,
		[]container.EnvironmentVariable{{Name: "SQLCMDPASSWORD", Value: operations.password}})
	if err != nil {
		if ctx.Err() != nil {
			return nil, contextError(ctx.Err())
		}
		return nil, ErrCommandFailed
	}
	if len(output) > maxOutputBytes {
		return nil, ErrOutputLimit
	}
	return output, nil
}

func sanitizeRestoreCommandError(operationCtx context.Context, err error) error {
	if errors.Is(err, ErrOutputLimit) {
		return ErrOutputLimit
	}
	if operationCtx.Err() != nil {
		return contextError(operationCtx.Err())
	}
	if errors.Is(err, ErrTimeout) {
		return ErrTimeout
	}
	return ErrCommandFailed
}

func quoteStringLiteral(value string) string {
	return "N'" + strings.ReplaceAll(value, "'", "''") + "'"
}

type backupFile struct {
	logicalName string
	fileType    string
}

type closeOnceReadCloser struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (reader *closeOnceReadCloser) Close() error {
	reader.once.Do(func() {
		reader.err = reader.ReadCloser.Close()
	})
	return reader.err
}

func parseBackupFileList(output []byte) ([]backupFile, error) {
	if len(output) == 0 || len(output) > maxOutputBytes || !utf8.Valid(output) {
		return nil, ErrInvalidBackupMetadata
	}
	text := strings.TrimSuffix(string(output), "\n")
	if text == "" {
		return nil, ErrInvalidBackupMetadata
	}
	rows := strings.Split(text, "\n")
	if len(rows) > maxBackupFiles {
		return nil, ErrInvalidBackupMetadata
	}

	files := make([]backupFile, 0, len(rows))
	seenNames := make(map[string]struct{}, len(rows))
	dataCount, logCount := 0, 0
	for _, row := range rows {
		row = strings.TrimSuffix(row, "\r")
		if row == "" || len(row) > 4096 {
			return nil, ErrInvalidBackupMetadata
		}
		for _, char := range row {
			if unicode.IsControl(char) {
				return nil, ErrInvalidBackupMetadata
			}
		}

		columns := strings.Split(row, "|")
		if len(columns) != fileListColumns {
			return nil, ErrInvalidBackupMetadata
		}
		logicalName, fileType := columns[0], columns[2]
		if !validLogicalFileName(logicalName) {
			return nil, ErrInvalidBackupMetadata
		}
		switch fileType {
		case "D":
			dataCount++
		case "L":
			logCount++
		default:
			return nil, ErrInvalidBackupMetadata
		}
		key := strings.ToLower(logicalName)
		if _, exists := seenNames[key]; exists {
			return nil, ErrInvalidBackupMetadata
		}
		seenNames[key] = struct{}{}
		files = append(files, backupFile{logicalName: logicalName, fileType: fileType})
	}
	if dataCount == 0 || logCount == 0 {
		return nil, ErrInvalidBackupMetadata
	}
	return files, nil
}

func validLogicalFileName(name string) bool {
	if name == "" || len(name) > 512 || strings.TrimSpace(name) != name || !utf8.ValidString(name) {
		return false
	}
	runeCount := 0
	for _, char := range name {
		runeCount++
		if runeCount > 128 || unicode.IsControl(char) || char == '|' {
			return false
		}
	}
	return true
}

func restoreQuery(database string, files []backupFile) (string, error) {
	if !validDatabaseName(database) || len(files) == 0 || len(files) > maxBackupFiles {
		return "", ErrInvalidBackupMetadata
	}

	var query strings.Builder
	query.WriteString("RESTORE DATABASE ")
	query.WriteString(quoteIdentifier(database))
	query.WriteString(" FROM DISK = ")
	query.WriteString(quoteStringLiteral(nativeBackupPath))
	query.WriteString(" WITH FILE = 1, REPLACE")
	dataCount, logCount := 0, 0
	for _, file := range files {
		if !validLogicalFileName(file.logicalName) {
			return "", ErrInvalidBackupMetadata
		}
		var physicalPath string
		switch file.fileType {
		case "D":
			dataCount++
			extension := "ndf"
			if dataCount == 1 {
				extension = "mdf"
			}
			physicalPath = fmt.Sprintf("/var/opt/mssql/data/%s_data_%03d.%s", database, dataCount, extension)
		case "L":
			logCount++
			physicalPath = fmt.Sprintf("/var/opt/mssql/data/%s_log_%03d.ldf", database, logCount)
		default:
			return "", ErrInvalidBackupMetadata
		}
		query.WriteString(", MOVE ")
		query.WriteString(quoteStringLiteral(file.logicalName))
		query.WriteString(" TO ")
		query.WriteString(quoteStringLiteral(physicalPath))
	}
	if dataCount == 0 || logCount == 0 {
		return "", ErrInvalidBackupMetadata
	}
	query.WriteString(", RECOVERY;")
	return query.String(), nil
}

func boundedContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, contextError(err)
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	return operationCtx, cancel, nil
}

func boundedChildContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}

func contextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return ErrCanceled
	}
	return ErrTimeout
}

func validDatabaseName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '$' ||
			char == '#' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validRunID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for index, char := range id {
		alphaNumeric := char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9'
		if index == 0 && !alphaNumeric {
			return false
		}
		if !alphaNumeric && char != '_' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func quoteIdentifier(name string) string {
	// validDatabaseName excludes SQL Server's closing bracket delimiter.
	return "[" + name + "]"
}
