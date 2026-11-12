package sqlserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultRemoteSQLCmd = "sqlcmd"
	remoteOutputLimit   = 64 << 10

	// DefaultBackupDirectory is used when REKSICHERUNG_SQLSERVER_BACKUP_DIR is unset.
	DefaultBackupDirectory = "/var/opt/mssql/backup"
	// DefaultDataDirectory is used when REKSICHERUNG_SQLSERVER_DATA_DIR is unset.
	DefaultDataDirectory = "/var/opt/mssql/data"
)

var ErrRemoteBackupLimit = errors.New("SQL Server backup exceeded the configured size limit")

// RemoteCommandRunner runs a bounded executable locally in the MCP process.
// It is independent of any container engine.
type RemoteCommandRunner interface {
	Run(context.Context, string, []string, []string, int) ([]byte, error)
}

// RemoteConfig describes an operator-managed SQL Server reachable from the MCP
// service. BackupDirectory must be the same shared-volume path visible to the
// SQL Server service; DataDirectory is the SQL Server data directory.
type RemoteConfig struct {
	Server          string
	Username        string
	Password        string
	BackupDirectory string
	DataDirectory   string
	SQLCmd          string
	SQLPackage      string
	Timeout         time.Duration
	MaxBackupBytes  int64
}

// RemoteOperations restores into run-derived database names on an
// operator-managed SQL Server. It never creates, starts, or removes services.
type RemoteOperations struct {
	runner RemoteCommandRunner
	config RemoteConfig
}

// NewRemote creates operations for a pre-existing SQL Server and validates the
// shared backup-volume configuration.
func NewRemote(runner RemoteCommandRunner, config RemoteConfig) (*RemoteOperations, error) {
	if runner == nil {
		runner = remoteExecRunner{}
	}
	if config.SQLCmd == "" {
		config.SQLCmd = defaultRemoteSQLCmd
	}
	if config.SQLPackage == "" {
		config.SQLPackage = "sqlpackage"
	}
	if config.Timeout == 0 {
		config.Timeout = nativeRestoreTimeout
	}
	if config.MaxBackupBytes == 0 {
		config.MaxBackupBytes = maxNativeBackupBytes
	}
	if !validRemoteValue(config.Server, 512) ||
		strings.HasPrefix(config.Server, "-") ||
		!validDatabaseName(config.Username) || config.Password == "" ||
		strings.ContainsAny(config.Password, "\x00\r\n") ||
		!filepath.IsAbs(config.BackupDirectory) ||
		filepath.Clean(config.BackupDirectory) != config.BackupDirectory ||
		!filepath.IsAbs(config.DataDirectory) ||
		filepath.Clean(config.DataDirectory) != config.DataDirectory ||
		!validRemoteValue(config.SQLCmd, 4096) ||
		strings.HasPrefix(config.SQLCmd, "-") ||
		!validRemoteValue(config.SQLPackage, 4096) ||
		strings.HasPrefix(config.SQLPackage, "-") ||
		config.Timeout < time.Second || config.Timeout > 24*time.Hour ||
		config.MaxBackupBytes < 1 || config.MaxBackupBytes > maxNativeBackupBytes {
		return nil, ErrInvalidConfig
	}
	return &RemoteOperations{runner: runner, config: config}, nil
}

func sanitizeRestoreCommandError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return contextError(ctx.Err())
	}
	if errors.Is(err, ErrOutputLimit) || errors.Is(err, ErrTimeout) ||
		errors.Is(err, ErrCanceled) || errors.Is(err, ErrInvalidBackupMetadata) ||
		errors.Is(err, ErrRemoteBackupLimit) || errors.Is(err, ErrBackupTransferFailed) ||
		errors.Is(err, ErrBackupCleanupFailed) || errors.Is(err, ErrInvalidConfig) {
		return err
	}
	return ErrCommandFailed
}

