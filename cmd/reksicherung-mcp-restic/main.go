// Command reksicherung-mcp-restic serves read-only Restic snapshot metadata
// tools over MCP stdio. Per-source repository material is resolved from OpenBao.
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

	resticservice "github.com/1k-off/reksicherung/internal/reksicherung/mcp/restic"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/artifact"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets/openbao"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxMCPFrameSize          = 64 * 1024
	resticStageDirEnv        = "REKSICHERUNG_ARTIFACT_STAGE_DIR"
	resticArtifactMaxEnv     = "REKSICHERUNG_ARTIFACT_MAX_BYTES"
	resticDefaultArtifactMax = int64(200 << 30)
	resticHardArtifactMax    = int64(1 << 40)
)

type multiSourceRestic interface {
	ListSnapshots(context.Context, string, int) (resticservice.SnapshotList, error)
	SelectSnapshot(context.Context, string, string) (resticservice.Snapshot, error)
}

type multiSourceResticStaging interface {
	Stage(context.Context, string, string, string) (resticStagedArtifact, error)
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
	config, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	provider, err := openbao.New(config.openBaoAddress, config.openBaoToken)
	if err != nil {
		return errors.New("could not initialize configured OpenBao secret provider")
	}
	maxBytes := resticDefaultArtifactMax
	if value := getenv(resticArtifactMaxEnv); value != "" {
		maxBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil || maxBytes < 1 || maxBytes > resticHardArtifactMax {
			return errors.New("invalid configured artifact size limit")
		}
	}
	stageDirectory := strings.TrimSpace(getenv(resticStageDirEnv))
	if stageDirectory == "" {
		stageDirectory = staging.DefaultDirectory
	}
	artifactStore, err := staging.New(stageDirectory, maxBytes)
	if err != nil {
		return errors.New("could not initialize configured artifact staging directory")
	}
	tempDirectory := filepath.Join(stageDirectory, "tmp")
	cacheDirectory := filepath.Join(stageDirectory, "restic-cache")
	for _, directory := range []string{tempDirectory, cacheDirectory} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return errors.New("could not initialize artifact temporary directory")
		}
	}
	gateway, err := newResticSourceGateway(provider, artifactStore, artifact.Limits{
		MaxInputBytes:     maxBytes,
		MaxExpandedBytes:  maxBytes,
		MaxTemporaryBytes: maxBytes,
		TempDir:           tempDirectory,
	}, maxBytes, cacheDirectory)
	if err != nil {
		return errors.New("could not initialize Restic source gateway")
	}
	return serve(ctx, gateway, gateway, stdin, stdout)
}

func serve(ctx context.Context, instance multiSourceRestic, stager multiSourceResticStaging, stdin io.ReadCloser, stdout io.WriteCloser) error {
	server := newMCPServer(instance, stager)
	if err := server.Run(ctx, &mcp.IOTransport{
		Reader: newBoundedFrameReader(stdin, maxMCPFrameSize),
		Writer: stdout,
	}); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("MCP stdio server failed")
	}
	return nil
}
