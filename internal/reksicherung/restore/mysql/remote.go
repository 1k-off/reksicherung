package mysql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultMySQLCmd      = "mysql"
	defaultMySQLCheckCmd = "mysqlcheck"
)

// RemoteCommandRunner runs a bounded executable locally in the MCP process.
// stdin may be nil. It is independent of any container engine.
type RemoteCommandRunner interface {
	Run(ctx context.Context, executable string, args []string, stdin io.Reader, maxOutput int) ([]byte, error)
}

// RemoteConfig describes an operator-managed MySQL server reachable from the
// MCP service.
type RemoteConfig struct {
	Host         string
	Port         int
	Username     string
	Password     string
	MySQL        string
	MySQLCheck   string
	TempDir      string // directory for 0600 credential files; default os.TempDir()
	Timeout      time.Duration
	MaxDumpBytes int64
}

// RemoteOperations restores into run-derived database names on an
// operator-managed MySQL server. It never creates, starts, or removes services.
type RemoteOperations struct {
	runner RemoteCommandRunner
	config RemoteConfig
}

// NewRemote creates operations for a pre-existing MySQL server.
func NewRemote(runner RemoteCommandRunner, config RemoteConfig) (*RemoteOperations, error) {
	if runner == nil {
		runner = remoteExecRunner{}
	}
	if config.MySQL == "" {
		config.MySQL = defaultMySQLCmd
	}
	if config.MySQLCheck == "" {
		config.MySQLCheck = defaultMySQLCheckCmd
	}
	if config.TempDir == "" {
		config.TempDir = os.TempDir()
	}
	if config.Timeout == 0 {
		config.Timeout = restoreTimeout
	}
	if config.MaxDumpBytes == 0 {
		config.MaxDumpBytes = maxDumpBytes
	}
	if !validRemoteValue(config.Host, 512) || strings.HasPrefix(config.Host, "-") ||
		config.Port < 1 || config.Port > 65535 ||
		!validRemoteValue(config.Username, 128) || strings.HasPrefix(config.Username, "-") ||
		config.Password == "" || strings.ContainsAny(config.Password, "\x00\r\n") ||
		!validRemoteValue(config.MySQL, 4096) || strings.HasPrefix(config.MySQL, "-") ||
		!validRemoteValue(config.MySQLCheck, 4096) || strings.HasPrefix(config.MySQLCheck, "-") ||
		strings.ContainsAny(config.TempDir, "\x00\r\n") ||
		config.Timeout < time.Second || config.Timeout > 24*time.Hour ||
		config.MaxDumpBytes < 1 || config.MaxDumpBytes > maxDumpBytes {
		return nil, ErrInvalidConfig
	}
	return &RemoteOperations{runner: runner, config: config}, nil
}

func sanitizeCommandError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return contextError(ctx.Err())
	}
	for _, known := range []error{ErrOutputLimit, ErrTimeout, ErrCanceled, ErrInvalidDump,
		ErrDumpLimit, ErrTransferFailed, ErrInvalidConfig, ErrCredentialFileFailed} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrCommandFailed
}

// RestoreDump creates the run-scoped database and pipes one rewritten logical
// dump into it. The dump must target at most sourceDatabase; any other schema
// statement fails closed. Seekable dumps are fully validated before the
// database is created.
func (operations *RemoteOperations) RestoreDump(ctx context.Context, runID string, dump io.ReadCloser, sourceDatabase string) (returnErr error) {
	if operations == nil || ctx == nil || dump == nil || !validDatabaseName(sourceDatabase) {
		if dump != nil {
			_ = dump.Close()
		}
		return ErrInvalidConfig
	}
	defer func() {
		if err := dump.Close(); err != nil && returnErr == nil {
			returnErr = ErrTransferFailed
		}
	}()

	database, err := DatabaseNameForRun(runID)
	if err != nil {
		return err
	}
	operationCtx, cancel := context.WithTimeout(ctx, operations.config.Timeout)
	defer cancel()

	if seeker, ok := dump.(io.Seeker); ok {
		if err := operations.validateDump(operationCtx, dump, seeker, sourceDatabase, database); err != nil {
			return err
		}
	}

	return operations.withDefaultsFile(func(defaults string) error {
		createQuery := "CREATE DATABASE " + quoteIdentifier(database)
		if _, err := operations.run(operationCtx, createDBTimeout, operations.config.MySQL,
			[]string{"--defaults-extra-file=" + defaults, "--batch", "-e", createQuery}, nil); err != nil {
			return sanitizeCommandError(operationCtx, err)
		}
		rewriter, err := NewDumpRewriter(&contextReader{ctx: operationCtx, reader: dump},
			sourceDatabase, database, operations.config.MaxDumpBytes)
		if err != nil {
			return err
		}
		_, runErr := operations.run(operationCtx, operations.config.Timeout, operations.config.MySQL,
			[]string{"--defaults-extra-file=" + defaults, "--binary-mode", "--local-infile=0", "--batch", database}, rewriter)
		if err := rewriter.Err(); err != nil {
			return sanitizeCommandError(operationCtx, err)
		}
		if runErr != nil {
			return sanitizeCommandError(operationCtx, runErr)
		}
		return nil
	})
}

