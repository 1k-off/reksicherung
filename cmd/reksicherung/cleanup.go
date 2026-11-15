package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	restoremysql "github.com/1k-off/reksicherung/internal/reksicherung/restore/mysql"
	sqlserver "github.com/1k-off/reksicherung/internal/reksicherung/restore/sqlserver"
	"github.com/1k-off/reksicherung/internal/reksicherung/runregistry"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets/openbao"
)

func runCleanup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	registryPath := flags.String("registry", "", "path to the durable run registry database")
	runID := flags.String("run-id", "", "held-for-review run ID whose restored database should be dropped")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *registryPath == "" || *runID == "" {
		return errors.New("usage: reksicherung cleanup -registry <runs.db> -run-id <held-run>")
	}

	store, err := runregistry.Open(*registryPath)
	if err != nil {
		return err
	}
	defer store.Close()

	dropper := newEngineDropperFromEnv(os.Getenv)
	record, err := runregistry.CleanupHeld(ctx, store, dropper, *runID)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "cleanup succeeded: run=%s database=%s type=%s status=%s\n",
		record.ID, record.DatabaseName, record.DatabaseType.Effective(), record.CleanupStatus)
	return nil
}

func runReconcile(ctx context.Context, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	registryPath := flags.String("registry", "", "path to the durable run registry database")
	interval := flags.Duration("interval", 30*time.Second, "reconciliation interval")
	once := flags.Bool("once", false, "run a single reconciliation pass and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *registryPath == "" {
		return errors.New("usage: reksicherung reconcile -registry <runs.db> [-interval 30s] [-once]")
	}

	store, err := runregistry.Open(*registryPath)
	if err != nil {
		return err
	}
	defer store.Close()

	dropper := newEngineDropperFromEnv(os.Getenv)
	reconciler := runregistry.Reconciler{Store: store, Databases: dropper}
	if *once {
		outcomes, err := reconciler.Reconcile(ctx, time.Now().UTC())
		if err != nil {
			return err
		}
		for _, outcome := range outcomes {
			if outcome.Err != nil {
				fmt.Fprintf(stderr, "cleanup failed run=%s status=%s err=%v\n",
					outcome.RunID, outcome.Status, outcome.Err)
				continue
			}
			fmt.Fprintf(stderr, "cleanup ok run=%s status=%s\n", outcome.RunID, outcome.Status)
		}
		return nil
	}
	return reconciler.Run(ctx, *interval, func(outcome runregistry.CleanupOutcome) {
		if outcome.Err != nil {
			fmt.Fprintf(stderr, "cleanup failed run=%s status=%s err=%v\n",
				outcome.RunID, outcome.Status, outcome.Err)
			return
		}
		fmt.Fprintf(stderr, "cleanup ok run=%s status=%s\n", outcome.RunID, outcome.Status)
	})
}

// lazyDropper defers building an engine dropper (and resolving its secrets)
// until the first drop, so a cleanup of one engine does not require the other
// engine's configuration.
type lazyDropper struct {
	build   func(context.Context) (runregistry.DatabaseDropper, error)
	mu      sync.Mutex
	dropper runregistry.DatabaseDropper
}

// get returns the cached dropper. Only a successful build is cached; build
// errors are returned and construction is retried on the next call.
func (l *lazyDropper) get(ctx context.Context) (runregistry.DatabaseDropper, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dropper != nil {
		return l.dropper, nil
	}
	dropper, err := l.build(ctx)
	if err != nil {
		return nil, err
	}
	l.dropper = dropper
	return dropper, nil
}

func (l *lazyDropper) DropDatabase(ctx context.Context, runID string) error {
	dropper, err := l.get(ctx)
	if err != nil {
		return err
	}
	return dropper.DropDatabase(ctx, runID)
}

// newEngineDropperFromEnv routes drops by Record.DatabaseType (empty means
// sqlserver). Engine configuration is resolved lazily.
func newEngineDropperFromEnv(getenv func(string) string) runregistry.EngineDropper {
	return runregistry.EngineDropper{
		SQLServer: &lazyDropper{build: func(ctx context.Context) (runregistry.DatabaseDropper, error) {
			return newSQLDropperFromEnv(ctx, getenv)
		}},
		MySQL: &lazyDropper{build: func(ctx context.Context) (runregistry.DatabaseDropper, error) {
			return newMySQLDropperFromEnv(ctx, getenv)
		}},
	}
}

type sqlDropper struct {
	operations *sqlserver.RemoteOperations
}

func (dropper sqlDropper) DropDatabase(ctx context.Context, runID string) error {
	return dropper.operations.DropDatabase(ctx, runID)
}