// RestoreBackup stages one archive in the configured shared volume, validates
// that it contains exactly one full backup for expectedSourceDatabase, and
// restores it to the database derived from runID.
func (operations *RemoteOperations) RestoreBackup(ctx context.Context, runID string, archive io.ReadCloser, expectedSourceDatabase string) (returnErr error) {
	if operations == nil || ctx == nil || archive == nil || !validDatabaseName(expectedSourceDatabase) {
		if archive != nil {
			_ = archive.Close()
		}
		return ErrInvalidConfig
	}
	defer func() {
		if err := archive.Close(); err != nil && returnErr == nil {
			returnErr = ErrBackupTransferFailed
		}
	}()

	database, err := DatabaseNameForRun(runID)
	if err != nil {
		return err
	}
	operationCtx, cancel := context.WithTimeout(ctx, operations.config.Timeout)
	defer cancel()

	if err := os.MkdirAll(operations.config.BackupDirectory, 0700); err != nil {
		return ErrBackupTransferFailed
	}
	finalPath := filepath.Join(operations.config.BackupDirectory, database+".bak")
	temporary, err := os.CreateTemp(operations.config.BackupDirectory, ".reksicherung-*.tmp")
	if err != nil {
		return ErrBackupTransferFailed
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return ErrBackupTransferFailed
	}
	written, copyErr := io.Copy(temporary, &contextLimitedReader{
		ctx: operationCtx, reader: archive, remaining: operations.config.MaxBackupBytes,
	})
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil || written == 0 {
		if operationCtx.Err() != nil {
			return contextError(operationCtx.Err())
		}
		if errors.Is(copyErr, ErrRemoteBackupLimit) {
			return ErrRemoteBackupLimit
		}
		return ErrBackupTransferFailed
	}
	if err := operationCtx.Err(); err != nil {
		return contextError(err)
	}
	// Do not overwrite a previous artifact or an operator-managed file.
	if _, err := os.Lstat(finalPath); err == nil {
		return ErrBackupTransferFailed
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrBackupTransferFailed
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return ErrBackupTransferFailed
	}
	defer func() {
		if err := os.Remove(finalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, ErrBackupCleanupFailed)
		}
	}()

	headerQuery := "RESTORE HEADERONLY FROM DISK = " + quoteStringLiteral(finalPath) + ";"
	header, err := operations.execute(operationCtx, fileListTimeout,
		[]string{"-h", "-1", "-W", "-s", "|"}, headerQuery)
	if err != nil {
		return sanitizeRestoreCommandError(operationCtx, err)
	}
	if err := validateSingleFullBackupSet(header, expectedSourceDatabase); err != nil {
		return err
	}
	fileListQuery := "RESTORE FILELISTONLY FROM DISK = " + quoteStringLiteral(finalPath) + " WITH FILE = 1;"
	fileList, err := operations.execute(operationCtx, fileListTimeout,
		[]string{"-h", "-1", "-W", "-s", "|"}, fileListQuery)
	if err != nil {
		return sanitizeRestoreCommandError(operationCtx, err)
	}
	files, err := parseBackupFileList(fileList)
	if err != nil {
		return err
	}
	query, err := restoreQueryInDirectory(database, files, operations.config.DataDirectory, finalPath)
	if err != nil {
		return ErrInvalidBackupMetadata
	}
	if _, err := operations.execute(operationCtx, operations.config.Timeout, nil, query); err != nil {
		return sanitizeRestoreCommandError(operationCtx, err)
	}
	return nil
}

