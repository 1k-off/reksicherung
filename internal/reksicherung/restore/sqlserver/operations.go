// Package sqlserver runs narrowly scoped SQL Server maintenance commands in an
// already managed restore container.
package sqlserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

const (
	sqlcmdPath       = "/opt/mssql-tools18/bin/sqlcmd"
	maxOutputBytes   = 64 << 10
	readinessTimeout = 2 * time.Minute
	commandTimeout   = 15 * time.Second
	checkDBTimeout   = 30 * time.Minute
	dropDBTimeout    = 30 * time.Second
	pollInterval     = time.Second
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
)

type commandRuntime interface {
	ExecWithEnvironment(context.Context, container.Container, []string, []container.EnvironmentVariable) ([]byte, error)
}

// IntegrityResult contains only the outcome and the database bound at
// construction. sqlcmd output is never returned to callers.
type IntegrityResult struct {
	Database string
	Passed   bool
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
	return IntegrityResult{Database: operations.database, Passed: true}, nil
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
	command := []string{
		sqlcmdPath,
		"-b",
		"-C",
		"-S", "localhost",
		"-U", "sa",
		"-d", "master",
		"-Q", query,
	}
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
