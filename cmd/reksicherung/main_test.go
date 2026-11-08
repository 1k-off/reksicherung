package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validCatalog = `apiVersion: reksicherung.io/v1alpha1
kind: BackupCatalog
backups:
  - id: production
    provider: gobackup
    backupName: production
    database:
      type: sqlserver
      name: Application
    storage:
      type: s3
      endpoint: https://storage.example.test
      bucket: backups
      region: eu-central
      credentials:
        accessKeyRef: openbao://secret/reksicherung/s3#access-key
        secretKeyRef: openbao://secret/reksicherung/s3#secret-key
    selection:
      policy: latest
    restore:
      image: mcr.microsoft.com/mssql/server:2022-latest
      cpuMilli: 2000
      memoryMiB: 4096
      timeout: 2h
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

func TestCLICatalogEmitsOnlyAironInventory(t *testing.T) {
	path := writeConfig(t, validCatalog)
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"catalog", "-config", path}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v, stderr = %q", err, stderr.String())
	}
	for _, forbidden := range []string{"openbao://", "storage.example.test", "accessKey", "secretKey"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Errorf("catalog output leaked %q: %s", forbidden, stdout.String())
		}
	}
	for _, required := range []string{
		`"kind":"BackupInventory"`,
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

func TestCLIProductionRejectsMutableRestoreImage(t *testing.T) {
	path := writeConfig(t, validCatalog)
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"validate", "-config", path, "-production"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "pinned by sha256") {
		t.Fatalf("run() error = %v, want production digest requirement", err)
	}
}

func TestCLIRejectsMissingConfigAndUnexpectedArguments(t *testing.T) {
	for _, args := range [][]string{
		{"validate"},
		{"catalog", "-config", "somewhere.yaml", "extra"},
		{"reconcile"},
		{"reconcile", "-registry", "runs.db", "-interval", "500ms"},
	} {
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), args, &stdout, &stderr); err == nil {
			t.Errorf("run(%v) succeeded", args)
		}
	}
}

func TestCLIRejectsOversizedConfig(t *testing.T) {
	path := writeConfig(t, strings.Repeat(" ", maxConfigBytes+1))
	_, err := loadCatalog(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("loadCatalog() error = %v, want size limit", err)
	}
}

func TestCLIHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{"help"}, &stdout, &stderr); err == nil {
		t.Fatal("run() succeeded with canceled context")
	}
}

func writeConfig(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.yaml")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
