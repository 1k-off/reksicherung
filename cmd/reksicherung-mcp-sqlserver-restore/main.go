// Command reksicherung-mcp-sqlserver-restore exposes bounded restore operations
// against a pre-existing SQL Server. It does not manage containers or services.
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

	sqlserver "github.com/1k-off/reksicherung/internal/reksicherung/restore/sqlserver"
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

type remoteService interface {
	restoreService
}

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
	backupInfo, err := os.Lstat(config.backupDirectory)
	if err != nil || !backupInfo.IsDir() || backupInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("SQL Server shared backup directory must be pre-created and mounted")
	}
	provider, err := openbao.New(config.openBaoAddress, config.openBaoToken)
	if err != nil {
		return errors.New("could not initialize configured OpenBao secret provider")
	}
	password, err := resolvePassword(ctx, provider, config.passwordRef)
	if err != nil {
		return errors.New("could not resolve configured SQL Server restore-password secret")
	}
	defer clear(password)
	operations, err := sqlserver.NewRemote(nil, sqlserver.RemoteConfig{
		Server:          config.address,
		Username:        config.username,
		Password:        string(password),
		BackupDirectory: config.backupDirectory,
		DataDirectory:   config.dataDirectory,
		SQLCmd:          config.sqlCmd,
		SQLPackage:      config.sqlPackage,
		Timeout:         6 * time.Hour,
		MaxBackupBytes:  maxBytes,
	})
	if err != nil {
		return errors.New("could not initialize SQL Server restore operations")
	}
	return serve(ctx, operations, artifacts, stdin, stdout)
}

func resolvePassword(ctx context.Context, provider secrets.SecretProvider, raw string) ([]byte, error) {
	reference, err := secrets.ParseReference(raw)
	if err != nil {
		return nil, errors.New("invalid secret reference")
	}
	value, err := provider.Resolve(ctx, reference)
	if err != nil || len(value) == 0 {
		clear(value)
		return nil, errors.New("secret resolution failed")
	}
	return value, nil
}

func serve(ctx context.Context, service restoreService, artifacts artifactStore, stdin io.ReadCloser, stdout io.WriteCloser) error {
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
