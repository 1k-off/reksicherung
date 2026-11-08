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

	inventory, err := FromConfig(source)
	if err != nil {
		t.Fatalf("FromConfig() error = %v", err)
	}
	if got := inventory.Backups[0].ID; got != "another-source" {
		t.Fatalf("inventory order starts with %q, want another-source", got)
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
	if _, err := FromConfig(source); err == nil {
		t.Fatal("FromConfig() accepted invalid source")
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
    backupName: production
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
    cleanup: delete
`
	source, err := config.Decode([]byte(yaml))
	if err != nil {
		t.Fatalf("config.Decode() error = %v", err)
	}
	return source
}