// ImportBACPAC stages one BACPAC in the shared backup volume and imports it
// into the run-scoped database with a local sqlpackage executable. It never
// creates or manages containers.
func (operations *RemoteOperations) ImportBACPAC(ctx context.Context, runID string, archive io.ReadCloser) (returnErr error) {
	if operations == nil || ctx == nil || archive == nil {
		if archive != nil {
			_ = archive.Close()
		}
		return ErrInvalidConfig
	}
	defer func() {
		if err := archive.Close(); err != nil && returnErr == nil {
			returnErr = ErrBackupTransferFailed
		}
	}()

	database, err := DatabaseNameForRun(runID)
	if err != nil {
		return err
	}
	operationCtx, cancel := context.WithTimeout(ctx, operations.config.Timeout)
	defer cancel()

	if err := os.MkdirAll(operations.config.BackupDirectory, 0700); err != nil {
		return ErrBackupTransferFailed
	}
	finalPath := filepath.Join(operations.config.BackupDirectory, database+".bacpac")
	temporary, err := os.CreateTemp(operations.config.BackupDirectory, ".reksicherung-*.tmp")
	if err != nil {
		return ErrBackupTransferFailed
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return ErrBackupTransferFailed
	}
	written, copyErr := io.Copy(temporary, &contextLimitedReader{
		ctx: operationCtx, reader: archive, remaining: operations.config.MaxBackupBytes,
	})
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil || written == 0 {
		if operationCtx.Err() != nil {
			return contextError(operationCtx.Err())
		}
		if errors.Is(copyErr, ErrRemoteBackupLimit) {
			return ErrRemoteBackupLimit
		}
		return ErrBackupTransferFailed
	}
	if err := operationCtx.Err(); err != nil {
		return contextError(err)
	}
	if _, err := os.Lstat(finalPath); err == nil {
		return ErrBackupTransferFailed
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrBackupTransferFailed
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return ErrBackupTransferFailed
	}
	defer func() {
		if err := os.Remove(finalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, ErrBackupCleanupFailed)
		}
	}()

	responsePath := filepath.Join(operations.config.BackupDirectory, database+".sqlpackage.rsp")
	responseBody := strings.Join([]string{
		"/Action:Import",
		"/SourceFile:" + finalPath,
		"/TargetServerName:" + operations.config.Server,
		"/TargetDatabaseName:" + database,
		"/TargetUser:" + operations.config.Username,
		"/TargetPassword:" + operations.config.Password,
		"/TargetEncryptConnection:True",
		"/TargetTrustServerCertificate:True",
	}, "\n") + "\n"
	if err := os.WriteFile(responsePath, []byte(responseBody), 0600); err != nil {
		return ErrBackupTransferFailed
	}
	defer func() {
		_ = os.Remove(responsePath)
	}()
	_, err = operations.runner.Run(operationCtx, operations.config.SQLPackage,
		[]string{"@" + responsePath}, nil, remoteOutputLimit)
	if err != nil {
		return sanitizeRestoreCommandError(operationCtx, err)
	}
	return nil
}

// CheckIntegrity runs the fixed, non-repair DBCC CHECKDB operation against the
// database derived from runID. A completed command is not a clean-integrity
// verdict.
func (operations *RemoteOperations) CheckIntegrity(ctx context.Context, runID string) (IntegrityResult, error) {
	if operations == nil || ctx == nil {
		return IntegrityResult{}, ErrInvalidConfig
	}
	database, err := DatabaseNameForRun(runID)
	if err != nil {
		return IntegrityResult{}, err
	}
	query := "DBCC CHECKDB (" + quoteIdentifier(database) + ") WITH NO_INFOMSGS, MAXDOP = 2;"
	operationCtx, cancel := context.WithTimeout(ctx, checkDBTimeout)
	defer cancel()
	if _, err := operations.execute(operationCtx, checkDBTimeout, nil, query); err != nil {
		return IntegrityResult{}, sanitizeRestoreCommandError(operationCtx, err)
	}
	return IntegrityResult{Database: database, CommandCompleted: true}, nil
}

// DropDatabase removes only the database derived from runID.
func (operations *RemoteOperations) DropDatabase(ctx context.Context, runID string) error {
	if operations == nil || ctx == nil {
		return ErrInvalidConfig
	}
	database, err := DatabaseNameForRun(runID)
	if err != nil {
		return err
	}
	query := "IF DB_ID(N'" + database + "') IS NOT NULL BEGIN " +
		"ALTER DATABASE " + quoteIdentifier(database) +
		" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE " +
		quoteIdentifier(database) + "; END;"
	operationCtx, cancel := context.WithTimeout(ctx, dropDBTimeout)
	defer cancel()
	if _, err := operations.execute(operationCtx, dropDBTimeout, nil, query); err != nil {
		return sanitizeRestoreCommandError(operationCtx, err)
	}
	return nil
}

