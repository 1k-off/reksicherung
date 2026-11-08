package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return errors.New("a command is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	switch args[0] {
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "catalog":
		return runCatalog(args[1:], stdout, stderr)
	case "cleanup":
		return runCleanup(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, `Reksicherung backup restore verification

Usage:
  reksicherung validate -config <catalog.yaml>
  reksicherung validate -config <catalog.yaml> -production
  reksicherung catalog  -config <catalog.yaml> [-source-ref <relative-path>] [-production]
  reksicherung cleanup  -registry <runs.db> -run-id <held-run>
  reksicherung help

Commands:
  validate   Validate a versioned backup catalog
  catalog    Write the secret-free Airon inventory as JSON to stdout
  cleanup    Remove a held-for-review restore container

The -production flag requires restore images to be pinned by sha256 digest.`)
}
