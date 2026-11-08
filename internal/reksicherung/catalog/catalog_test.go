package catalog

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/config"
)

func TestFromConfigProducesDeterministicSecretFreeInventory(t *testing.T) {
	source := validSource(t)
	second := source.Backups[0]
	second.ID = "another-source"
	second.BackupName = "another-backup"
	source.Backups = append(source.Backups, second)

	inventory, err := FromConfig(source, "configs/catalog.yaml")
	if err != nil {
		t.Fatalf("FromConfig() error = %v", err)
	}
	if got := inventory.Backups[0].ID; got != "another-source" {
		t.Fatalf("inventory order starts with %q, want another-source", got)
	}
	if got := inventory.Backups[0].SourceLocation; got != "configs/catalog.yaml#backups/another-source" {
		t.Fatalf("source location = %q", got)
	}
	if len(inventory.Backups[0].Checks) != 3 || inventory.Backups[0].Checks[2] != "semantic-review" {
		t.Fatalf("configured checks = %v", inventory.Backups[0].Checks)
	}
	if len(inventory.Backups[0].ExpectedEmptyTables) != 1 ||
		inventory.Backups[0].ExpectedEmptyTables[0] != "dbo.AuditLog" {
		t.Fatalf("expected-empty tables = %v", inventory.Backups[0].ExpectedEmptyTables)
	}

	firstJSON, err := inventory.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	secondJSON, err := inventory.Marshal()
	if err != nil {
		t.Fatalf("Marshal() second call error = %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("Marshal() differs across calls:\n%s\n%s", firstJSON, secondJSON)
	}

	for _, forbidden := range []string{
		"openbao://",
		"storage.example.test",
		"accessKey",
		"secretKey",
		"password",
	} {
		if strings.Contains(string(firstJSON), forbidden) {
			t.Errorf("inventory leaked forbidden field/value %q: %s", forbidden, firstJSON)
		}
	}

	var decoded Inventory
	if err := json.Unmarshal(firstJSON, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.Kind != Kind || len(decoded.Backups) != 2 {
		t.Fatalf("decoded inventory = %#v", decoded)
	}
}

func TestFromConfigRejectsInvalidCatalog(t *testing.T) {
	source := validSource(t)
	source.Backups[0].Restore.MemoryMiB = 0
	if _, err := FromConfig(source, "catalog.yaml"); err == nil {
		t.Fatal("FromConfig() accepted invalid source")
	}
}

func TestFromConfigRejectsUnsafeSourceLocations(t *testing.T) {
	for _, location := range []string{"", "/etc/catalog.yaml", "../catalog.yaml", "dir//catalog.yaml"} {
		if _, err := FromConfig(validSource(t), location); err == nil {
			t.Errorf("FromConfig(source, %q) succeeded", location)
		}
	}
}

func TestMarshalRejectsInvalidInventory(t *testing.T) {
	tests := []Inventory{
		{APIVersion: "v2", Kind: Kind, Backups: []Entry{{ID: "a"}}},
		{APIVersion: APIVersion, Kind: Kind},
	}
	for _, test := range tests {
		if _, err := test.Marshal(); err == nil {
			t.Errorf("Marshal(%#v) succeeded", test)
		}
	}
}

func validSource(t *testing.T) config.Catalog {
	t.Helper()
	const yaml = `apiVersion: reksicherung.io/v1alpha1
kind: BackupCatalog
backups:
  - id: source
    provider: restic
    repositoryPasswordRef: openbao://secret/reksicherung/restic#repository-password
    backupName: production
    backupPath: /var/opt/mssql/backup/production.bak
    database:
      type: sqlserver
      name: App
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
      cpuMilli: 1000
      memoryMiB: 2048
      timeout: 1h
    validation:
      semanticReview: true
      expectedEmptyTables:
        - dbo.AuditLog
    cleanup: delete
`
	source, err := config.Decode([]byte(yaml))
	if err != nil {
		t.Fatalf("config.Decode() error = %v", err)
	}
	return source
}
