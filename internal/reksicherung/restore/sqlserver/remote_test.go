package sqlserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPassword = "test-secret-password"

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

func TestRemoteOperationsRestoreOnExistingSQLServerAndCleansSharedArtifact(t *testing.T) {
	backupDir := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "sql-data")
	runner := &remoteTestRunner{}
	runner.responses = [][]byte{
		[]byte(backupHeaderRow(1, 1) + "\n"),
		[]byte(backupFileListRow("primary", "D") + "\n" + backupFileListRow("log", "L") + "\n"),
		nil,
	}
	operations, err := NewRemote(runner, remoteTestConfig(backupDir, dataDir))
	if err != nil {
		t.Fatalf("NewRemote() error = %v", err)
	}

	runID := "airon-run-123"
	database, err := DatabaseNameForRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	err = operations.RestoreBackup(context.Background(), runID,
		io.NopCloser(strings.NewReader("native backup bytes")), "Example")
	if err != nil {
		t.Fatalf("RestoreBackup() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(backupDir, database+".bak")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged backup remains after restore: %v", err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("SQL commands = %d, want HEADERONLY, FILELISTONLY, RESTORE", len(runner.calls))
	}
	for _, call := range runner.calls {
		if !containsArgSequence(call.args, "-S", "sqlserver:1433", "-U", "restore_login", "-d", "master") {
			t.Errorf("command not scoped to configured service and master database: %#v", call.args)
		}
		if containsArgSequence(call.args, "-C") {
			t.Errorf("command disabled certificate verification: %#v", call.args)
		}
		if strings.Contains(strings.Join(call.args, " "), testPassword) {
			t.Errorf("password appeared in command arguments: %#v", call.args)
		}
		if len(call.environment) != 1 || call.environment[0] != "SQLCMDPASSWORD="+testPassword {
			t.Errorf("command environment = %#v, want SQLCMDPASSWORD only", call.environment)
		}
	}
	restoreQuery := runner.calls[2].args[len(runner.calls[2].args)-1]
	if !strings.Contains(restoreQuery, "RESTORE DATABASE ["+database+"]") ||
		!strings.Contains(restoreQuery, filepath.Join(dataDir, database+"_01.mdf")) ||
		!strings.Contains(restoreQuery, filepath.Join(backupDir, database+".bak")) {
		t.Fatalf("restore query not bound to run-scoped DB and configured shared volume: %s", restoreQuery)
	}
}

func TestRemoteOperationsDerivesOnlyRunScopedCleanupAndIntegrity(t *testing.T) {
	runner := &remoteTestRunner{responses: [][]byte{nil, nil}}
	operations, err := NewRemote(runner, remoteTestConfig(t.TempDir(), t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("commands = %d, want integrity and cleanup", len(runner.calls))
	}
	if !strings.Contains(runner.calls[0].args[len(runner.calls[0].args)-1], "DBCC CHECKDB (["+database+"])") {
		t.Fatalf("integrity query not scoped to derived database: %#v", runner.calls[0].args)
	}
	if !strings.Contains(runner.calls[1].args[len(runner.calls[1].args)-1], "DROP DATABASE ["+database+"]") {
		t.Fatalf("cleanup query not scoped to derived database: %#v", runner.calls[1].args)
	}
}

func TestRemoteOperationsRejectsUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name string
		edit func(*RemoteConfig)
	}{
		{name: "relative backup directory", edit: func(config *RemoteConfig) { config.BackupDirectory = "../shared" }},
		{name: "relative data directory", edit: func(config *RemoteConfig) { config.DataDirectory = "../data" }},
		{name: "option as server", edit: func(config *RemoteConfig) { config.Server = "-Q SELECT 1" }},
		{name: "empty password", edit: func(config *RemoteConfig) { config.Password = "" }},
		{name: "overlarge backup limit", edit: func(config *RemoteConfig) { config.MaxBackupBytes = maxNativeBackupBytes + 1 }},
		{name: "unbounded timeout", edit: func(config *RemoteConfig) { config.Timeout = 25 * time.Hour }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := remoteTestConfig(t.TempDir(), t.TempDir())
			test.edit(&config)
			if _, err := NewRemote(&remoteTestRunner{}, config); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("NewRemote() error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestRemoteOperationsImportBACPACUsesResponseFileAndCleansArtifacts(t *testing.T) {
	backupDir := t.TempDir()
	runner := &remoteTestRunner{responses: [][]byte{nil}}
	operations, err := NewRemote(runner, remoteTestConfig(backupDir, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	runID := "bacpac-run-1"
	database, err := DatabaseNameForRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.ImportBACPAC(context.Background(), runID, io.NopCloser(strings.NewReader("bacpac-bytes"))); err != nil {
		t.Fatalf("ImportBACPAC() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(backupDir, database+".bacpac")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged bacpac remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(backupDir, database+".sqlpackage.rsp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("response file remains: %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0].binary != "/usr/bin/sqlpackage" {
		t.Fatalf("sqlpackage calls = %#v", runner.calls)
	}
	if len(runner.calls[0].args) != 1 || !strings.HasPrefix(runner.calls[0].args[0], "@") {
		t.Fatalf("sqlpackage args = %#v, want response-file invocation", runner.calls[0].args)
	}
	for _, arg := range runner.calls[0].args {
		if strings.Contains(arg, testPassword) {
			t.Fatalf("password leaked into argv: %#v", runner.calls[0].args)
		}
	}
}

func TestRemoteOperationsRejectsSourceMismatchAndRemovesTemporaryFiles(t *testing.T) {
	backupDir := t.TempDir()
	runner := &remoteTestRunner{responses: [][]byte{[]byte(backupHeaderRow(1, 1) + "\n")}}
	operations, err := NewRemote(runner, remoteTestConfig(backupDir, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	err = operations.RestoreBackup(context.Background(), "source-check",
		io.NopCloser(strings.NewReader("native backup bytes")), "DifferentDatabase")
	if !errors.Is(err, ErrInvalidBackupMetadata) {
		t.Fatalf("RestoreBackup() error = %v, want source-database mismatch", err)
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staged artifacts remain after validation failure: %#v", entries)
	}
}

type remoteTestCall struct {
	binary      string
	args        []string
	environment []string
}

type remoteTestRunner struct {
	calls     []remoteTestCall
	responses [][]byte
	err       error
}

func (runner *remoteTestRunner) Run(_ context.Context, binary string, args, environment []string, maxOutput int) ([]byte, error) {
	runner.calls = append(runner.calls, remoteTestCall{
		binary: binary, args: append([]string(nil), args...), environment: append([]string(nil), environment...),
	})
	if runner.err != nil {
		return nil, runner.err
	}
	if len(runner.responses) == 0 {
		return nil, nil
	}
	response := runner.responses[0]
	runner.responses = runner.responses[1:]
	if len(response) > maxOutput {
		return nil, ErrOutputLimit
	}
	return response, nil
}

func remoteTestConfig(backupDir, dataDir string) RemoteConfig {
	return RemoteConfig{
		Server:          "sqlserver:1433",
		Username:        "restore_login",
		Password:        testPassword,
		BackupDirectory: backupDir,
		DataDirectory:   dataDir,
		SQLCmd:          "/usr/bin/sqlcmd",
		SQLPackage:      "/usr/bin/sqlpackage",
		Timeout:         time.Minute,
		MaxBackupBytes:  1024,
	}
}
