package mysql

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testPassword = `pa"ss#word\1`

type remoteCall struct {
	binary   string
	args     []string
	stdin    string
	defaults string
	mode     os.FileMode
}

type fakeRunner struct {
	calls []remoteCall
	err   error
	errAt int // 1-based call index that fails; 0 = every call when err set
}

func (runner *fakeRunner) Run(_ context.Context, binary string, args []string, stdin io.Reader, _ int) ([]byte, error) {
	call := remoteCall{binary: binary, args: append([]string(nil), args...)}
	if len(args) > 0 && strings.HasPrefix(args[0], "--defaults-extra-file=") {
		path := strings.TrimPrefix(args[0], "--defaults-extra-file=")
		if data, err := os.ReadFile(path); err == nil {
			call.defaults = string(data)
		}
		if info, err := os.Stat(path); err == nil {
			call.mode = info.Mode().Perm()
		}
	}
	var readErr error
	if stdin != nil {
		data, err := io.ReadAll(stdin)
		call.stdin, readErr = string(data), err
	}
	runner.calls = append(runner.calls, call)
	if readErr != nil {
		return nil, readErr
	}
	if runner.err != nil && (runner.errAt == 0 || runner.errAt == len(runner.calls)) {
		return nil, runner.err
	}
	return nil, nil
}

func testConfig(dir string) RemoteConfig {
	return RemoteConfig{
		Host: "mysql", Port: 3306, Username: "restore", Password: testPassword,
		MySQL: "/usr/bin/mysql", MySQLCheck: "/usr/bin/mysqlcheck",
		TempDir: dir, Timeout: time.Minute, MaxDumpBytes: 1 << 20,
	}
}

func assertNoPassword(t *testing.T, call remoteCall) {
	t.Helper()
	for _, arg := range call.args {
		if strings.Contains(arg, testPassword) || strings.Contains(arg, "password") {
			t.Errorf("password flag/value in argv: %#v", call.args)
		}
	}
	if len(call.args) == 0 || !strings.HasPrefix(call.args[0], "--defaults-extra-file=") {
		t.Errorf("argv must start with --defaults-extra-file: %#v", call.args)
	}
	if !strings.Contains(call.defaults, "[client]\n") || !strings.Contains(call.defaults, "user=\"restore\"\n") ||
		!strings.Contains(call.defaults, `password="pa\"ss#word\\1"`) ||
		!strings.Contains(call.defaults, "host=\"mysql\"\n") || !strings.Contains(call.defaults, "port=3306\n") {
		t.Errorf("defaults file content = %q", call.defaults)
	}
	if runtime.GOOS != "windows" && call.mode != 0600 {
		t.Errorf("defaults file mode = %v, want 0600", call.mode)
	}
}

