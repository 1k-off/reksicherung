// Command reksicherung-mcp-gobackup serves bounded GoBackup archive discovery
// tools over MCP stdio. Per-source S3 material is resolved from OpenBao.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/1k-off/reksicherung/internal/reksicherung/restore/artifact"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets/openbao"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxMCPFrameBytes        = 64 * 1024
	artifactStageDirEnv     = "REKSICHERUNG_ARTIFACT_STAGE_DIR"
	artifactMaxBytesEnv     = "REKSICHERUNG_ARTIFACT_MAX_BYTES"
	defaultArtifactMaxBytes = int64(200 << 30)
	hardArtifactMaxBytes    = int64(1 << 40)
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
	config, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	provider, err := openbao.New(config.openBaoAddress, config.openBaoToken)
	if err != nil {
		return errors.New("could not initialize configured OpenBao secret provider")
	}
	maxBytes := defaultArtifactMaxBytes
	if value := getenv(artifactMaxBytesEnv); value != "" {
		maxBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil || maxBytes < 1 || maxBytes > hardArtifactMaxBytes {
			return errors.New("invalid configured artifact size limit")
		}
	}
	stageDirectory := strings.TrimSpace(getenv(artifactStageDirEnv))
	if stageDirectory == "" {
		stageDirectory = staging.DefaultDirectory
	}
	artifactStore, err := staging.New(stageDirectory, maxBytes)
	if err != nil {
		return errors.New("could not initialize configured artifact staging directory")
	}
	// Resolve unwraps archives into TempDir; keep it on the staged volume, not
	// the tiny container /tmp (distroless nonroot overlay).
	tempDirectory := filepath.Join(stageDirectory, "tmp")
	if err := os.MkdirAll(tempDirectory, 0o750); err != nil {
		return errors.New("could not initialize artifact temporary directory")
	}
	gateway, err := newSourceGateway(provider, artifactStore, artifact.Limits{
		MaxInputBytes:     maxBytes,
		MaxExpandedBytes:  maxBytes,
		MaxTemporaryBytes: maxBytes,
		TempDir:           tempDirectory,
	}, maxBytes)
	if err != nil {
		return errors.New("could not initialize GoBackup source gateway")
	}
	return serve(ctx, gateway, gateway, stdin, stdout)
}

func serve(ctx context.Context, service multiSourceArchives, stager multiSourceStaging, stdin io.ReadCloser, stdout io.WriteCloser) error {
	server := newMCPServer(service, stager)
	if err := server.Run(ctx, &mcp.IOTransport{
		Reader: newBoundedFrameReader(stdin, maxMCPFrameBytes),
		Writer: stdout,
	}); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("MCP stdio server failed")
	}
	return nil
}
