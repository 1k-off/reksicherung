package sqlserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

const testPassword = "test-secret-password"

type fakeRuntime struct {
	command      []string
	commands     [][]string
	target       container.Container
	environment  []container.EnvironmentVariable
	environments [][]container.EnvironmentVariable
	ctx          context.Context
	output       []byte
	err          error
	calls        int
	exec         func(context.Context) ([]byte, error)
	transferName string
	transferMax  int64
	transferErr  error
	transfer     func(context.Context, string, io.ReadCloser, int64) error
}

func (runtime *fakeRuntime) ExecWithEnvironment(ctx context.Context, target container.Container, command []string, environment []container.EnvironmentVariable) ([]byte, error) {
	runtime.calls++
	runtime.command = append([]string(nil), command...)
	runtime.commands = append(runtime.commands, append([]string(nil), command...))
	runtime.target = target
	runtime.environment = append([]container.EnvironmentVariable(nil), environment...)
	runtime.environments = append(runtime.environments, append([]container.EnvironmentVariable(nil), environment...))
	runtime.ctx = ctx
	if runtime.exec != nil {
		return runtime.exec(ctx)
	}
	return runtime.output, runtime.err
}

func (runtime *fakeRuntime) Transfer(ctx context.Context, _ container.Container, name string, source io.ReadCloser, maxBytes int64) error {
	runtime.transferName = name
	runtime.transferMax = maxBytes
	if runtime.transfer != nil {
		return runtime.transfer(ctx, name, source, maxBytes)
	}
	_, err := io.Copy(io.Discard, source)
	if err != nil {
		return err
	}
	return runtime.transferErr
}

func newTestOperations(t *testing.T, runtime *fakeRuntime) *Operations {
	t.Helper()
	target := testContainer()
	database, err := DatabaseNameForRun(target.RunID)
	if err != nil {
		t.Fatalf("DatabaseNameForRun() error = %v", err)
	}
	operations, err := New(runtime, target, database, testPassword)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return operations
}

func testContainer() container.Container {
	return container.Container{ID: "container-id", Name: "reksicherung-test-run", RunID: "test-run"}
}

func TestCheckIntegrityUsesFixedSQLCMDAndReturnsSanitizedResult(t *testing.T) {
	runtime := &fakeRuntime{
		output: []byte("database check output must not escape"),
		err:    nil,
	}
	database, err := DatabaseNameForRun(testContainer().RunID)
	if err != nil {
		t.Fatal(err)
	}
	operations := newTestOperations(t, runtime)

	result, err := operations.CheckIntegrity(context.Background())
	if err != nil {
		t.Fatalf("CheckIntegrity() error = %v", err)
	}
	if result != (IntegrityResult{Database: database, CommandCompleted: true}) {
		t.Fatalf("CheckIntegrity() result = %#v", result)
	}
	assertSQLCMDInvocation(t, runtime)
	query := runtime.command[len(runtime.command)-1]
	if query != "DBCC CHECKDB (["+database+"]) WITH NO_INFOMSGS;" {
		t.Fatalf("integrity query = %q", query)
	}
	if strings.Contains(strings.ToUpper(query), "REPAIR") {
		t.Fatalf("integrity query includes repair option: %q", query)
	}
}

func TestWaitReadyPollsKnownSQLCMDPath(t *testing.T) {
	runtime := &fakeRuntime{}
	runtime.exec = func(context.Context) ([]byte, error) {
		if runtime.calls == 1 {
			return nil, errors.New("startup diagnostic contains " + testPassword)
		}
		return []byte("1\n"), nil
	}
	operations := newTestOperations(t, runtime)

	if err := operations.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if runtime.calls != 2 {
		t.Fatalf("ExecWithEnvironment() calls = %d, want 2", runtime.calls)
	}
	assertSQLCMDInvocation(t, runtime)
	if got := runtime.command[len(runtime.command)-1]; got != "SELECT 1;" {
		t.Fatalf("readiness query = %q", got)
	}
}

