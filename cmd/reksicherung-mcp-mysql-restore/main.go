// Command reksicherung-mcp-mysql-restore exposes bounded restore operations
// against a pre-existing MySQL server. It does not manage containers or services.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	mysqlrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/mysql"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets/openbao"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxMCPFrameBytes        = 64 * 1024
	defaultArtifactMaxBytes = int64(200 << 30)
	hardArtifactMaxBytes    = int64(1 << 40)
	artifactLimitEnv        = "REKSICHERUNG_ARTIFACT_MAX_BYTES"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, stdin io.ReadCloser, stdout io.WriteCloser) error {
	config, err := loadRestoreConfig(getenv)
	if err != nil {
		return err
	}
	maxBytes := defaultArtifactMaxBytes
	if value := getenv(artifactLimitEnv); value != "" {
		maxBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil || maxBytes < 1 || maxBytes > hardArtifactMaxBytes {
			return errors.New("invalid configured artifact size limit")
		}
	}
	artifacts, err := staging.New(config.stageDirectory, maxBytes)
	if err != nil {
		return errors.New("could not initialize configured artifact staging directory")
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
	operations, err := mysqlrestore.NewRemote(nil, mysqlrestore.RemoteConfig{
		Host:         material.Host,
		Port:         material.Port,
		Username:     material.Username,
		Password:     string(material.Password),
		MySQL:        config.mysql,
		MySQLCheck:   config.mysqlCheck,
		TempDir:      config.tempDirectory,
		Timeout:      6 * time.Hour,
		MaxDumpBytes: maxBytes,
	})
	if err != nil {
		return errors.New("could not initialize MySQL restore operations")
	}
	return serve(ctx, operations, artifacts, stdin, stdout)
}

func serve(ctx context.Context, service restoreService, artifacts *staging.Store, stdin io.ReadCloser, stdout io.WriteCloser) error {
	server := newRestoreMCPServer(service, artifacts)
	if err := server.Run(ctx, &mcp.IOTransport{
		Reader: newRestoreFrameReader(stdin, maxMCPFrameBytes),
		Writer: stdout,
	}); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("MCP stdio server failed")
	}
	return nil
}
