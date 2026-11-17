// Command reksicherung-mcp-mysql serves the MySQL inspection tools over MCP
// stdio.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	mysqlsvc "github.com/1k-off/reksicherung/internal/reksicherung/mcp/mysql"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets/openbao"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	databaseEnv      = "REKSICHERUNG_MYSQL_DATABASE"
	openBaoAddrEnv   = "REKSICHERUNG_OPENBAO_ADDR"
	openBaoTokenEnv  = "REKSICHERUNG_OPENBAO_TOKEN"
	maxMCPFrameBytes = 64 * 1024
)

type serverConfig struct {
	database       string
	openBaoAddress string
	openBaoToken   string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Getenv, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, stdin, stdout *os.File) error {
	config, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	provider, err := openbao.New(config.openBaoAddress, config.openBaoToken)
	if err != nil {
		return errors.New("could not initialize configured OpenBao secret provider")
	}
	material, err := secrets.ResolveMySQL(ctx, provider)
	if err != nil {
		return errors.New("could not resolve MySQL connection from OpenBao")
	}
	defer material.Clear()

	dsn, err := material.DSN(config.database)
	if err != nil {
		return errors.New("could not build MySQL DSN")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return errors.New("could not initialize MySQL connection")
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return errors.New("could not connect to configured MySQL server")
	}

	service, err := mysqlsvc.New(db, mysqlsvc.Options{
		TargetDatabase:   config.database,
		QueryTimeout:     10 * time.Second,
		IntegrityTimeout: 15 * time.Minute,
		MaxSampleRows:    100,
		MaxTables:        200,
		MaxColumns:       200,
	})
	if err != nil {
		return errors.New("could not initialize MySQL inspection")
	}

	return serve(ctx, inspectionAdapter{service}, config.database, stdin, stdout)
}

// inspectionAdapter exposes ScopeToDatabase for run_id-targeted semantic review.
type inspectionAdapter struct {
	*mysqlsvc.Service
}

func (adapter inspectionAdapter) ScopeToDatabase(database string) (inspectionService, error) {
	scoped, err := adapter.Service.ForDatabase(database)
	if err != nil {
		return nil, err
	}
	return inspectionAdapter{scoped}, nil
}

// loadConfig reads process configuration. REKSICHERUNG_MYSQL_DATABASE is
// optional: when unset the process connects to information_schema and every
// tool call must pass a run_id.
func loadConfig(getenv func(string) string) (serverConfig, error) {
	config := serverConfig{
		database:       strings.TrimSpace(getenv(databaseEnv)),
		openBaoAddress: getenv(openBaoAddrEnv),
		openBaoToken:   getenv(openBaoTokenEnv),
	}
	if config.openBaoAddress == "" || config.openBaoToken == "" {
		return serverConfig{}, errors.New("REKSICHERUNG_OPENBAO_ADDR and REKSICHERUNG_OPENBAO_TOKEN must be set")
	}
	if config.database == "" {
		config.database = mysqlsvc.AnchorDatabase
	}
	return config, nil
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