func TestDropDatabaseIsScopedToBoundDatabase(t *testing.T) {
	runtime := &fakeRuntime{output: []byte("SQL diagnostic must not escape")}
	database, err := DatabaseNameForRun(testContainer().RunID)
	if err != nil {
		t.Fatal(err)
	}
	operations := newTestOperations(t, runtime)

	if err := operations.DropDatabase(context.Background()); err != nil {
		t.Fatalf("DropDatabase() error = %v", err)
	}
	assertSQLCMDInvocation(t, runtime)
	query := runtime.command[len(runtime.command)-1]
	for _, required := range []string{
		"DB_ID(N'" + database + "')",
		"ALTER DATABASE [" + database + "]",
		"DROP DATABASE [" + database + "]",
		"WITH ROLLBACK IMMEDIATE",
	} {
		if !strings.Contains(query, required) {
			t.Errorf("drop query %q does not contain %q", query, required)
		}
	}
	if strings.Contains(query, "master") || strings.Contains(query, "OtherDb") {
		t.Fatalf("drop query exceeds bound database scope: %q", query)
	}
}

func TestNewRejectsUnownedDatabaseAndInvalidContainer(t *testing.T) {
	runtime := &fakeRuntime{}
	database, err := DatabaseNameForRun(testContainer().RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, database := range []string{
		"master",
		"RestoredDb",
		"reksicherung_" + strings.Repeat("a", 32),
		"restore]; DROP DATABASE master;--",
		"database name",
	} {
		if _, err := New(runtime, testContainer(), database, testPassword); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("New(database %q) error = %v, want ErrInvalidConfig", database, err)
		}
	}
	otherRunDatabase, err := DatabaseNameForRun("another-run")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(runtime, testContainer(), otherRunDatabase, testPassword); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("New(database for another run) error = %v, want ErrInvalidConfig", err)
	}
	if _, err := New(runtime, container.Container{ID: "unmanaged"}, database, testPassword); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("New(invalid container) error = %v, want ErrInvalidConfig", err)
	}
	if _, err := New(runtime, testContainer(), database, "bad\npassword"); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("New(invalid password) error = %v, want ErrInvalidConfig", err)
	}
}

func TestDatabaseNameForRunIsDeterministicAndBounded(t *testing.T) {
	first, err := DatabaseNameForRun("restore-run-1")
	if err != nil {
		t.Fatalf("DatabaseNameForRun() error = %v", err)
	}
	second, err := DatabaseNameForRun("restore-run-1")
	if err != nil {
		t.Fatalf("DatabaseNameForRun() second error = %v", err)
	}
	other, err := DatabaseNameForRun("restore-run-2")
	if err != nil {
		t.Fatalf("DatabaseNameForRun() other error = %v", err)
	}
	if first != second || first == other || !validDatabaseName(first) {
		t.Fatalf("run database names = %q, %q, %q", first, second, other)
	}
	if _, err := DatabaseNameForRun("bad run id"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("DatabaseNameForRun(invalid ID) error = %v, want ErrInvalidConfig", err)
	}
}

func TestSQLCMDPasswordIsEnvironmentOnly(t *testing.T) {
	runtime := &fakeRuntime{}
	operations := newTestOperations(t, runtime)
	if _, err := operations.CheckIntegrity(context.Background()); err != nil {
		t.Fatalf("CheckIntegrity() error = %v", err)
	}

	if bytes.Contains([]byte(strings.Join(runtime.command, "\x00")), []byte(testPassword)) {
		t.Fatal("password appeared in SQLCMD argv")
	}
	if len(runtime.environment) != 1 || runtime.environment[0].Name != "SQLCMDPASSWORD" ||
		runtime.environment[0].Value != testPassword {
		t.Fatalf("ExecWithEnvironment() environment = %#v", runtime.environment)
	}
}

