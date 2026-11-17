package runregistry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDatabaseTypeValidationAndDefault(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	record := mustNewRecord(t, "run-type", CleanupAutomatic, now, now.Add(time.Hour))
	for _, valid := range []DatabaseType{"", DatabaseTypeSQLServer, DatabaseTypeMySQL} {
		record.DatabaseType = valid
		if err := record.Validate(); err != nil {
			t.Errorf("Validate(%q) error = %v", valid, err)
		}
	}
	record.DatabaseType = "postgres"
	if err := record.Validate(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Validate(postgres) error = %v, want ErrInvalidRecord", err)
	}
	if got := DatabaseType("").Effective(); got != DatabaseTypeSQLServer {
		t.Fatalf("empty Effective() = %q, want sqlserver", got)
	}
}

func TestDatabaseTypeImmutableOnceDatabaseNameSet(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	record, err := store.Create(context.Background(), mustNewRecord(t, "run-immutable", CleanupHold, now, now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	record = updateRecord(t, store, record, func(next *Record) {
		next.DatabaseName = "reksicherung_abc"
		next.DatabaseType = DatabaseTypeMySQL
	})
	_, err = store.Update(context.Background(), record.ID, record.Revision, func(next *Record) error {
		next.DatabaseType = DatabaseTypeSQLServer
		return nil
	})
	if !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("changing database type error = %v, want ErrInvalidRecord", err)
	}
}

func TestDropperForRoutesByDatabaseType(t *testing.T) {
	t.Parallel()

	sql, my := &fakeDatabaseDropper{}, &fakeDatabaseDropper{}
	tests := []struct {
		dbType  DatabaseType
		want    DatabaseDropper
		wantErr bool
	}{
		{dbType: "", want: sql},
		{dbType: DatabaseTypeSQLServer, want: sql},
		{dbType: DatabaseTypeMySQL, want: my},
		{dbType: "oracle", wantErr: true},
	}
	for _, test := range tests {
		got, err := DropperFor(Record{DatabaseType: test.dbType}, sql, my)
		if test.wantErr {
			if err == nil || got != nil {
				t.Errorf("DropperFor(%q) = %v, %v; want error", test.dbType, got, err)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Errorf("DropperFor(%q) = %v, %v; want %v", test.dbType, got, err, test.want)
		}
	}
	if _, err := DropperFor(Record{DatabaseType: DatabaseTypeMySQL}, sql, nil); err == nil {
		t.Fatal("DropperFor(mysql, nil mysql dropper) error = nil")
	}
}

func TestCleanupHeldRoutesByDatabaseType(t *testing.T) {
	tests := []struct {
		name    string
		dbType  DatabaseType
		wantSQL bool
	}{
		{name: "empty defaults to sqlserver", dbType: "", wantSQL: true},
		{name: "sqlserver", dbType: DatabaseTypeSQLServer, wantSQL: true},
		{name: "mysql", dbType: DatabaseTypeMySQL, wantSQL: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openCleanupTestStore(t)
			createHeldRunWithType(t, store, test.dbType)
			sql, my := &fakeDatabaseDropper{}, &fakeDatabaseDropper{}

			record, err := CleanupHeld(context.Background(), store, EngineDropper{SQLServer: sql, MySQL: my}, "run-123")
			if err != nil {
				t.Fatalf("CleanupHeld() error = %v", err)
			}
			if record.CleanupStatus != CleanupSucceeded {
				t.Fatalf("cleanup status = %q", record.CleanupStatus)
			}
			gotSQL, gotMy := sql.droppedRunID, my.droppedRunID
			if test.wantSQL && (gotSQL != "run-123" || gotMy != "") {
				t.Fatalf("drops sql=%q mysql=%q, want sql only", gotSQL, gotMy)
			}
			if !test.wantSQL && (gotMy != "run-123" || gotSQL != "") {
				t.Fatalf("drops sql=%q mysql=%q, want mysql only", gotSQL, gotMy)
			}
		})
	}
}

func TestCleanupHeldInvalidTypeDropsNothingAndLeavesRetained(t *testing.T) {
	store := openCleanupTestStore(t)
	createHeldRunWithType(t, store, DatabaseTypeMySQL)
	sql := &fakeDatabaseDropper{}

	_, err := CleanupHeld(context.Background(), store, EngineDropper{SQLServer: sql}, "run-123")
	if err == nil {
		t.Fatal("CleanupHeld() error = nil, want missing mysql dropper")
	}
	if sql.droppedRunID != "" {
		t.Fatalf("sqlserver dropper called for mysql record: %q", sql.droppedRunID)
	}
	got, err := store.Get(context.Background(), "run-123")
	if err != nil {
		t.Fatal(err)
	}
	if got.CleanupStatus != CleanupFailed && got.CleanupStatus != CleanupRetained {
		t.Fatalf("cleanup status = %q, want failed or retained", got.CleanupStatus)
	}
}

func TestReconcileRoutesByDatabaseType(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	now := time.Now().UTC()
	createExpiringTypedRun(t, store, "run-sql-empty", "", now)
	createExpiringTypedRun(t, store, "run-sql", DatabaseTypeSQLServer, now)
	createExpiringTypedRun(t, store, "run-my", DatabaseTypeMySQL, now)
	sql, my := &fakeDropper{}, &fakeDropper{}

	outcomes, err := (Reconciler{Store: store, Databases: EngineDropper{SQLServer: sql, MySQL: my}}).Reconcile(context.Background(), now)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %#v", outcomes)
	}
	for _, outcome := range outcomes {
		if outcome.Err != nil || outcome.Status != CleanupSucceeded {
			t.Fatalf("outcome = %#v", outcome)
		}
	}
	if len(sql.ids) != 2 || len(my.ids) != 1 || my.ids[0] != "run-my" {
		t.Fatalf("sql drops = %v, mysql drops = %v", sql.ids, my.ids)
	}
}

func createHeldRunWithType(t *testing.T, store *Store, dbType DatabaseType) {
	t.Helper()
	now := time.Now().UTC()
	record, err := NewRecord("run-123", CleanupHold, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	record.DatabaseName = "reksicherung_abc"
	record.DatabaseType = dbType
	record.Status = StatusFinished
	record.Verdict = VerdictPass
	record.CleanupStatus = CleanupRetained
	if _, err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}

func createExpiringTypedRun(t *testing.T, store *Store, id string, dbType DatabaseType, now time.Time) {
	t.Helper()
	record, err := store.Create(context.Background(), mustNewRecord(t, id, CleanupAutomatic, now.Add(-2*time.Minute), now.Add(-time.Minute)))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	updateRecord(t, store, record, func(next *Record) {
		next.DatabaseName = "reksicherung_" + id
		next.DatabaseType = dbType
	})
}
