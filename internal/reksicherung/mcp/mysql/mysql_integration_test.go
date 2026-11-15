//go:build integration

package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// TestMySQLLoopbackFixture is opt-in and refuses any DSN whose host is not a
// loopback address. Use a disposable MySQL started by the operator; the test
// creates and drops its own table in the DSN's database using dummy rows.
func TestMySQLLoopbackFixture(t *testing.T) {
	dsn := os.Getenv("REKSICHERUNG_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set REKSICHERUNG_MYSQL_TEST_DSN to a local disposable MySQL")
	}
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil || config.Net != "tcp" {
		t.Fatal("integration DSN must be a valid tcp DSN")
	}
	host, _, err := net.SplitHostPort(config.Addr)
	if err != nil || !isLoopbackHost(host) {
		t.Fatal("integration DSN must use a loopback host")
	}
	database := config.DBName
	if database == "" {
		t.Fatal("integration DSN must select the disposable fixture database")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal("could not open local MySQL test connection")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("could not reach local MySQL")
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
	qualified := quotedDatabase + "." + quotedTable
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+qualified+" (id int NOT NULL, status varchar(32) NULL)"); err != nil {
		t.Fatal("could not create local dummy-data fixture")
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, "DROP TABLE "+qualified)
	}()
	if _, err := db.ExecContext(ctx, "INSERT INTO "+qualified+" (id, status) VALUES (1,'fixture-ok'),(2,'fixture-ok'),(3,'fixture-other')"); err != nil {
		t.Fatal("could not insert local dummy fixture rows")
	}

	service, err := New(db, Options{TargetDatabase: database, MaxSampleRows: 1})
	if err != nil {
		t.Fatal("could not construct MySQL service")
	}
	ref := TableRef{Database: database, Table: table}
	description, err := service.DescribeTable(ctx, ref)
	if err != nil || len(description.Columns) != 2 {
		t.Fatal("MySQL service did not inspect the dummy fixture schema")
	}
	result, err := service.Validate(ctx, ValidationQuery{Table: ref, Limit: 1})
	if err != nil || result != (ValidationResult{ObservedRows: 1, Limit: 1, Truncated: true}) {
		t.Fatal("MySQL service did not return the bounded dummy-fixture aggregate")
	}
	if _, err := service.ListTables(ctx, "another_database", ""); err == nil {
		t.Fatal("MySQL service accepted a database outside its configured target")
	}
	integrity, err := service.CheckIntegrity(ctx)
	if err != nil || !integrity.CommandCompleted {
		t.Fatal("MySQL service did not complete CHECK TABLE on the disposable fixture")
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
