// Command reksicherung-mcp-restic serves read-only Restic snapshot metadata
// tools over MCP stdio.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	resticservice "github.com/1k-off/reksicherung/internal/reksicherung/mcp/restic"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets/openbao"
)

const (
	repositoryEnv   = "REKSICHERUNG_RESTIC_REPOSITORY"
	maxMCPFrameSize = 64 * 1024
)

type service interface {
	ListSnapshots(context.Context, int) (resticservice.SnapshotList, error)
	SelectSnapshot(context.Context, string) (resticservice.Snapshot, error)
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
	instance, err := newResticService(ctx, config, provider, nil)
	if err != nil {
		return err
	}
	return serve(ctx, instance, stdin, stdout)
}

func newResticService(ctx context.Context, config serverConfig, provider secrets.SecretProvider, runner resticservice.Runner) (*resticservice.Service, error) {
	password, err := resolveSecret(ctx, provider, config.passwordRef)
	if err != nil {
		return nil, errors.New("could not resolve configured Restic repository-password secret")
	}
	defer clear(password)

	environment := append([]string(nil), config.environment...)
	for _, backend := range config.backendSecrets {
		value, err := resolveSecret(ctx, provider, backend.reference)
		if err != nil {
			return nil, fmt.Errorf("could not resolve configured Restic %s secret", backend.name)
		}
		environment = append(environment, backend.name+"="+string(value))
		clear(value)
	}

	options := resticservice.Options{
		Binary:       "restic",
		Repository:   config.repository,
		Password:     string(password),
		Environment:  environment,
		Timeout:      15 * time.Second,
		MaxSnapshots: 100,
	}
	var instance *resticservice.Service
	if runner == nil {
		instance, err = resticservice.New(options)
	} else {
		instance, err = resticservice.NewWithRunner(runner, options)
	}
	if err != nil {
		return nil, errors.New("could not initialize Restic snapshot discovery")
	}
	return instance, nil
}

func resolveSecret(ctx context.Context, provider secrets.SecretProvider, rawReference string) ([]byte, error) {
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
