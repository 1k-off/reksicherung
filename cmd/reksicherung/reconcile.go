package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/runregistry"
	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container/podman"
)

func runReconcile(ctx context.Context, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	registryPath := flags.String("registry", "", "path to the run registry database")
	interval := flags.Duration("interval", 30*time.Second, "interval between cleanup reconciliation passes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("reconcile does not accept positional arguments")
	}
	if *registryPath == "" {
		return errors.New("reconcile requires -registry")
	}
	if *interval < time.Second || *interval > 24*time.Hour {
		return errors.New("reconcile interval must be between 1 second and 24 hours")
	}

	store, err := runregistry.Open(*registryPath)
	if err != nil {
		return fmt.Errorf("open run registry: %w", err)
	}
	logger := log.New(stderr, "[REKSICHERUNG]: ", log.LstdFlags)
	reconciler := runregistry.Reconciler{
		Store:      store,
		Containers: podman.New(""),
	}
	runErr := reconciler.Run(ctx, *interval, func(outcome runregistry.CleanupOutcome) {
		if outcome.Err != nil {
			logger.Printf("cleanup attempt for run %s failed; retry is scheduled", outcome.RunID)
			return
		}
		logger.Printf("cleanup completed for run %s", outcome.RunID)
	})
	closeErr := store.Close()
	return errors.Join(runErr, closeErr)
}