func TestOperationsBoundContextAndSanitizeContextErrors(t *testing.T) {
	runtime := &fakeRuntime{}
	runtime.exec = func(ctx context.Context) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("command context has no deadline")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > checkDBTimeout {
			t.Fatalf("command deadline is not bounded: %v remaining", remaining)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	operations := newTestOperations(t, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := operations.CheckIntegrity(ctx)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("CheckIntegrity() error = %v, want ErrTimeout", err)
	}
}

func TestOperationsReturnOnlySanitizedErrorsAndNoOutputOnOverflow(t *testing.T) {
	t.Run("runtime error", func(t *testing.T) {
		runtime := &fakeRuntime{err: errors.New("runtime leaked " + testPassword)}
		operations := newTestOperations(t, runtime)
		result, err := operations.CheckIntegrity(context.Background())
		if !errors.Is(err, ErrCommandFailed) || result != (IntegrityResult{}) {
			t.Fatalf("CheckIntegrity() = (%#v, %v), want zero result and ErrCommandFailed", result, err)
		}
		if strings.Contains(err.Error(), testPassword) || strings.Contains(err.Error(), "runtime leaked") {
			t.Fatalf("error was not sanitized: %v", err)
		}
	})

	t.Run("output overflow", func(t *testing.T) {
		runtime := &fakeRuntime{output: bytes.Repeat([]byte("x"), maxOutputBytes+1)}
		operations := newTestOperations(t, runtime)
		result, err := operations.CheckIntegrity(context.Background())
		if !errors.Is(err, ErrOutputLimit) || result != (IntegrityResult{}) {
			t.Fatalf("CheckIntegrity() = (%#v, %v), want zero result and ErrOutputLimit", result, err)
		}
	})
}

func TestRestoreBackupTransfersToFixedPathAndMovesEveryFile(t *testing.T) {
	runtime := &fakeRuntime{}
	runtime.exec = func(context.Context) ([]byte, error) {
		if runtime.calls == 1 {
			return []byte(backupHeaderRow(1, 1) + "\n"), nil
		}
		if runtime.calls == 2 {
			return []byte(backupFileListRow("primary", "D") + "\n" +
				backupFileListRow("secondary", "D") + "\n" +
				backupFileListRow("transaction-log", "L") + "\n"), nil
		}
		return []byte("restore output is intentionally discarded"), nil
	}
	operations := newTestOperations(t, runtime)

	if err := operations.RestoreBackup(context.Background(), io.NopCloser(strings.NewReader("backup")), "Example"); err != nil {
		t.Fatalf("RestoreBackup() error = %v", err)
	}
	if runtime.transferName != nativeBackupName || runtime.transferMax != maxNativeBackupBytes {
		t.Fatalf("Transfer(name, max) = (%q, %d), want fixed artifact and bounded size", runtime.transferName, runtime.transferMax)
	}
	if runtime.calls != 4 {
		t.Fatalf("ExecWithEnvironment() calls = %d, want HEADERONLY, FILELISTONLY, RESTORE, and cleanup", runtime.calls)
	}
	assertSQLCMDInvocation(t, runtime)
	headerQuery := runtime.commands[0][len(runtime.commands[0])-1]
	if !strings.HasPrefix(headerQuery, "RESTORE HEADERONLY FROM DISK = ") {
		t.Fatalf("first SQL command = %q, want HEADERONLY", headerQuery)
	}
	fileListQuery := runtime.commands[1][len(runtime.commands[1])-1]
	if !strings.Contains(fileListQuery, "RESTORE FILELISTONLY") || !strings.Contains(fileListQuery, "WITH FILE = 1") {
		t.Fatalf("second SQL command = %q, want FILELISTONLY for the validated first backup set", fileListQuery)
	}
	restoreQuery := runtime.commands[2][len(runtime.commands[2])-1]
	if !strings.HasPrefix(restoreQuery, "RESTORE DATABASE ["+operations.database+"] FROM DISK = N'"+nativeBackupPath+"'") {
		t.Fatalf("restore query is not bound to the run database and fixed backup: %q", restoreQuery)
	}
	if !strings.Contains(restoreQuery, "WITH FILE = 1") {
		t.Fatalf("restore query does not use the validated first backup set: %q", restoreQuery)
	}
	if !reflect.DeepEqual(runtime.commands[3], []string{"rm", "-f", "--", nativeBackupPath}) {
		t.Fatalf("backup cleanup command = %#v", runtime.commands[3])
	}
	expectedMoves := []string{
		"MOVE N'primary' TO N'/var/opt/mssql/data/" + operations.database + "_data_001.mdf'",
		"MOVE N'secondary' TO N'/var/opt/mssql/data/" + operations.database + "_data_002.ndf'",
		"MOVE N'transaction-log' TO N'/var/opt/mssql/data/" + operations.database + "_log_001.ldf'",
	}
	for _, move := range expectedMoves {
		if !strings.Contains(restoreQuery, move) {
			t.Errorf("restore query %q does not contain %q", restoreQuery, move)
		}
	}
	if !strings.Contains(strings.Join(runtime.command, "\x00"), nativeBackupPath) {
		t.Fatal("FILELISTONLY/RESTORE command did not use the fixed backup path")
	}
}

func TestRestoreBackupQuotesUntrustedLogicalNames(t *testing.T) {
	injectionLikeName := "primary'; DROP DATABASE master;--"
	files, err := parseBackupFileList([]byte(backupFileListRow(injectionLikeName, "D") + "\n" +
		backupFileListRow("log", "L") + "\n"))
	if err != nil {
		t.Fatalf("parseBackupFileList() error = %v", err)
	}
	query, err := restoreQuery("reksicherung_test", files)
	if err != nil {
		t.Fatalf("restoreQuery() error = %v", err)
	}
	if !strings.Contains(query, "MOVE N'primary''; DROP DATABASE master;--' TO ") {
		t.Fatalf("logical name was not escaped as a SQL string literal: %q", query)
	}
	if strings.Contains(query, "MOVE N'primary'; DROP") {
		t.Fatalf("logical name escaped its SQL string literal: %q", query)
	}
}

func TestParseBackupFileListRejectsMalformedUnsupportedAndMissingFiles(t *testing.T) {
	tests := map[string]string{
		"wrong column count":        backupFileListRow("data", "D") + "|extra\n" + backupFileListRow("log", "L"),
		"delimiter in logical name": backupFileListRow("data|extra", "D") + "\n" + backupFileListRow("log", "L"),
		"control in logical name":   backupFileListRow("data\tname", "D") + "\n" + backupFileListRow("log", "L"),
		"newline injection":         backupFileListRow("data\nDROP DATABASE master;--", "D") + "\n" + backupFileListRow("log", "L"),
		"unsupported file type":     backupFileListRow("data", "D") + "\n" + backupFileListRow("stream", "S") + "\n" + backupFileListRow("log", "L"),
		"missing data":              backupFileListRow("log", "L"),
		"missing log":               backupFileListRow("data", "D"),
		"duplicate logical name":    backupFileListRow("same", "D") + "\n" + backupFileListRow("SAME", "L"),
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseBackupFileList([]byte(output)); !errors.Is(err, ErrInvalidBackupMetadata) {
				t.Fatalf("parseBackupFileList() error = %v, want ErrInvalidBackupMetadata", err)
			}
		})
	}
}

