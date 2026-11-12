package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validCatalog = `apiVersion: reksicherung.1node.xyz/v1alpha2
kind: BackupCatalog
backups:
  - id: production
    provider: gobackup
    database:
      type: sqlserver
      name: Application
    selection:
      policy: latest
    validation:
      semanticReview: true
    cleanup: delete
`

func TestCLIValidate(t *testing.T) {
	path := writeConfig(t, validCatalog)
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"validate", "-config", path}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v, stderr = %q", err, stderr.String())
	}
	if got := stdout.String(); got != "valid catalog: 1 backup source(s)\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestCLICatalogEmitsOnlyInventory(t *testing.T) {
	path := writeConfig(t, validCatalog)
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"catalog", "-config", path}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v, stderr = %q", err, stderr.String())
	}
	for _, forbidden := range []string{"openbao://", "storage.example.test", "accessKey", "secretKey", "bucket"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Errorf("catalog output leaked %q: %s", forbidden, stdout.String())
		}
	}
	for _, required := range []string{
		`"kind":"BackupInventory"`,
		`"apiVersion":"reksicherung.1node.xyz/v1alpha2"`,
		`"id":"production"`,
		`"provider":"gobackup"`,
		`"sourceLocation":"catalog.yaml#backups/production"`,
		`"semantic-review"`,
	} {
		if !strings.Contains(stdout.String(), required) {
			t.Errorf("catalog output missing %q: %s", required, stdout.String())
		}
	}
}

func TestCLIProductionFlagStillValidates(t *testing.T) {
	path := writeConfig(t, validCatalog)
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"validate", "-config", path, "-production"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run(-production) error = %v, stderr = %q", err, stderr.String())
	}
}

func TestCLIRejectsMissingConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"validate"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("validate without -config succeeded")
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
