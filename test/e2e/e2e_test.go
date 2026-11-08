//go:build e2e

package e2e_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/microsoft/go-mssqldb"

	sqlserver "github.com/1k-off/reksicherung/internal/reksicherung/restore/sqlserver"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/1k-off/reksicherung/internal/reksicherung/runregistry"
)

// Release-gate E2E against operator-started services.
// Required env:
//   REKSICHERUNG_E2E_SQLSERVER_ADDRESS   e.g. tcp:127.0.0.1,1433
//   REKSICHERUNG_E2E_SQLSERVER_USER
//   REKSICHERUNG_E2E_SQLSERVER_PASSWORD
//   REKSICHERUNG_E2E_BACKUP_DIR          absolute shared backup directory
//   REKSICHERUNG_E2E_DATA_DIR            absolute SQL data directory as seen by SQL Server
//   REKSICHERUNG_E2E_BAK_FIXTURE         path to a synthetic .bak whose header DB name is Example
func TestNativeBackupRestoreIntegrityAndCleanup(t *testing.T) {
	address := requiredEnv(t, "REKSICHERUNG_E2E_SQLSERVER_ADDRESS")
	user := requiredEnv(t, "REKSICHERUNG_E2E_SQLSERVER_USER")
	password := requiredEnv(t, "REKSICHERUNG_E2E_SQLSERVER_PASSWORD")
	backupDir := requiredEnv(t, "REKSICHERUNG_E2E_BACKUP_DIR")
	dataDir := requiredEnv(t, "REKSICHERUNG_E2E_DATA_DIR")
	fixture := requiredEnv(t, "REKSICHERUNG_E2E_BAK_FIXTURE")

	stageDir := t.TempDir()
	store, err := staging.New(stageDir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Save(context.Background(), source, staging.KindBAK)
	if err != nil {
		t.Fatalf("stage fixture: %v", err)
	}

	operations, err := sqlserver.NewRemote(nil, sqlserver.RemoteConfig{
		Server:          address,
		Username:        user,
		Password:        password,
		BackupDirectory: backupDir,
		DataDirectory:   dataDir,
		SQLCmd:          envOr("REKSICHERUNG_E2E_SQLCMD", "sqlcmd"),
		Timeout:         30 * time.Minute,
		MaxBackupBytes:  1 << 30,
	})
	if err != nil {
		t.Fatal(err)
	}

	runID := fmt.Sprintf("e2e-%d", time.Now().Unix())
	database, err := sqlserver.DatabaseNameForRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = operations.DropDatabase(context.Background(), runID)
	})

	file, _, err := store.Open(artifact.ID, staging.KindBAK)
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.RestoreBackup(context.Background(), runID, file, "Example"); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	result, err := operations.CheckIntegrity(context.Background(), runID)
	if err != nil || !result.CommandCompleted || result.Database != database {
		t.Fatalf("CheckIntegrity = %#v, %v", result, err)
	}
	if err := operations.DropDatabase(context.Background(), runID); err != nil {
		t.Fatalf("DropDatabase: %v", err)
	}
	if exists, err := databaseExists(address, user, password, database); err != nil || exists {
		t.Fatalf("database still present after cleanup: exists=%v err=%v", exists, err)
	}
}

func TestRegistryCleanupHeldDropsDatabase(t *testing.T) {
	address := requiredEnv(t, "REKSICHERUNG_E2E_SQLSERVER_ADDRESS")
	user := requiredEnv(t, "REKSICHERUNG_E2E_SQLSERVER_USER")
	password := requiredEnv(t, "REKSICHERUNG_E2E_SQLSERVER_PASSWORD")
	backupDir := requiredEnv(t, "REKSICHERUNG_E2E_BACKUP_DIR")
	dataDir := requiredEnv(t, "REKSICHERUNG_E2E_DATA_DIR")

	operations, err := sqlserver.NewRemote(nil, sqlserver.RemoteConfig{
		Server:          address,
		Username:        user,
		Password:        password,
		BackupDirectory: backupDir,
		DataDirectory:   dataDir,
		SQLCmd:          envOr("REKSICHERUNG_E2E_SQLCMD", "sqlcmd"),
		Timeout:         time.Minute,
		MaxBackupBytes:  1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	registry, err := runregistry.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	runID := fmt.Sprintf("held-%d", time.Now().Unix())
	database, err := sqlserver.DatabaseNameForRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record, err := runregistry.NewRecord(runID, runregistry.CleanupHold, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	record.DatabaseName = database
	record.Status = runregistry.StatusFinished
	record.Verdict = runregistry.VerdictPass
	record.CleanupStatus = runregistry.CleanupRetained
	if _, err := registry.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	if err := createEmptyDatabase(address, user, password, database); err != nil {
		t.Fatalf("create empty database: %v", err)
	}
	dropper := databaseDropper{operations: operations}
	if _, err := runregistry.CleanupHeld(context.Background(), registry, dropper, runID); err != nil {
		t.Fatalf("CleanupHeld: %v", err)
	}
	if exists, err := databaseExists(address, user, password, database); err != nil || exists {
		t.Fatalf("held database remains: exists=%v err=%v", exists, err)
	}
}

type databaseDropper struct {
	operations *sqlserver.RemoteOperations
}

func (d databaseDropper) DropDatabase(ctx context.Context, runID string) error {
	return d.operations.DropDatabase(ctx, runID)
}

func requiredEnv(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Skipf("missing %s", key)
	}
	return value
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func sqlDSN(address, user, password, database string) (string, error) {
	hostPort := strings.TrimPrefix(address, "tcp:")
	hostPort = strings.Replace(hostPort, ",", ":", 1)
	if hostPort == "" || user == "" || password == "" || database == "" {
		return "", fmt.Errorf("invalid SQL DSN components")
	}
	u := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(user, password),
		Host:   hostPort,
	}
	q := u.Query()
	q.Set("database", database)
	q.Set("encrypt", "disable")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func databaseExists(address, user, password, database string) (bool, error) {
	dsn, err := sqlDSN(address, user, password, "master")
	if err != nil {
		return false, err
	}
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var id sql.NullInt64
	err = db.QueryRowContext(ctx, "SELECT DB_ID(@p1)", sql.Named("p1", database)).Scan(&id)
	if err != nil {
		return false, err
	}
	return id.Valid, nil
}

func createEmptyDatabase(address, user, password, database string) error {
	dsn, err := sqlDSN(address, user, password, "master")
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = db.ExecContext(ctx, "CREATE DATABASE ["+database+"]")
	return err
}