func TestParseBackupFileListRejectsFileCountAndOutputOverflow(t *testing.T) {
	rows := make([]string, maxBackupFiles+1)
	for index := range rows {
		fileType := "D"
		if index == maxBackupFiles {
			fileType = "L"
		}
		rows[index] = backupFileListRow(fmt.Sprintf("file-%d", index), fileType)
	}
	if _, err := parseBackupFileList([]byte(strings.Join(rows, "\n"))); !errors.Is(err, ErrInvalidBackupMetadata) {
		t.Fatalf("parseBackupFileList(file overflow) error = %v, want ErrInvalidBackupMetadata", err)
	}
	if _, err := parseBackupFileList(bytes.Repeat([]byte("x"), maxOutputBytes+1)); !errors.Is(err, ErrInvalidBackupMetadata) {
		t.Fatalf("parseBackupFileList(output overflow) error = %v, want ErrInvalidBackupMetadata", err)
	}
}

func TestValidateSingleFullBackupSetRejectsAmbiguousOrUnsupportedHeaders(t *testing.T) {
	valid := backupHeaderRow(1, 1)
	tests := map[string]string{
		"valid full backup":     valid + "\n",
		"wrong column count":    "1|1\n",
		"non-first position":    backupHeaderRow(2, 1) + "\n",
		"not a database backup": backupHeaderRow(1, 2) + "\n",
		"multiple backup sets":  valid + "\n" + backupHeaderRow(2, 1) + "\n",
		"delimiter in metadata": strings.Replace(valid, "1|", "name|with|delimiter|", 1) + "\n",
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateSingleFullBackupSet([]byte(output), "Example")
			if name == "valid full backup" {
				if err != nil {
					t.Fatalf("validateSingleFullBackupSet() error = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidBackupMetadata) {
				t.Fatalf("validateSingleFullBackupSet() error = %v, want ErrInvalidBackupMetadata", err)
			}
		})
	}
	if err := validateSingleFullBackupSet([]byte(valid+"\n"), "DifferentDatabase"); !errors.Is(err, ErrInvalidBackupMetadata) {
		t.Fatalf("validateSingleFullBackupSet(database mismatch) error = %v, want ErrInvalidBackupMetadata", err)
	}
}

