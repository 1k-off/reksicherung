package gobackup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	sqlserverrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/sqlserver"
	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

func TestImporterStreamsAndImportsBACPAC(t *testing.T) {
	runtime := &fakeImportRuntime{}
	importer := newTestImporter(t, runtime)
	password := `p@ss"'$word`
	archive := makeArchive(t, []tarEntry{{name: "nested/db.bacpac", body: "bacpac bytes"}}, FormatTarGzip)

	if err := importer.Import(context.Background(), testContainer(), io.NopCloser(bytes.NewReader(archive)), FormatTarGzip, ImportOptions{
		Database: testDatabase(t),
		Username: "restore_user",
		Password: password,
	}); err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	if runtime.artifactName != artifactName || string(runtime.artifact) != "bacpac bytes" {
		t.Fatalf("transferred artifact = (%q, %q), want (%q, %q)", runtime.artifactName, runtime.artifact, artifactName, "bacpac bytes")
	}
	if len(runtime.execCommands) != 1 || len(runtime.execEnvironment) != 4 {
		t.Fatalf("SqlPackage calls = %d, environment variables = %d; want 1 and 4", len(runtime.execCommands), len(runtime.execEnvironment))
	}
	command := runtime.execCommands[0]
	if len(command) != 3 || command[0] != "sh" || command[1] != "-c" || command[2] != sqlPackageImportScript {
		t.Fatalf("SqlPackage command = %#v, want fixed script invocation", command)
	}
	if strings.Contains(strings.Join(command, " "), password) {
		t.Fatal("password appeared in command arguments")
	}
	gotEnvironment := make(map[string]string, len(runtime.execEnvironment))
	for _, variable := range runtime.execEnvironment {
		gotEnvironment[variable.Name] = variable.Value
	}
	timeoutSeconds, timeoutErr := strconv.ParseFloat(gotEnvironment["REKSICHERUNG_SQLPACKAGE_TIMEOUT"], 64)
	if gotEnvironment["REKSICHERUNG_DB_NAME"] != testDatabase(t) ||
		gotEnvironment["REKSICHERUNG_DB_USER"] != "restore_user" ||
		gotEnvironment["REKSICHERUNG_DB_PASSWORD"] != password ||
		timeoutErr != nil || timeoutSeconds <= 0 || timeoutSeconds > 60 {
		t.Fatalf("SqlPackage environment = %#v, does not contain expected values", gotEnvironment)
	}
	if len(runtime.cleanupCommands) != 1 || strings.Join(runtime.cleanupCommands[0], " ") != "rm -f -- "+artifactPath {
		t.Fatalf("artifact cleanup command = %#v", runtime.cleanupCommands)
	}
}

func TestImporterRejectsUnsafeConnectionParametersBeforeTransfer(t *testing.T) {
	tests := []struct {
		name    string
		options ImportOptions
	}{
		{name: "database option injection", options: ImportOptions{Database: "db /Action:Export", Username: "restore", Password: "secret"}},
		{name: "database over SQL Server limit", options: ImportOptions{Database: strings.Repeat("a", 129), Username: "restore", Password: "secret"}},
		{name: "username option injection", options: ImportOptions{Database: testDatabase(t), Username: `user"; touch /tmp/no`, Password: "secret"}},
		{name: "empty password", options: ImportOptions{Database: testDatabase(t), Username: "restore"}},
		{name: "password newline", options: ImportOptions{Database: testDatabase(t), Username: "restore", Password: "secret\nvalue"}},
		{name: "password too long", options: ImportOptions{Database: testDatabase(t), Username: "restore", Password: strings.Repeat("x", 4097)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &fakeImportRuntime{}
			importer := newTestImporter(t, runtime)
			err := importer.Import(context.Background(), testContainer(), io.NopCloser(strings.NewReader("unused")), FormatTar, test.options)
			if err == nil {
				t.Fatal("Import() error = nil, want validation failure")
			}
			if runtime.transfers != 0 || len(runtime.execCommands) != 0 {
				t.Fatalf("runtime called before validation: transfers=%d imports=%d", runtime.transfers, len(runtime.execCommands))
			}
		})
	}
}

func TestImporterRejectsDatabaseOutsideRestoreRun(t *testing.T) {
	runtime := &fakeImportRuntime{}
	importer := newTestImporter(t, runtime)
	err := importer.Import(
		context.Background(),
		testContainer(),
		io.NopCloser(strings.NewReader("unused")),
		FormatTar,
		ImportOptions{Database: "master", Username: "restore", Password: "secret"},
	)
	if err == nil || err.Error() != "GoBackup import requires the database bound to the restore run" {
		t.Fatalf("Import() error = %v, want run-bound database rejection", err)
	}
	if runtime.transfers != 0 || len(runtime.execCommands) != 0 {
		t.Fatalf("runtime called for unowned database: transfers=%d imports=%d", runtime.transfers, len(runtime.execCommands))
	}
}

