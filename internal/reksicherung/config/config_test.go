package config

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/plan"
)

const validCatalogYAML = `apiVersion: reksicherung.1node.xyz/v1alpha2
kind: BackupCatalog
backups:
  - id: application-production
    provider: gobackup
    database:
      type: sqlserver
      name: Application
    selection:
      policy: latest
    validation:
      semanticReview: true
      expectedEmptyTables:
        - dbo.AuditLog
    cleanup: delete
`

func TestDecodeValidCatalog(t *testing.T) {
	catalog, err := Decode([]byte(validCatalogYAML))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if catalog.APIVersion != APIVersion || len(catalog.Backups) != 1 {
		t.Fatalf("Decode() = %#v", catalog)
	}
	backup := catalog.Backups[0]
	if backup.ID != "application-production" || backup.Selection.Policy != "latest" {
		t.Fatalf("unexpected backup = %#v", backup)
	}
}

func TestDecodeRepositoryExampleCatalog(t *testing.T) {
	data, err := os.ReadFile("../../../examples/catalog.example.yaml")
	if err != nil {
		t.Fatalf("ReadFile(example) error = %v", err)
	}
	catalog, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode(example) error = %v", err)
	}
	if len(catalog.Backups) < 2 {
		t.Fatalf("example should contain at least two sources, got %d", len(catalog.Backups))
	}
}

func TestDecodeValidFixtureCatalog(t *testing.T) {
	data, err := os.ReadFile("../../../docs/schemas/fixtures/valid/catalog.two-sources.yaml")
	if err != nil {
		t.Fatalf("ReadFile(fixture) error = %v", err)
	}
	if _, err := Decode(data); err != nil {
		t.Fatalf("Decode(fixture) error = %v", err)
	}
}

func TestRejectsStorageBlock(t *testing.T) {
	data, err := os.ReadFile("../../../docs/schemas/fixtures/invalid/catalog.with-storage.yaml")
	if err != nil {
		t.Fatalf("ReadFile(invalid fixture) error = %v", err)
	}
	if _, err := Decode(data); err == nil {
		t.Fatal("Decode(storage block) succeeded, want rejection")
	}
}

func TestResticBackupPathAllowsPlainAndArchiveFiles(t *testing.T) {
	catalog, err := Decode([]byte(validCatalogYAML))
	if err != nil {
		t.Fatal(err)
	}
	backup := catalog.Backups[0]
	backup.Provider = "restic"
	for _, backupPath := range []string{
		"/mnt/backups/database.bak",
		"/mnt/backups/database.bak.zip",
		"/mnt/backups/nested/nightly.tar.gz",
		"/mnt/backups/nested/database.bacpac",
	} {
		backup.BackupPath = backupPath
		catalog.Backups[0] = backup
		if err := catalog.Validate(); err != nil {
			t.Errorf("Validate(backupPath %q) error = %v, want success", backupPath, err)
		}
	}
}

func TestCatalogRejectsMoreSourcesThanRunLimit(t *testing.T) {
	catalog, err := Decode([]byte(validCatalogYAML))
	if err != nil {
		t.Fatalf("Decode(valid catalog) error = %v", err)
	}
	source := catalog.Backups[0]
	catalog.Backups = make([]BackupSource, plan.MaxSelectedDatabases+1)
	for index := range catalog.Backups {
		item := source
		item.ID = fmt.Sprintf("src-%04d", index)
		catalog.Backups[index] = item
	}
	if err := catalog.Validate(); err == nil {
		t.Fatal("Validate() succeeded for oversized catalog")
	}
}

func TestDecodeRejectsUnknownAPIVersion(t *testing.T) {
	raw := strings.Replace(validCatalogYAML, APIVersion, "reksicherung.io/v1alpha1", 1)
	if _, err := Decode([]byte(raw)); err == nil {
		t.Fatal("Decode(v1alpha1) succeeded, want rejection")
	}
}
