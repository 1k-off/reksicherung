package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	restoremysql "github.com/1k-off/reksicherung/internal/reksicherung/restore/mysql"
	sqlserver "github.com/1k-off/reksicherung/internal/reksicherung/restore/sqlserver"
	"github.com/1k-off/reksicherung/internal/reksicherung/runregistry"
)

const holdUsage = "usage: reksicherung hold -registry <runs.db> -run-id <id> -database-type sqlserver|mysql -verdict pass|fail|inconclusive"

// runHold records a hold_for_review run in the run registry with an explicit
// database type, so `cleanup` and `reconcile` route the drop to the right
// engine. The database type is required: an unset type would be treated as
// sqlserver and mis-route a MySQL drop.
func runHold(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("hold", flag.ContinueOnError)
	flags.SetOutput(stderr)
	registryPath := flags.String("registry", "", "path to the durable run registry database")
	runID := flags.String("run-id", "", "run ID used for the restore (database name is derived from it)")
	databaseType := flags.String("database-type", "", "engine hosting the restored database: sqlserver or mysql (required)")
	verdict := flags.String("verdict", "", "run verdict: pass, fail, or inconclusive (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *registryPath == "" || *runID == "" || *databaseType == "" || *verdict == "" {
		return errors.New(holdUsage)
	}

	dbType := runregistry.DatabaseType(*databaseType)
	var database string
	var err error
	switch dbType {
	case runregistry.DatabaseTypeSQLServer:
		database, err = sqlserver.DatabaseNameForRun(*runID)
	case runregistry.DatabaseTypeMySQL:
		database, err = restoremysql.DatabaseNameForRun(*runID)
	default:
		return fmt.Errorf("invalid -database-type %q: want sqlserver or mysql", *databaseType)
	}
	if err != nil {
		return fmt.Errorf("invalid -run-id: %w", err)
	}

	now := time.Now().UTC()
	record, err := runregistry.NewRecord(*runID, runregistry.CleanupHold, now, now.Add(time.Hour))
	if err != nil {
		return err
	}
	record.DatabaseName = database
	record.DatabaseType = dbType
	record.Status = runregistry.StatusFinished
	record.Verdict = runregistry.Verdict(*verdict)
	record.CleanupStatus = runregistry.CleanupRetained

	store, err := runregistry.Open(*registryPath)
	if err != nil {
		return err
	}
	defer store.Close()

	created, err := store.Create(ctx, record)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "hold recorded: run=%s database=%s type=%s\n",
		created.ID, created.DatabaseName, created.DatabaseType)
	return nil
}