func TestImporterSanitizesSqlPackageFailure(t *testing.T) {
	runtime := &fakeImportRuntime{
		execErr: errors.New("failed with secret value"),
		output:  []byte("password echoed by SqlPackage"),
	}
	importer := newTestImporter(t, runtime)
	err := importer.Import(context.Background(), testContainer(), io.NopCloser(bytes.NewReader(
		makeArchive(t, []tarEntry{{name: "db.bacpac", body: "payload"}}, FormatTar),
	)), FormatTar, ImportOptions{Database: testDatabase(t), Username: "restore", Password: "secret value"})
	if err == nil || err.Error() != "SqlPackage BACPAC import failed" {
		t.Fatalf("Import() error = %v, want sanitized SqlPackage failure", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "echoed") {
		t.Fatalf("Import() exposed tool details: %v", err)
	}
	if len(runtime.cleanupCommands) != 1 {
		t.Fatalf("cleanup calls = %d, want one after tool failure", len(runtime.cleanupCommands))
	}
}

func TestImporterSanitizesArtifactCleanupFailure(t *testing.T) {
	const sentinel = "private-storage-credential"
	runtime := &fakeImportRuntime{cleanupErr: errors.New("podman detail " + sentinel)}
	importer := newTestImporter(t, runtime)
	err := importer.Import(context.Background(), testContainer(), io.NopCloser(bytes.NewReader(
		makeArchive(t, []tarEntry{{name: "db.bacpac", body: "payload"}}, FormatTar),
	)), FormatTar, ImportOptions{Database: testDatabase(t), Username: "restore", Password: "secret"})
	if err == nil || !strings.Contains(err.Error(), "remove BACPAC artifact") {
		t.Fatalf("Import() error = %v, want sanitized artifact cleanup failure", err)
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("Import() leaked cleanup details: %v", err)
	}
}

func TestImporterRejectsOversizedSqlPackageOutput(t *testing.T) {
	runtime := &fakeImportRuntime{output: bytes.Repeat([]byte("x"), maxSqlPackageOutput+1)}
	importer := newTestImporter(t, runtime)
	err := importer.Import(context.Background(), testContainer(), io.NopCloser(bytes.NewReader(
		makeArchive(t, []tarEntry{{name: "db.bacpac", body: "payload"}}, FormatTar),
	)), FormatTar, ImportOptions{Database: testDatabase(t), Username: "restore", Password: "secret"})
	if err == nil || err.Error() != "SqlPackage BACPAC import failed" {
		t.Fatalf("Import() error = %v, want sanitized output-limit failure", err)
	}
	if len(runtime.cleanupCommands) != 1 {
		t.Fatalf("cleanup calls = %d, want one after output-limit failure", len(runtime.cleanupCommands))
	}
}

func TestImporterUsesBoundedTimeout(t *testing.T) {
	runtime := &fakeImportRuntime{waitForImportCancellation: true}
	importer, err := NewImporter(runtime, testLimits, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("NewImporter() error = %v", err)
	}
	err = importer.Import(context.Background(), testContainer(), io.NopCloser(bytes.NewReader(
		makeArchive(t, []tarEntry{{name: "db.bacpac", body: "payload"}}, FormatTar),
	)), FormatTar, ImportOptions{Database: testDatabase(t), Username: "restore", Password: "secret"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Import() error = %v, want context.DeadlineExceeded", err)
	}
	if len(runtime.cleanupCommands) != 1 {
		t.Fatalf("cleanup calls = %d, want one after timeout", len(runtime.cleanupCommands))
	}
}

func TestImporterDoesNotInvokeSqlPackageForInvalidArchive(t *testing.T) {
	runtime := &fakeImportRuntime{}
	importer := newTestImporter(t, runtime)
	err := importer.Import(context.Background(), testContainer(), io.NopCloser(bytes.NewReader(
		makeArchive(t, []tarEntry{{name: "../outside.bacpac", body: "payload"}}, FormatTar),
	)), FormatTar, ImportOptions{Database: testDatabase(t), Username: "restore", Password: "secret"})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Import() error = %v, want ErrUnsafePath", err)
	}
	if len(runtime.execCommands) != 0 {
		t.Fatalf("SqlPackage calls = %d, want zero", len(runtime.execCommands))
	}
	if len(runtime.cleanupCommands) != 1 {
		t.Fatalf("artifact cleanup calls = %d, want one after transfer failure", len(runtime.cleanupCommands))
	}
}

func newTestImporter(t *testing.T, runtime Runtime) *Importer {
	t.Helper()
	importer, err := NewImporter(runtime, testLimits, time.Minute)
	if err != nil {
		t.Fatalf("NewImporter() error = %v", err)
	}
	return importer
}

func testContainer() container.Container {
	return container.Container{ID: "owned-container", Name: "reksicherung-test", RunID: "test"}
}

func testDatabase(t *testing.T) string {
	t.Helper()
	database, err := sqlserverrestore.DatabaseNameForRun(testContainer().RunID)
	if err != nil {
		t.Fatalf("DatabaseNameForRun() error = %v", err)
	}
	return database
}

type fakeImportRuntime struct {
	artifactName              string
	artifact                  []byte
	transfers                 int
	execCommands              [][]string
	execEnvironment           []container.EnvironmentVariable
	cleanupCommands           [][]string
	execErr                   error
	cleanupErr                error
	output                    []byte
	waitForImportCancellation bool
}

func (runtime *fakeImportRuntime) Transfer(ctx context.Context, _ container.Container, name string, source io.ReadCloser, _ int64) error {
	runtime.transfers++
	defer source.Close()
	runtime.artifactName = name
	data, err := io.ReadAll(source)
	runtime.artifact = data
	return err
}

func (runtime *fakeImportRuntime) Exec(_ context.Context, _ container.Container, command []string) ([]byte, error) {
	runtime.cleanupCommands = append(runtime.cleanupCommands, append([]string(nil), command...))
	return nil, runtime.cleanupErr
}

func (runtime *fakeImportRuntime) ExecWithEnvironment(ctx context.Context, _ container.Container, command []string, environment []container.EnvironmentVariable) ([]byte, error) {
	runtime.execCommands = append(runtime.execCommands, append([]string(nil), command...))
	runtime.execEnvironment = append([]container.EnvironmentVariable(nil), environment...)
	if runtime.waitForImportCancellation {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return runtime.output, runtime.execErr
}
