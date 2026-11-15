package runregistry

import (
	"context"
	"errors"
	"fmt"
)

// DatabaseType identifies the engine that hosts a run's restored database.
type DatabaseType string

const (
	DatabaseTypeSQLServer DatabaseType = "sqlserver"
	DatabaseTypeMySQL     DatabaseType = "mysql"
)

// Effective returns the engine to use for cleanup. Records written before
// engines were tracked have an empty type and always refer to SQL Server.
func (t DatabaseType) Effective() DatabaseType {
	if t == "" {
		return DatabaseTypeSQLServer
	}
	return t
}

func (t DatabaseType) valid() bool {
	switch t {
	case "", DatabaseTypeSQLServer, DatabaseTypeMySQL:
		return true
	default:
		return false
	}
}

// RecordDropper is implemented by droppers that choose an engine from the run
// record. CleanupHeld and Reconciler prefer it over DatabaseDropper.
type RecordDropper interface {
	DropRecordDatabase(context.Context, Record) error
}

// EngineDropper routes drops to the dropper for the record's database type.
type EngineDropper struct {
	SQLServer DatabaseDropper
	MySQL     DatabaseDropper
}

// DropperFor selects the dropper for a record. An empty type means sqlserver.
func DropperFor(record Record, sql, my DatabaseDropper) (DatabaseDropper, error) {
	var dropper DatabaseDropper
	switch record.DatabaseType.Effective() {
	case DatabaseTypeSQLServer:
		dropper = sql
	case DatabaseTypeMySQL:
		dropper = my
	default:
		return nil, fmt.Errorf("run registry: unsupported database type %q", record.DatabaseType)
	}
	if dropper == nil {
		return nil, fmt.Errorf("run registry: no dropper configured for database type %q", record.DatabaseType.Effective())
	}
	return dropper, nil
}

// DropRecordDatabase drops the record's database using the matching engine.
func (d EngineDropper) DropRecordDatabase(ctx context.Context, record Record) error {
	dropper, err := DropperFor(record, d.SQLServer, d.MySQL)
	if err != nil {
		return err
	}
	return dropper.DropDatabase(ctx, record.ID)
}

// DropDatabase always fails: engine routing needs the run record.
func (EngineDropper) DropDatabase(context.Context, string) error {
	return errors.New("run registry: engine dropper requires a run record")
}

func dropRecordDatabase(ctx context.Context, databases DatabaseDropper, record Record) error {
	if routed, ok := databases.(RecordDropper); ok {
		return routed.DropRecordDatabase(ctx, record)
	}
	return databases.DropDatabase(ctx, record.ID)
}
