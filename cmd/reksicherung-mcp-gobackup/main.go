// Command reksicherung-mcp-gobackup serves bounded GoBackup archive discovery
// tools over MCP stdio.
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

	gobackup "github.com/1k-off/reksicherung/internal/reksicherung/mcp/gobackup"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/artifact"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets/openbao"
	"github.com/1k-off/reksicherung/internal/reksicherung/storage/s3"
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
	store, err := newStore(ctx, config, provider)
	if err != nil {
		return err
	}
	maxBytes := defaultArtifactMaxBytes
	if value := getenv(artifactMaxBytesEnv); value != "" {
		maxBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil || maxBytes < 1 || maxBytes > hardArtifactMaxBytes {
			return errors.New("invalid configured artifact size limit")
		}
	}
	stageDirectory := getenv(artifactStageDirEnv)
	artifactStore, err := staging.New(stageDirectory, maxBytes)
	if err != nil {
		return errors.New("could not initialize configured artifact staging directory")
	}
	service, err := gobackup.New(store, gobackup.Config{
		Prefix:         config.prefix,
		PageSize:       100,
		MaxBackups:     1000,
		MaxPages:       10,
		MaxObjectBytes: maxBytes,
	})
	if err != nil {
		return errors.New("could not initialize GoBackup archive discovery")
	}
	stager, err := newBackupStager(service, artifactStore, artifact.Limits{
		MaxInputBytes: maxBytes, MaxExpandedBytes: maxBytes, MaxTemporaryBytes: maxBytes,
	})
	if err != nil {
		return errors.New("could not initialize GoBackup artifact staging")
	}
	return serve(ctx, service, stager, stdin, stdout)
}

func serve(ctx context.Context, service archiveService, stager stagingService, stdin io.ReadCloser, stdout io.WriteCloser) error {
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

func newStore(ctx context.Context, config serverConfig, provider secrets.SecretProvider) (*s3.Client, error) {
	accessKey, err := resolveCredential(ctx, provider, config.accessKeyRef)
	if err != nil {
		return nil, errors.New("could not resolve configured S3 access-key secret")
	}
	defer clear(accessKey)
	secretKey, err := resolveCredential(ctx, provider, config.secretKeyRef)
	if err != nil {
		return nil, errors.New("could not resolve configured S3 secret-key secret")
	}
	defer clear(secretKey)

	store, err := s3.New(s3.Config{
		Endpoint:        config.endpoint,
		Region:          config.region,
		Bucket:          config.bucket,
		AccessKeyID:     string(accessKey),
		SecretAccessKey: string(secretKey),
	})
	if err != nil {
		return nil, errors.New("invalid S3 object-storage configuration")
	}
	return store, nil
}

func resolveCredential(ctx context.Context, provider secrets.SecretProvider, rawReference string) ([]byte, error) {
	reference, err := secrets.ParseReference(rawReference)
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
