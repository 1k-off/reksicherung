// Command reksicherung-mcp-sqlserver serves the SQL Server inspection tools
// over MCP stdio.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/microsoft/go-mssqldb"

	sqlserver "github.com/1k-off/reksicherung/internal/reksicherung/mcp/sqlserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	dsnEnv           = "REKSICHERUNG_SQLSERVER_DSN"
	databaseEnv      = "REKSICHERUNG_SQLSERVER_DATABASE"
	maxMCPFrameBytes = 64 * 1024
)

type serverConfig struct {
	dsn      string
	database string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Getenv, os.Stdin, os.Stdout); err != nil {
		// Errors from this executable are deliberately generic and never include
		// environment values, connection details, prompts, or query arguments.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, stdin, stdout *os.File) error {
	config, err := loadConfig(getenv)
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlserver", config.dsn)
	if err != nil {
		return errors.New("could not initialize SQL Server connection")
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return errors.New("could not connect to configured SQL Server database")
	}

	service, err := sqlserver.New(db, sqlserver.Options{
		TargetDatabase:   config.database,
		QueryTimeout:     10 * time.Second,
		IntegrityTimeout: 15 * time.Minute,
		MaxSampleRows:    100,
		MaxTables:        200,
		MaxColumns:       200,
	})
	if err != nil {
		return errors.New("could not initialize SQL Server inspection")
	}

	return serve(ctx, service, config.database, stdin, stdout)
}

func loadConfig(getenv func(string) string) (serverConfig, error) {
	dsn := getenv(dsnEnv)
	database := getenv(databaseEnv)
	if dsn == "" || database == "" {
		return serverConfig{}, fmt.Errorf("%s and %s must be set", dsnEnv, databaseEnv)
	}
	if !dsnSelectsDatabase(dsn, database) {
		return serverConfig{}, errors.New("SQL Server DSN must select the configured target database")
	}
	return serverConfig{dsn: dsn, database: database}, nil
}

// dsnSelectsDatabase accepts the URL form used by go-mssqldb and prevents the
// server configuration from inspecting a database other than its selected
// target. It intentionally never includes the DSN in an error.
func dsnSelectsDatabase(dsn, target string) bool {
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "sqlserver" || parsed.Hostname() == "" ||
		parsed.Path != "" || parsed.Fragment != "" {
		return false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return false
	}
	database, ok := singleQueryValue(query, "database")
	if !ok || database != target {
		return false
	}
	if isLoopbackSQLHost(parsed.Hostname()) {
		return true
	}
	encrypt, ok := singleQueryValue(query, "encrypt")
	if !ok || (!strings.EqualFold(encrypt, "true") && !strings.EqualFold(encrypt, "strict")) {
		return false
	}
	trustServerCertificate, present, ok := optionalSingleQueryValue(query, "trustservercertificate")
	return ok && (!present || strings.EqualFold(trustServerCertificate, "false"))
}

func singleQueryValue(values url.Values, key string) (string, bool) {
	value, present, ok := optionalSingleQueryValue(values, key)
	return value, present && ok
}

func optionalSingleQueryValue(values url.Values, key string) (string, bool, bool) {
	var value string
	present := false
	for name, entries := range values {
		if !strings.EqualFold(name, key) {
			continue
		}
		if present || len(entries) != 1 {
			return "", false, false
		}
		present = true
		value = entries[0]
	}
	return value, present, true
}

func isLoopbackSQLHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func serve(ctx context.Context, service inspectionService, database string, stdin, stdout *os.File) error {
	server := newMCPServer(service, database)
	transport := &mcp.IOTransport{
		Reader: newBoundedFrameReader(stdin, maxMCPFrameBytes),
		Writer: stdout,
	}
	if err := server.Run(ctx, transport); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("MCP stdio server failed")
	}
	return nil
}