func TestRestoreBackupRejectsMultipleSetsAndAlwaysRemovesStagedArtifact(t *testing.T) {
	runtime := &fakeRuntime{}
	runtime.exec = func(context.Context) ([]byte, error) {
		if runtime.command[0] == sqlcmdPath {
			return []byte(backupHeaderRow(1, 1) + "\n" + backupHeaderRow(2, 1) + "\n"), nil
		}
		return nil, nil
	}
	operations := newTestOperations(t, runtime)
	err := operations.RestoreBackup(context.Background(), io.NopCloser(strings.NewReader("backup")), "Example")
	if !errors.Is(err, ErrInvalidBackupMetadata) {
		t.Fatalf("RestoreBackup() error = %v, want ErrInvalidBackupMetadata", err)
	}
	if runtime.calls != 2 || !reflect.DeepEqual(runtime.commands[1], []string{"rm", "-f", "--", nativeBackupPath}) {
		t.Fatalf("commands = %#v, want HEADERONLY and staged-artifact cleanup", runtime.commands)
	}
}

func TestRestoreBackupReportsStagedArtifactCleanupFailure(t *testing.T) {
	runtime := &fakeRuntime{}
	runtime.exec = func(context.Context) ([]byte, error) {
		if runtime.command[0] == "rm" {
			return nil, errors.New("cleanup detail is intentionally hidden")
		}
		switch runtime.calls {
		case 1:
			return []byte(backupHeaderRow(1, 1) + "\n"), nil
		case 2:
			return []byte(backupFileListRow("primary", "D") + "\n" +
				backupFileListRow("log", "L") + "\n"), nil
		default:
			return nil, nil
		}
	}
	operations := newTestOperations(t, runtime)
	err := operations.RestoreBackup(context.Background(), io.NopCloser(strings.NewReader("backup")), "Example")
	if !errors.Is(err, ErrBackupCleanupFailed) || strings.Contains(err.Error(), "cleanup detail") {
		t.Fatalf("RestoreBackup() error = %v, want sanitized cleanup error", err)
	}
}