func TestRestoreDumpCreatesImportsAndRemovesCredentialFile(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{}
	operations, err := NewRemote(runner, testConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	const runID = "run-123"
	database, _ := DatabaseNameForRun(runID)
	dump := io.NopCloser(strings.NewReader("CREATE DATABASE `app`;\nUSE `app`;\nCREATE TABLE t(i INT);\n"))
	if err := operations.RestoreDump(context.Background(), runID, dump, "app"); err != nil {
		t.Fatalf("RestoreDump() error = %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d, want create + import", len(runner.calls))
	}
	for _, call := range runner.calls {
		if call.binary != "/usr/bin/mysql" {
			t.Errorf("binary = %s", call.binary)
		}
		assertNoPassword(t, call)
	}
	create := runner.calls[0]
	if !containsArg(create.args, "-e") || create.args[len(create.args)-1] != "CREATE DATABASE `"+database+"`" {
		t.Fatalf("create argv = %#v", create.args)
	}
	imp := runner.calls[1]
	if imp.args[len(imp.args)-1] != database || !containsArg(imp.args, "--binary-mode") ||
		!containsArg(imp.args, "--local-infile=0") {
		t.Fatalf("import argv = %#v", imp.args)
	}
	if strings.Contains(imp.stdin, "`app`") || !strings.Contains(imp.stdin, "USE `"+database+"`;") ||
		!strings.Contains(imp.stdin, "CREATE TABLE t(i INT);") {
		t.Fatalf("import stdin = %q", imp.stdin)
	}
	assertDirEmpty(t, dir)
}

func TestRestoreDumpRejectsMultiDatabaseDumpBeforeCreatingDatabase(t *testing.T) {
	runner := &fakeRunner{}
	dir := t.TempDir()
	operations, _ := NewRemote(runner, testConfig(dir))
	file, err := os.Create(dir + "/dump.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("CREATE DATABASE `app`;\nCREATE DATABASE `other`;\n")
	_, _ = file.Seek(0, io.SeekStart)
	// Keep the dump file out of the credential-file directory check.
	err = operations.RestoreDump(context.Background(), "run-1", file, "app")
	if !errors.Is(err, ErrInvalidDump) {
		t.Fatalf("err = %v, want ErrInvalidDump", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("mysql ran despite invalid seekable dump: %#v", runner.calls)
	}
}

func TestRestoreDumpRejectsNonSeekableInvalidDumpWithoutLeakingOtherDatabase(t *testing.T) {
	runner := &fakeRunner{}
	operations, _ := NewRemote(runner, testConfig(t.TempDir()))
	dump := io.NopCloser(strings.NewReader("CREATE DATABASE `app`;\nCREATE DATABASE `other`;\n"))
	err := operations.RestoreDump(context.Background(), "run-1", dump, "app")
	if !errors.Is(err, ErrInvalidDump) {
		t.Fatalf("err = %v, want ErrInvalidDump", err)
	}
	for _, call := range runner.calls {
		if strings.Contains(call.stdin, "other") {
			t.Fatalf("foreign database statement reached mysql: %q", call.stdin)
		}
	}
}

func TestRestoreDumpCommandFailureIsSanitized(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{err: errors.New("access denied for password " + testPassword)}
	operations, _ := NewRemote(runner, testConfig(dir))
	err := operations.RestoreDump(context.Background(), "run-1", io.NopCloser(strings.NewReader("SELECT 1;\n")), "app")
	if !errors.Is(err, ErrCommandFailed) || strings.Contains(err.Error(), "password") {
		t.Fatalf("err = %v", err)
	}
	assertDirEmpty(t, dir)
}

func TestIntegrityAndDropAreRunScoped(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{}
	operations, _ := NewRemote(runner, testConfig(dir))
	const runID = "run-cleanup-1"
	database, _ := DatabaseNameForRun(runID)
	result, err := operations.CheckIntegrity(context.Background(), runID)
	if err != nil || !result.CommandCompleted || result.Database != database {
		t.Fatalf("CheckIntegrity() = %#v, %v", result, err)
	}
	if err := operations.DropDatabase(context.Background(), runID); err != nil {
		t.Fatalf("DropDatabase() error = %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
	check := runner.calls[0]
	if check.binary != "/usr/bin/mysqlcheck" || !containsArg(check.args, "--check") || check.args[len(check.args)-1] != database {
		t.Fatalf("integrity argv = %#v", check.args)
	}
	drop := runner.calls[1]
	if drop.binary != "/usr/bin/mysql" || drop.args[len(drop.args)-1] != "DROP DATABASE IF EXISTS `"+database+"`" {
		t.Fatalf("drop argv = %#v", drop.args)
	}
	assertNoPassword(t, check)
	assertNoPassword(t, drop)
	assertDirEmpty(t, dir)
}

func TestIntegrityFailureIsNotCompleted(t *testing.T) {
	operations, _ := NewRemote(&fakeRunner{err: errors.New("boom")}, testConfig(t.TempDir()))
	result, err := operations.CheckIntegrity(context.Background(), "run-1")
	if !errors.Is(err, ErrCommandFailed) || result.CommandCompleted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestInvalidRunIDRejected(t *testing.T) {
	runner := &fakeRunner{}
	operations, _ := NewRemote(runner, testConfig(t.TempDir()))
	if err := operations.DropDatabase(context.Background(), "../mysql"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err=%v", err)
	}
	if _, err := operations.CheckIntegrity(context.Background(), ""); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("runner invoked for invalid run id")
	}
}

func TestNewRemoteRejectsUnsafeConfiguration(t *testing.T) {
	tests := map[string]func(*RemoteConfig){
		"option host":       func(c *RemoteConfig) { c.Host = "-h evil" },
		"empty password":    func(c *RemoteConfig) { c.Password = "" },
		"newline password":  func(c *RemoteConfig) { c.Password = "a\nb" },
		"bad port":          func(c *RemoteConfig) { c.Port = 70000 },
		"option username":   func(c *RemoteConfig) { c.Username = "-u" },
		"unbounded timeout": func(c *RemoteConfig) { c.Timeout = 25 * time.Hour },
		"oversized limit":   func(c *RemoteConfig) { c.MaxDumpBytes = maxDumpBytes + 1 },
	}
	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			config := testConfig(t.TempDir())
			edit(&config)
			if _, err := NewRemote(&fakeRunner{}, config); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("credential files remain: %v", entries)
	}
}