func newSQLDropperFromEnv(ctx context.Context, getenv func(string) string) (runregistry.DatabaseDropper, error) {
	config, err := loadCleanupSQLConfig(getenv)
	if err != nil {
		return nil, err
	}
	provider, err := openbao.New(config.openBaoAddress, config.openBaoToken)
	if err != nil {
		return nil, errors.New("could not initialize configured OpenBao secret provider")
	}
	sqlMaterial, err := secrets.ResolveSQLServer(ctx, provider)
	if err != nil {
		return nil, errors.New("could not resolve SQL Server connection from OpenBao")
	}
	defer sqlMaterial.Clear()

	operations, err := sqlserver.NewRemote(nil, sqlserver.RemoteConfig{
		Server:          sqlMaterial.SQLCmdAddress(),
		Username:        sqlMaterial.Username,
		Password:        string(sqlMaterial.Password),
		BackupDirectory: config.backupDirectory,
		DataDirectory:   config.dataDirectory,
		SQLCmd:          config.sqlCmd,
		Timeout:         time.Minute,
		MaxBackupBytes:  1 << 20,
	})
	if err != nil {
		return nil, errors.New("could not initialize SQL Server cleanup operations")
	}
	return sqlDropper{operations: operations}, nil
}

type cleanupSQLConfig struct {
	backupDirectory string
	dataDirectory   string
	sqlCmd          string
	openBaoAddress  string
	openBaoToken    string
}

func loadCleanupSQLConfig(getenv func(string) string) (cleanupSQLConfig, error) {
	config := cleanupSQLConfig{
		backupDirectory: getenv("REKSICHERUNG_SQLSERVER_BACKUP_DIR"),
		dataDirectory:   getenv("REKSICHERUNG_SQLSERVER_DATA_DIR"),
		sqlCmd:          getenv("REKSICHERUNG_SQLCMD"),
		openBaoAddress:  getenv("REKSICHERUNG_OPENBAO_ADDR"),
		openBaoToken:    getenv("REKSICHERUNG_OPENBAO_TOKEN"),
	}
	if strings.TrimSpace(config.backupDirectory) == "" {
		config.backupDirectory = sqlserver.DefaultBackupDirectory
	}
	if strings.TrimSpace(config.dataDirectory) == "" {
		config.dataDirectory = sqlserver.DefaultDataDirectory
	}
	if strings.TrimSpace(config.openBaoAddress) == "" || strings.TrimSpace(config.openBaoToken) == "" {
		return cleanupSQLConfig{}, errors.New("required SQL cleanup configuration is missing")
	}
	if config.sqlCmd == "" {
		config.sqlCmd = "sqlcmd"
	}
	return config, nil
}

type mysqlDropper struct {
	operations *restoremysql.RemoteOperations
}

func (dropper mysqlDropper) DropDatabase(ctx context.Context, runID string) error {
	return dropper.operations.DropDatabase(ctx, runID)
}

type cleanupMySQLConfig struct {
	mysqlCmd       string
	mysqlCheckCmd  string
	tempDir        string
	openBaoAddress string
	openBaoToken   string
}

func loadCleanupMySQLConfig(getenv func(string) string) (cleanupMySQLConfig, error) {
	config := cleanupMySQLConfig{
		mysqlCmd:       getenv("REKSICHERUNG_MYSQL"),
		mysqlCheckCmd:  getenv("REKSICHERUNG_MYSQLCHECK"),
		tempDir:        getenv("REKSICHERUNG_MYSQL_TMPDIR"),
		openBaoAddress: getenv("REKSICHERUNG_OPENBAO_ADDR"),
		openBaoToken:   getenv("REKSICHERUNG_OPENBAO_TOKEN"),
	}
	if strings.TrimSpace(config.openBaoAddress) == "" || strings.TrimSpace(config.openBaoToken) == "" {
		return cleanupMySQLConfig{}, errors.New("required MySQL cleanup configuration is missing")
	}
	if config.mysqlCmd == "" {
		config.mysqlCmd = "mysql"
	}
	if config.mysqlCheckCmd == "" {
		config.mysqlCheckCmd = "mysqlcheck"
	}
	return config, nil
}

func newMySQLDropperFromEnv(ctx context.Context, getenv func(string) string) (runregistry.DatabaseDropper, error) {
	config, err := loadCleanupMySQLConfig(getenv)
	if err != nil {
		return nil, err
	}
	provider, err := openbao.New(config.openBaoAddress, config.openBaoToken)
	if err != nil {
		return nil, errors.New("could not initialize configured OpenBao secret provider")
	}
	material, err := secrets.ResolveMySQL(ctx, provider)
	if err != nil {
		return nil, errors.New("could not resolve MySQL connection from OpenBao")
	}
	defer material.Clear()

	operations, err := restoremysql.NewRemote(nil, restoremysql.RemoteConfig{
		Host:       material.Host,
		Port:       material.Port,
		Username:   material.Username,
		Password:   string(material.Password),
		MySQL:      config.mysqlCmd,
		MySQLCheck: config.mysqlCheckCmd,
		TempDir:    config.tempDir,
		Timeout:    time.Minute,
	})
	if err != nil {
		return nil, errors.New("could not initialize MySQL cleanup operations")
	}
	return mysqlDropper{operations: operations}, nil
}
