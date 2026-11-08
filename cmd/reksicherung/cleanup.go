package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

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

	dropper, err := newSQLDropperFromEnv(ctx, os.Getenv)
	if err != nil {
		return err
	}
	record, err := runregistry.CleanupHeld(ctx, store, dropper, *runID)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "cleanup succeeded: run=%s database=%s status=%s\n",
		record.ID, record.DatabaseName, record.CleanupStatus)
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

	dropper, err := newSQLDropperFromEnv(ctx, os.Getenv)
	if err != nil {
		return err
	}
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
	password, err := resolveCleanupPassword(ctx, provider, config.passwordRef)
	if err != nil {
		return nil, err
	}
	defer clearBytes(password)

	operations, err := sqlserver.NewRemote(nil, sqlserver.RemoteConfig{
		Server:          config.address,
		Username:        config.username,
		Password:        string(password),
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
	address         string
	username        string
	passwordRef     string
	backupDirectory string
	dataDirectory   string
	sqlCmd          string
	openBaoAddress  string
	openBaoToken    string
}

func loadCleanupSQLConfig(getenv func(string) string) (cleanupSQLConfig, error) {
	config := cleanupSQLConfig{
		address:         getenv("REKSICHERUNG_SQLSERVER_ADDRESS"),
		username:        getenv("REKSICHERUNG_SQLSERVER_RESTORE_USERNAME"),
		passwordRef:     getenv("REKSICHERUNG_SQLSERVER_RESTORE_PASSWORD_REF"),
		backupDirectory: getenv("REKSICHERUNG_SQLSERVER_BACKUP_DIR"),
		dataDirectory:   getenv("REKSICHERUNG_SQLSERVER_DATA_DIR"),
		sqlCmd:          getenv("REKSICHERUNG_SQLCMD"),
		openBaoAddress:  getenv("REKSICHERUNG_OPENBAO_ADDR"),
		openBaoToken:    getenv("REKSICHERUNG_OPENBAO_TOKEN"),
	}
	for _, value := range []string{
		config.address, config.username, config.passwordRef,
		config.backupDirectory, config.dataDirectory,
		config.openBaoAddress, config.openBaoToken,
	} {
		if strings.TrimSpace(value) == "" {
			return cleanupSQLConfig{}, errors.New("required SQL cleanup configuration is missing")
		}
	}
	if config.sqlCmd == "" {
		config.sqlCmd = "sqlcmd"
	}
	return config, nil
}

func resolveCleanupPassword(ctx context.Context, provider secrets.SecretProvider, raw string) ([]byte, error) {
	reference, err := secrets.ParseReference(raw)
	if err != nil {
		return nil, errors.New("invalid secret reference")
	}
	value, err := provider.Resolve(ctx, reference)
	if err != nil || len(value) == 0 {
		clearBytes(value)
		return nil, errors.New("secret resolution failed")
	}
	return value, nil
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
