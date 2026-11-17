package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestCLIConfiggenResticprofile(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "input.yaml")
	outPath := filepath.Join(dir, "restic.yml")
	bindingsPath := filepath.Join(dir, "bindings.json")
	inputYAML := `project:
  name: demo-app
storage:
  type: s3
  bucket: backups-general
  endpoint: https://objects.example.test
  path_prefix: demo-app
  access_key_id_ref: openbao://secret/reksicherung/object-store#access-key
  secret_access_key_ref: openbao://secret/reksicherung/object-store#secret-key
schedules:
  cron: "0 5 * * *"
  healthcheck_url_ref: openbao://secret/reksicherung/healthchecks#url
archive:
  includes: [/var/lib/app]
encryption:
  enabled: true
  password_ref: openbao://secret/reksicherung/encryption#password
`
	if err := os.WriteFile(inPath, []byte(inputYAML), 0o600); err != nil {
		t.Fatalf("WriteFile input: %v", err)
	}

	secrets := map[string]string{
		"/v1/secret/data/reksicherung/object-store": `{"data":{"data":{"access-key":"AKIA_TEST","secret-key":"SECRET_TEST"}}}`,
		"/v1/secret/data/reksicherung/healthchecks": `{"data":{"data":{"url":"https://hc.example/check"}}}`,
		"/v1/secret/data/reksicherung/encryption":   `{"data":{"data":{"password":"restic-pass"}}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := secrets[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	defer server.Close()
	t.Setenv("REKSICHERUNG_OPENBAO_ADDR", server.URL)
	t.Setenv("REKSICHERUNG_OPENBAO_TOKEN", "test-token")
	t.Setenv("REKSICHERUNG_OPENBAO_PLAINTEXT", "1")

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"configgen", "-provider", "resticprofile",
		"-in", inPath, "-out", outPath, "-bindings", bindingsPath,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("configgen error = %v stderr=%q", err, stderr.String())
	}
	safePath, unsafePath := configgenOutputPaths(outPath)
	safeOut, err := os.ReadFile(safePath)
	if err != nil {
		t.Fatalf("read safe out: %v", err)
	}
	if !strings.Contains(string(safeOut), "version:") || !strings.Contains(string(safeOut), "$REKSICHERUNG_ENCRYPTION_PASSWORD") {
		t.Fatalf("unexpected safe output: %s", safeOut)
	}
	unsafeOut, err := os.ReadFile(unsafePath)
	if err != nil {
		t.Fatalf("read unsafe out: %v", err)
	}
	if !strings.Contains(string(unsafeOut), "restic-pass") || strings.Contains(string(unsafeOut), "$REKSICHERUNG_") {
		t.Fatalf("unexpected unsafe output: %s", unsafeOut)
	}
	bindings, err := os.ReadFile(bindingsPath)
	if err != nil {
		t.Fatalf("read bindings: %v", err)
	}
	if !strings.Contains(string(bindings), "REKSICHERUNG_ENCRYPTION_PASSWORD") {
		t.Fatalf("bindings = %s", bindings)
	}
}

func TestConfiggenOutputPaths(t *testing.T) {
	safe, unsafe := configgenOutputPaths(filepath.Join("out", "restic.yml"))
	if !strings.HasSuffix(safe, filepath.Join("out", "restic.safe.yml")) {
		t.Fatalf("safe = %q", safe)
	}
	if !strings.HasSuffix(unsafe, filepath.Join("out", "restic.unsafe.yml")) {
		t.Fatalf("unsafe = %q", unsafe)
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