func (operations *RemoteOperations) validateDump(ctx context.Context, dump io.Reader, seeker io.Seeker, source, target string) error {
	rewriter, err := NewDumpRewriter(&contextReader{ctx: ctx, reader: dump}, source, target, operations.config.MaxDumpBytes)
	if err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, rewriter); err != nil {
		if ctx.Err() != nil {
			return contextError(ctx.Err())
		}
		return sanitizeCommandError(ctx, err)
	}
	if _, err := seeker.Seek(0, io.SeekStart); err != nil {
		return ErrTransferFailed
	}
	return nil
}

// CheckIntegrity runs the fixed, read-only mysqlcheck --check against the
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
	operationCtx, cancel := context.WithTimeout(ctx, checkDBTimeout)
	defer cancel()
	err = operations.withDefaultsFile(func(defaults string) error {
		_, err := operations.run(operationCtx, checkDBTimeout, operations.config.MySQLCheck,
			[]string{"--defaults-extra-file=" + defaults, "--check", database}, nil)
		return sanitizeCommandError(operationCtx, err)
	})
	if err != nil {
		return IntegrityResult{}, err
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
	operationCtx, cancel := context.WithTimeout(ctx, dropDBTimeout)
	defer cancel()
	return operations.withDefaultsFile(func(defaults string) error {
		query := "DROP DATABASE IF EXISTS " + quoteIdentifier(database)
		_, err := operations.run(operationCtx, dropDBTimeout, operations.config.MySQL,
			[]string{"--defaults-extra-file=" + defaults, "--batch", "-e", query}, nil)
		return sanitizeCommandError(operationCtx, err)
	})
}

func (operations *RemoteOperations) run(ctx context.Context, timeout time.Duration, executable string, args []string, stdin io.Reader) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := operations.runner.Run(commandCtx, executable, args, stdin, remoteOutputLimit)
	if err != nil {
		if commandCtx.Err() != nil {
			return nil, contextError(commandCtx.Err())
		}
		var known error
		for _, candidate := range []error{ErrOutputLimit, ErrInvalidDump, ErrDumpLimit} {
			if errors.Is(err, candidate) {
				known = candidate
				break
			}
		}
		if known != nil {
			return nil, known
		}
		return nil, ErrCommandFailed
	}
	if len(output) > remoteOutputLimit {
		return nil, ErrOutputLimit
	}
	return output, nil
}

// withDefaultsFile writes a 0600 MySQL option file holding connection
// credentials, runs fn, and removes the file. Credentials never appear in argv.
func (operations *RemoteOperations) withDefaultsFile(fn func(path string) error) (returnErr error) {
	file, err := os.CreateTemp(operations.config.TempDir, "reksicherung-mysql-*.cnf")
	if err != nil {
		return ErrCredentialFileFailed
	}
	path := file.Name()
	defer func() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, ErrCredentialFileFailed)
		}
	}()
	if err := file.Chmod(0600); err != nil && !errors.Is(err, os.ErrPermission) {
		_ = file.Close()
		return ErrCredentialFileFailed
	}
	_, writeErr := io.WriteString(file, defaultsFileContent(operations.config))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return ErrCredentialFileFailed
	}
	return fn(path)
}

func defaultsFileContent(config RemoteConfig) string {
	return fmt.Sprintf("[client]\nuser=%s\npassword=%s\nhost=%s\nport=%d\nprotocol=tcp\n",
		optionValue(config.Username), optionValue(config.Password), optionValue(config.Host), config.Port)
}

// optionValue double-quotes a MySQL option-file value so '#', ';', spaces and
// similar characters are literal.
func optionValue(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(value) + `"`
}

type remoteExecRunner struct{}

func (remoteExecRunner) Run(ctx context.Context, executable string, args []string, stdin io.Reader, maxOutput int) ([]byte, error) {
	if ctx == nil || executable == "" || maxOutput < 1 || maxOutput > remoteOutputLimit {
		return nil, ErrInvalidConfig
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}
	command.Stdin = stdin
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

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
