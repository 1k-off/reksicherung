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
	case "hold":
		return runHold(ctx, args[1:], stdout, stderr)
	case "reconcile":
		return runReconcile(ctx, args[1:], stderr)
	case "configgen":
		return runConfiggen(args[1:], stdout, stderr)
	case "openbao":
		return runOpenBao(ctx, args[1:], stdout, stderr)
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
  reksicherung validate  -config <catalog.yaml>
  reksicherung validate  -config <catalog.yaml> -production
  reksicherung catalog   -config <catalog.yaml> [-source-ref <relative-path>] [-production]
  reksicherung configgen -provider gobackup|resticprofile -in <input.yaml> -out <base.yml> [-bindings <bindings.json>]
  reksicherung openbao   health|kv|approle …
  reksicherung hold      -registry <runs.db> -run-id <id> -database-type sqlserver|mysql -verdict pass|fail|inconclusive
  reksicherung cleanup   -registry <runs.db> -run-id <held-run>
  reksicherung reconcile -registry <runs.db> [-interval 30s] [-once]
  reksicherung help

Commands:
  validate   Validate a versioned backup catalog
  catalog    Write the secret-free backup inventory as JSON to stdout
  configgen  Render GoBackup or resticprofile YAML (safe placeholders + unsafe OpenBao-resolved)
  openbao    Provision/verify OpenBao KV and per-source AppRoles
  hold       Register a hold_for_review run with its database type (required for cleanup routing)
  cleanup    Drop a hold_for_review run-scoped database (services stay running)
  reconcile  Retry expired automatic database cleanup leases

The -production flag requires HTTPS for configured storage endpoints when used.
cleanup/reconcile require SQL Server and OpenBao environment variables used by
the restore MCP; they drop databases only and leave operator services untouched.
configgen writes <base>.safe.yml (placeholders) and <base>.unsafe.yml (secrets
resolved from OpenBao via REKSICHERUNG_OPENBAO_ADDR/TOKEN). Prefer safe + runtime
AppRole on hosts; treat unsafe as local/operator-only (mode 0600, never commit).`)
}
