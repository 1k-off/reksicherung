package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets/openbao"
)

func runOpenBao(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printOpenBaoUsage(stderr)
		return errors.New("openbao subcommand is required")
	}
	switch args[0] {
	case "health":
		return runOpenBaoHealth(ctx, args[1:], stdout, stderr)
	case "kv":
		return runOpenBaoKV(ctx, args[1:], stdout, stderr)
	case "approle":
		return runOpenBaoAppRole(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printOpenBaoUsage(stdout)
		return nil
	default:
		printOpenBaoUsage(stderr)
		return fmt.Errorf("unknown openbao subcommand %q", args[0])
	}
}

func runOpenBaoHealth(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("openbao health", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	provider, err := openBaoProvider(false)
	if err != nil {
		return err
	}
	health, err := provider.Health(ctx)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(health)
}

func runOpenBaoKV(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: reksicherung openbao kv get|put|list …")
		return errors.New("openbao kv action is required")
	}
	switch args[0] {
	case "get":
		return runOpenBaoKVGet(ctx, args[1:], stdout, stderr)
	case "put":
		return runOpenBaoKVPut(ctx, args[1:], stdout, stderr)
	case "list":
		return runOpenBaoKVList(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown openbao kv action %q", args[0])
	}
}

func runOpenBaoKVGet(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("openbao kv get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mount := fs.String("mount", secrets.DefaultMount, "KV v2 mount")
	pathFlag := fs.String("path", "", "secret path under mount (e.g. reksicherung/sources/demo)")
	key := fs.String("key", "", "optional single field to print")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*pathFlag) == "" {
		fmt.Fprintln(stderr, "usage: reksicherung openbao kv get -path <path> [-key <field>] [-mount secret]")
		return errors.New("openbao kv get requires -path")
	}
	provider, err := openBaoProvider(true)
	if err != nil {
		return err
	}
	data, err := provider.KVGet(ctx, *mount, *pathFlag)
	if err != nil {
		return err
	}
	if *key != "" {
		value, ok := data[*key]
		if !ok {
			return openbao.ErrNotFound
		}
		fmt.Fprintln(stdout, value)
		return nil
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

func runOpenBaoKVPut(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("openbao kv put", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mount := fs.String("mount", secrets.DefaultMount, "KV v2 mount")
	pathFlag := fs.String("path", "", "secret path under mount")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fields := fs.Args()
	if strings.TrimSpace(*pathFlag) == "" || len(fields) == 0 {
		fmt.Fprintln(stderr, "usage: reksicherung openbao kv put -path <path> KEY=VALUE [KEY=VALUE…]")
		return errors.New("openbao kv put requires -path and KEY=VALUE pairs")
	}
	data := make(map[string]string, len(fields))
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" {
			return fmt.Errorf("invalid KEY=VALUE pair %q", field)
		}
		data[key] = value
	}
	provider, err := openBaoProvider(true)
	if err != nil {
		return err
	}
	if err := provider.KVPut(ctx, *mount, *pathFlag, data); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %d field(s) to %s/%s\n", len(data), *mount, *pathFlag)
	return nil
}

func runOpenBaoKVList(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("openbao kv list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mount := fs.String("mount", secrets.DefaultMount, "KV v2 mount")
	pathFlag := fs.String("path", "reksicherung", "secret path prefix to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	provider, err := openBaoProvider(true)
	if err != nil {
		return err
	}
	keys, err := provider.KVList(ctx, *mount, *pathFlag)
	if err != nil {
		return err
	}
	for _, key := range keys {
		fmt.Fprintln(stdout, key)
	}
	return nil
}

func runOpenBaoAppRole(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: reksicherung openbao approle ensure|verify …")
		return errors.New("openbao approle action is required")
	}
	switch args[0] {
	case "ensure":
		return runOpenBaoAppRoleEnsure(ctx, args[1:], stdout, stderr)
	case "verify":
		return runOpenBaoAppRoleVerify(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown openbao approle action %q", args[0])
	}
}

func runOpenBaoAppRoleEnsure(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("openbao approle ensure", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sourceID := fs.String("source-id", "", "catalog / OpenBao source id")
	includeDefaults := fs.Bool("include-defaults", true, "allow read of secret/reksicherung/defaults")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*sourceID) == "" {
		fmt.Fprintln(stderr, "usage: reksicherung openbao approle ensure -source-id <id> [-include-defaults=true]")
		return errors.New("openbao approle ensure requires -source-id")
	}
	provider, err := openBaoProvider(true)
	if err != nil {
		return err
	}
	creds, err := provider.EnsureBackupAppRole(ctx, *sourceID, *includeDefaults)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]string{
		"role_name": creds.RoleName,
		"policy":    creds.Policy,
		"role_id":   creds.RoleID,
		"secret_id": creds.SecretID,
	})
}

func runOpenBaoAppRoleVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("openbao approle verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sourceID := fs.String("source-id", "", "catalog / OpenBao source id")
	roleID := fs.String("role-id", "", "AppRole role_id")
	secretID := fs.String("secret-id", "", "AppRole secret_id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*sourceID) == "" || strings.TrimSpace(*roleID) == "" || strings.TrimSpace(*secretID) == "" {
		fmt.Fprintln(stderr, "usage: reksicherung openbao approle verify -source-id <id> -role-id <id> -secret-id <id>")
		return errors.New("openbao approle verify requires -source-id, -role-id, and -secret-id")
	}
	provider, err := openBaoProvider(false)
	if err != nil {
		return err
	}
	if err := provider.VerifyBackupAppRole(ctx, *sourceID, *roleID, *secretID); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "ok")
	return nil
}

func openBaoProvider(requireToken bool) (*openbao.Provider, error) {
	addr := firstNonEmpty(
		os.Getenv("REKSICHERUNG_OPENBAO_ADDR"),
		os.Getenv("BAO_ADDR"),
		os.Getenv("VAULT_ADDR"),
	)
	token := firstNonEmpty(
		os.Getenv("REKSICHERUNG_OPENBAO_TOKEN"),
		os.Getenv("BAO_TOKEN"),
		os.Getenv("VAULT_TOKEN"),
	)
	if addr == "" {
		return nil, errors.New("openbao address required (REKSICHERUNG_OPENBAO_ADDR or BAO_ADDR)")
	}
	if requireToken && token == "" {
		return nil, errors.New("openbao token required (REKSICHERUNG_OPENBAO_TOKEN or BAO_TOKEN)")
	}
	return openbao.New(addr, token)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func printOpenBaoUsage(writer io.Writer) {
	fmt.Fprintln(writer, `OpenBao provisioning helpers (KV + per-source AppRole)

Usage:
  reksicherung openbao health
  reksicherung openbao kv get  -path <path> [-key <field>] [-mount secret]
  reksicherung openbao kv put  -path <path> KEY=VALUE [KEY=VALUE…]
  reksicherung openbao kv list -path <prefix>
  reksicherung openbao approle ensure -source-id <id> [-include-defaults]
  reksicherung openbao approle verify -source-id <id> -role-id <id> -secret-id <id>

Environment:
  REKSICHERUNG_OPENBAO_ADDR or BAO_ADDR (required)
  REKSICHERUNG_OPENBAO_TOKEN or BAO_TOKEN (required except health/verify login)
  REKSICHERUNG_OPENBAO_PLAINTEXT=1 allows http:// to non-loopback (lab only)

AppRole naming: role/policy backup-<source_id>, policy scoped to
secret/reksicherung/sources/<source_id> (+ optional defaults).
Copy role_id/secret_id to the backup host (mode 0600); resolve refs at runtime.`)
}