func TestRestoreBackupSanitizesTransferAndCommandErrors(t *testing.T) {
	t.Run("transfer failure", func(t *testing.T) {
		runtime := &fakeRuntime{transferErr: errors.New("transfer leaked " + testPassword)}
		operations := newTestOperations(t, runtime)
		err := operations.RestoreBackup(context.Background(), io.NopCloser(strings.NewReader("backup")), "Example")
		if !errors.Is(err, ErrBackupTransferFailed) || strings.Contains(err.Error(), testPassword) {
			t.Fatalf("RestoreBackup() error = %v, want sanitized transfer failure", err)
		}
		if runtime.calls != 0 {
			t.Fatalf("commands = %d after transfer failure, want 0", runtime.calls)
		}
	})
	t.Run("sqlcmd failure", func(t *testing.T) {
		runtime := &fakeRuntime{err: errors.New("SQL output leaked " + testPassword)}
		operations := newTestOperations(t, runtime)
		err := operations.RestoreBackup(context.Background(), io.NopCloser(strings.NewReader("backup")), "Example")
		if !errors.Is(err, ErrCommandFailed) || strings.Contains(err.Error(), testPassword) ||
			strings.Contains(err.Error(), "SQL output leaked") {
			t.Fatalf("RestoreBackup() error = %v, want sanitized command failure", err)
		}
	})
	t.Run("FILELISTONLY output overflow", func(t *testing.T) {
		runtime := &fakeRuntime{}
		runtime.exec = func(context.Context) ([]byte, error) {
			if runtime.calls == 1 {
				return []byte(backupHeaderRow(1, 1) + "\n"), nil
			}
			return bytes.Repeat([]byte("x"), maxOutputBytes+1), nil
		}
		operations := newTestOperations(t, runtime)
		err := operations.RestoreBackup(context.Background(), io.NopCloser(strings.NewReader("backup")), "Example")
		if !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("RestoreBackup() error = %v, want sanitized output limit", err)
		}
	})
}

func TestRestoreBackupHonorsTimeoutAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		want error
	}{
		{name: "deadline", want: ErrTimeout},
		{name: "cancellation", want: ErrCanceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &fakeRuntime{}
			started := make(chan struct{})
			runtime.exec = func(ctx context.Context) ([]byte, error) {
				if len(runtime.command) == 0 || runtime.command[0] != sqlcmdPath {
					return nil, nil
				}
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			operations := newTestOperations(t, runtime)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.name == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
				defer deadlineCancel()
			}
			result := make(chan error, 1)
			go func() {
				result <- operations.RestoreBackup(ctx, io.NopCloser(strings.NewReader("backup")), "Example")
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("FILELISTONLY command did not start")
			}
			if test.name == "cancellation" {
				cancel()
			}
			select {
			case err := <-result:
				if !errors.Is(err, test.want) {
					t.Fatalf("RestoreBackup() error = %v, want %v", err, test.want)
				}
			case <-time.After(time.Second):
				t.Fatal("RestoreBackup() did not stop after context ended")
			}
		})
	}
}

func backupFileListRow(logicalName, fileType string) string {
	columns := make([]string, fileListColumns)
	columns[0] = logicalName
	columns[2] = fileType
	return strings.Join(columns, "|")
}

func backupHeaderRow(position, backupType int) string {
	columns := make([]string, headerOnlyColumns)
	columns[2] = fmt.Sprint(backupType)
	columns[5] = fmt.Sprint(position)
	columns[9] = "Example"
	return strings.Join(columns, "|")
}

func assertSQLCMDInvocation(t *testing.T, runtime *fakeRuntime) {
	t.Helper()
	found := false
	for index, command := range runtime.commands {
		if len(command) == 0 || command[0] != sqlcmdPath {
			continue
		}
		found = true
		if !containsArgSequence(command, "-b", "-C", "-S", "localhost", "-U", "sa", "-d", "master") ||
			!containsArgSequence(command, "-Q") {
			t.Fatalf("command does not contain fixed SQLCMD options: %#v", command)
		}
		if len(runtime.environments[index]) != 1 || runtime.environments[index][0].Name != "SQLCMDPASSWORD" ||
			runtime.environments[index][0].Value != testPassword {
			t.Fatalf("SQLCMD environment = %#v, want only SQLCMDPASSWORD", runtime.environments[index])
		}
	}
	if !found {
		t.Fatalf("commands = %#v, want at least one sqlcmd call", runtime.commands)
	}
	if runtime.target != testContainer() {
		t.Fatalf("target = %#v, want managed target", runtime.target)
	}
}

func containsArgSequence(args []string, expected ...string) bool {
	for start := 0; start+len(expected) <= len(args); start++ {
		matches := true
		for offset := range expected {
			if args[start+offset] != expected[offset] {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
