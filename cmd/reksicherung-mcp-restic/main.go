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
)

const (
	repositoryEnv   = "REKSICHERUNG_RESTIC_REPOSITORY"
	passwordEnv     = "REKSICHERUNG_RESTIC_PASSWORD"
	maxMCPFrameSize = 64 * 1024
)

var backendEnvironment = []string{
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"AWS_DEFAULT_REGION",
	"AWS_REGION",
	"B2_ACCOUNT_ID",
	"B2_ACCOUNT_KEY",
	"AZURE_ACCOUNT_NAME",
	"AZURE_ACCOUNT_KEY",
}

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
	instance, err := resticservice.New(resticservice.Options{
		Binary:       "restic",
		Repository:   config.repository,
		Password:     config.password,
		Environment:  config.environment,
		Timeout:      15 * time.Second,
		MaxSnapshots: 100,
	})
	if err != nil {
		return errors.New("could not initialize Restic snapshot discovery")
	}
	return serve(ctx, instance, stdin, stdout)
}

type serverConfig struct {
	repository  string
	password    string
	environment []string
}

func loadConfig(getenv func(string) string) (serverConfig, error) {
	repository, password := getenv(repositoryEnv), getenv(passwordEnv)
	if repository == "" || password == "" {
		return serverConfig{}, fmt.Errorf("%s and %s must be set", repositoryEnv, passwordEnv)
	}
	environment := make([]string, 0, len(backendEnvironment))
	for _, name := range backendEnvironment {
		if value := getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return serverConfig{
		repository: repository, password: password, environment: environment,
	}, nil
}
