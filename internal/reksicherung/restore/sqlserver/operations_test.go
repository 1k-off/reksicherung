package sqlserver

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

const testPassword = "test-secret-password"

type fakeRuntime struct {
	command     []string
	target      container.Container
	environment []container.EnvironmentVariable
	ctx         context.Context
	output      []byte
	err         error
	calls       int
	exec        func(context.Context) ([]byte, error)
}

func (runtime *fakeRuntime) ExecWithEnvironment(ctx context.Context, target container.Container, command []string, environment []container.EnvironmentVariable) ([]byte, error) {
	runtime.calls++
	runtime.command = append([]string(nil), command...)
	runtime.target = target
	runtime.environment = append([]container.EnvironmentVariable(nil), environment...)
	runtime.ctx = ctx
	if runtime.exec != nil {
		return runtime.exec(ctx)
	}
	return runtime.output, runtime.err
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
	if result != (IntegrityResult{Database: database, Passed: true}) {
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

func assertSQLCMDInvocation(t *testing.T, runtime *fakeRuntime) {
	t.Helper()
	if len(runtime.command) < 2 || runtime.command[0] != sqlcmdPath {
		t.Fatalf("command = %#v, want sqlcmd at %s", runtime.command, sqlcmdPath)
	}
	if !containsArgSequence(runtime.command, "-b", "-C", "-S", "localhost", "-U", "sa", "-d", "master", "-Q") {
		t.Fatalf("command does not contain fixed SQLCMD options: %#v", runtime.command)
	}
	if runtime.target != testContainer() {
		t.Fatalf("target = %#v, want managed target", runtime.target)
	}
	if len(runtime.environment) != 1 || runtime.environment[0].Name != "SQLCMDPASSWORD" {
		t.Fatalf("environment = %#v, want SQLCMDPASSWORD", runtime.environment)
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
