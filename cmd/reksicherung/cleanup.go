package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/1k-off/reksicherung/internal/reksicherung/runregistry"
	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container/podman"
)

type cleanupStore interface {
	runregistryStore
	Close() error
}

type runregistryStore interface {
	Get(context.Context, string) (runregistry.Record, error)
	Update(context.Context, string, uint64, func(*runregistry.Record) error) (runregistry.Record, error)
}

type cleanupRuntime interface {
	Remove(context.Context, container.Container) error
}

func runCleanup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runCleanupWithDependencies(ctx, args, stdout, stderr,
		func(path string) (cleanupStore, error) {
			return runregistry.Open(path)
		},
		func() cleanupRuntime { return podman.New("") },
	)
}

func runCleanupWithDependencies(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	openStore func(string) (cleanupStore, error),
	newRuntime func() cleanupRuntime,
) error {
	flags := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	registryPath := flags.String("registry", "", "path to the run registry database")
	runID := flags.String("run-id", "", "ID of the held-for-review run")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("cleanup does not accept positional arguments")
	}
	if *registryPath == "" || *runID == "" {
		return errors.New("cleanup requires -registry and -run-id")
	}
	if openStore == nil || newRuntime == nil {
		return errors.New("cleanup dependencies are not configured")
	}

	store, err := openStore(*registryPath)
	if err != nil {
		return fmt.Errorf("open run registry: %w", err)
	}
	record, cleanupErr := runregistry.CleanupHeld(ctx, store, newRuntime(), *runID)
	closeErr := store.Close()
	if cleanupErr != nil || closeErr != nil {
		return errors.Join(cleanupErr, closeErr)
	}
	_, err = fmt.Fprintf(stdout, "cleaned run %s; container %s removed\n", record.ID, record.ContainerID)
	return err
}
