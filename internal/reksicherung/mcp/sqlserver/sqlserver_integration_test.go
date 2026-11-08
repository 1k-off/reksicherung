//go:build integration

package sqlserver

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/microsoft/go-mssqldb"
)

func TestSQLServerLoopbackFixture(t *testing.T) {
	dsn := os.Getenv("REKSICHERUNG_SQLSERVER_TEST_DSN")
	if dsn == "" {
		t.Skip("set REKSICHERUNG_SQLSERVER_TEST_DSN to a local Podman SQL Server")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "sqlserver" || !isLoopbackHost(parsed.Hostname()) {
		t.Fatal("integration DSN must use the sqlserver scheme and a loopback host")
	}
	database := parsed.Query().Get("database")
	if database == "" {
		t.Fatal("integration DSN must select the disposable fixture database")
	}

	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatal("could not open local SQL Server test connection")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("could not reach local SQL Server")
	}

	randomID := make([]byte, 8)
	if _, err := rand.Read(randomID); err != nil {
		t.Fatal("could not create fixture identifier")
	}
	table := "reksicherung_semantic_fixture_" + hex.EncodeToString(randomID)
	quotedDatabase, err := quoteIdentifier(database)
	if err != nil {
		t.Fatal("invalid fixture database name")
	}
	quotedTable, err := quoteIdentifier(table)
	if err != nil {
		t.Fatal("invalid fixture table name")
	}
	qualifiedTable := quotedDatabase + ".[dbo]." + quotedTable
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+qualifiedTable+" ([id] int NOT NULL, [status] nvarchar(32) NULL)"); err != nil {
		t.Fatal("could not create local dummy-data fixture")
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, "DROP TABLE "+qualifiedTable)
	}()
	if _, err := db.ExecContext(ctx, "INSERT INTO "+qualifiedTable+" ([id], [status]) VALUES (1, N'fixture-ok'), (2, N'fixture-ok'), (3, N'fixture-other')"); err != nil {
		t.Fatal("could not insert local dummy fixture rows")
	}

	service, err := New(db, Options{TargetDatabase: database, MaxSampleRows: 1})
	if err != nil {
		t.Fatal("could not construct SQL Server service")
	}
	ref := TableRef{Database: database, Schema: "dbo", Table: table}
	description, err := service.DescribeTable(ctx, ref)
	if err != nil || len(description.Columns) != 2 {
		t.Fatal("SQL Server service did not inspect the dummy fixture schema")
	}
	result, err := service.Validate(ctx, ValidationQuery{
		Table: ref,
		Limit: 1,
	})
	if err != nil || result != (ValidationResult{ObservedRows: 1, Limit: 1, Truncated: true}) {
		t.Fatal("SQL Server service did not return the bounded dummy-fixture aggregate")
	}
	if _, err := service.ListTables(ctx, "another_database", "dbo"); err == nil {
		t.Fatal("SQL Server service accepted a database outside its configured target")
	}
	integrity, err := service.CheckIntegrity(ctx)
	if err != nil || !integrity.Completed {
		t.Fatal("SQL Server service did not complete DBCC CHECKDB on the disposable fixture")
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