func (operations *RemoteOperations) execute(ctx context.Context, timeout time.Duration, options []string, query string) ([]byte, error) {
	commandCtx, cancel := boundedChildContext(ctx, timeout)
	defer cancel()
	// -C trusts the server certificate (ODBC 18). Isolated restore targets are
	// typically compose/self-signed; sqlpackage already uses TrustServerCertificate.
	args := []string{"-b", "-N", "m", "-C", "-S", operations.config.Server, "-U", operations.config.Username, "-d", "master"}
	args = append(args, options...)
	args = append(args, "-Q", query)
	output, err := operations.runner.Run(commandCtx, operations.config.SQLCmd, args,
		[]string{"SQLCMDPASSWORD=" + operations.config.Password}, remoteOutputLimit)
	if err != nil {
		if commandCtx.Err() != nil {
			return nil, contextError(commandCtx.Err())
		}
		return nil, ErrCommandFailed
	}
	if len(output) > remoteOutputLimit {
		return nil, ErrOutputLimit
	}
	return output, nil
}

type remoteExecRunner struct{}

func (remoteExecRunner) Run(ctx context.Context, executable string, args, environment []string, maxOutput int) ([]byte, error) {
	if ctx == nil || executable == "" || maxOutput < 1 || maxOutput > remoteOutputLimit {
		return nil, ErrInvalidConfig
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}
	command.Env = append(command.Env, environment...)
	var output boundedCommandOutput
	output.limit = maxOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	runErr := command.Run()
	if output.exceeded {
		return nil, ErrOutputLimit
	}
	if runErr != nil {
		return nil, runErr
	}
	return output.Bytes(), nil
}

type boundedCommandOutput struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (output *boundedCommandOutput) Write(data []byte) (int, error) {
	if len(data) > output.limit-output.Len() {
		output.exceeded = true
		return 0, errors.New("command output limit exceeded")
	}
	return output.Buffer.Write(data)
}

type contextLimitedReader struct {
	ctx       context.Context
	reader    io.Reader
	remaining int64
}

func (reader *contextLimitedReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if reader.remaining < 0 {
		return 0, ErrRemoteBackupLimit
	}
	if int64(len(buffer)) > reader.remaining+1 {
		buffer = buffer[:reader.remaining+1]
	}
	n, err := reader.reader.Read(buffer)
	reader.remaining -= int64(n)
	if reader.remaining < 0 {
		return n, ErrRemoteBackupLimit
	}
	return n, err
}

func validRemoteValue(value string, max int) bool {
	if value == "" || len(value) > max || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func restoreQueryInDirectory(database string, files []backupFile, dataDirectory, backupPath string) (string, error) {
	if !validDatabaseName(database) || !filepath.IsAbs(dataDirectory) ||
		filepath.Clean(dataDirectory) != dataDirectory || !filepath.IsAbs(backupPath) ||
		filepath.Clean(backupPath) != backupPath || len(files) == 0 {
		return "", ErrInvalidBackupMetadata
	}
	var moves []string
	var dataIndex, logIndex int
	for _, file := range files {
		if !validLogicalFileName(file.logicalName) {
			return "", ErrInvalidBackupMetadata
		}
		var path string
		switch file.fileType {
		case "D":
			dataIndex++
			path = filepath.Join(dataDirectory, fmt.Sprintf("%s_%02d.mdf", database, dataIndex))
		case "L":
			logIndex++
			path = filepath.Join(dataDirectory, fmt.Sprintf("%s_%02d.ldf", database, logIndex))
		default:
			return "", ErrInvalidBackupMetadata
		}
		moves = append(moves, "MOVE "+quoteStringLiteral(file.logicalName)+" TO "+quoteStringLiteral(path))
	}
	if dataIndex == 0 || logIndex == 0 {
		return "", ErrInvalidBackupMetadata
	}
	return "RESTORE DATABASE " + quoteIdentifier(database) +
		" FROM DISK = " + quoteStringLiteral(backupPath) +
		" WITH FILE = 1, " + strings.Join(moves, ", ") + ", RECOVERY, REPLACE;", nil
}
