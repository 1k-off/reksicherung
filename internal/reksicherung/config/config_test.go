package config

import (
	"os"
	"strings"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/plan"
)

const validCatalogYAML = `apiVersion: reksicherung.io/v1alpha1
kind: BackupCatalog
backups:
  - id: application-production
    provider: gobackup
    backupName: application-production
    database:
      type: sqlserver
      name: Application
    storage:
      type: s3
      endpoint: http://127.0.0.1:9000
      bucket: backups
      region: local
      prefix: application/production
      credentials:
        accessKeyRef: openbao://secret/reksicherung/minio#access-key
        secretKeyRef: openbao://secret/reksicherung/minio#secret-key
    selection:
      policy: latest
    restore:
      image: mcr.microsoft.com/mssql/server:2022-latest
      cpuMilli: 2000
      memoryMiB: 4096
      timeout: 2h
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
	data, err := os.ReadFile("../../../examples/catalog.yaml")
	if err != nil {
		t.Fatalf("ReadFile(example) error = %v", err)
	}
	catalog, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode(example) error = %v", err)
	}
	if got := catalog.Backups[0].RepositoryPasswordRef; got != "openbao://secret/reksicherung/restic#repository-password" {
		t.Fatalf("example repositoryPasswordRef = %q", got)
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
		catalog.Backups[index] = source
		catalog.Backups[index].ID = "database-" + strings.Repeat("x", index+1)
	}
	if err := catalog.Validate(); err == nil || !strings.Contains(err.Error(), "backup source limit") {
		t.Fatalf("Validate() error = %v, want source limit error", err)
	}
}

func TestDecodeRejectsMalformedOrAmbiguousDocuments(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "unknown field",
			data: strings.Replace(validCatalogYAML, "    provider:", "    password: should-not-be-accepted\n    provider:", 1),
			want: "invalid YAML document or unsupported field",
		},
		{
			name: "second document",
			data: validCatalogYAML + "---\nkind: another\n",
			want: "multiple YAML documents",
		},
		{
			name: "unsupported version",
			data: strings.Replace(validCatalogYAML, APIVersion, "reksicherung.io/v2", 1),
			want: "apiVersion",
		},
		{
			name: "literal secret field",
			data: strings.Replace(validCatalogYAML, "        accessKeyRef:", "        accessKey: should-not-be-accepted\n        accessKeyRef:", 1),
			want: "invalid YAML document or unsupported field",
		},
		{
			name: "bad type does not echo value",
			data: strings.Replace(validCatalogYAML, "      memoryMiB: 4096", "      memoryMiB: raw-secret-sentinel", 1),
			want: "invalid YAML document or unsupported field",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode([]byte(test.data))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Decode() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCatalogValidationRejectsUnsafeOrIncompleteValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Catalog)
		want   string
	}{
		{
			name: "duplicate source IDs",
			mutate: func(catalog *Catalog) {
				catalog.Backups = append(catalog.Backups, catalog.Backups[0])
			},
			want: "duplicate backup id",
		},
		{
			name: "missing secret reference",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Storage.Credentials.SecretKeyRef = ""
			},
			want: "secretKeyRef",
		},
		{
			name: "plaintext secret reference",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Storage.Credentials.SecretKeyRef = "not-a-secret-reference"
			},
			want: "secretKeyRef",
		},
		{
			name: "Restic requires repository password reference",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Provider = "restic"
			},
			want: "repositoryPasswordRef",
		},
		{
			name: "Restic repository password must be a reference",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Provider = "restic"
				catalog.Backups[0].RepositoryPasswordRef = "plaintext-secret"
			},
			want: "repositoryPasswordRef",
		},
		{
			name: "Restic requires an exact backup path",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Provider = "restic"
				catalog.Backups[0].RepositoryPasswordRef = "openbao://secret/reksicherung/restic#repository-password"
			},
			want: "backupPath",
		},
		{
			name: "Restic path traversal",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Provider = "restic"
				catalog.Backups[0].RepositoryPasswordRef = "openbao://secret/reksicherung/restic#repository-password"
				catalog.Backups[0].BackupPath = "/var/backups/../private.bak"
			},
			want: "backupPath",
		},
		{
			name: "Restic glob path",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Provider = "restic"
				catalog.Backups[0].RepositoryPasswordRef = "openbao://secret/reksicherung/restic#repository-password"
				catalog.Backups[0].BackupPath = "/var/backups/*.bak"
			},
			want: "backupPath",
		},
		{
			name: "GoBackup rejects Restic backup path",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].BackupPath = "/var/backups/db.bak"
			},
			want: "only valid for Restic",
		},
		{
			name: "GoBackup rejects repository password reference",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].RepositoryPasswordRef = "openbao://secret/reksicherung/restic#repository-password"
			},
			want: "only valid for Restic",
		},
		{
			name: "remote insecure storage endpoint",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Storage.Endpoint = "http://storage.example.test"
			},
			want: "must use https",
		},
		{
			name: "credential-bearing endpoint",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Storage.Endpoint = "https://user:password@storage.example.test"
			},
			want: "without credentials",
		},
		{
			name: "unsafe path prefix",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Storage.Prefix = "backups/../other"
			},
			want: "dot, or parent",
		},
		{
			name: "invalid bucket",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Storage.Bucket = "192.0.2.10"
			},
			want: "valid S3 bucket",
		},
		{
			name: "bucket label ends with hyphen",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Storage.Bucket = "foo-.bar"
			},
			want: "valid S3 bucket",
		},
		{
			name: "bucket label starts with hyphen",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Storage.Bucket = "foo.-bar"
			},
			want: "valid S3 bucket",
		},
		{
			name: "invalid selection",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Selection = Selection{Policy: "id"}
			},
			want: "selection.id is required",
		},
		{
			name: "invalid resource limit",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Restore.MemoryMiB = 64
			},
			want: "memoryMiB",
		},
		{
			name: "unbounded restore timeout",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Restore.Timeout = "0"
			},
			want: "timeout",
		},
		{
			name: "runtime option used as image",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Restore.Image = "--network=host"
			},
			want: "runtime command-line option",
		},
		{
			name: "invalid expected-empty table reference",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Validation.ExpectedEmptyTables = []string{"dbo."}
			},
			want: "schema.table",
		},
		{
			name: "expected-empty table requires semantic review",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Validation.SemanticReview = false
			},
			want: "requires semanticReview",
		},
		{
			name: "unknown cleanup policy",
			mutate: func(catalog *Catalog) {
				catalog.Backups[0].Cleanup = "retain"
			},
			want: "cleanup",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog, err := Decode([]byte(validCatalogYAML))
			if err != nil {
				t.Fatalf("Decode(valid) error = %v", err)
			}
			test.mutate(&catalog)
			err = catalog.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCatalogValidationAcceptsExplicitBackupIDAndHTTPSEndpoint(t *testing.T) {
	catalog, err := Decode([]byte(validCatalogYAML))
	if err != nil {
		t.Fatal(err)
	}
	catalog.Backups[0].Selection = Selection{Policy: "id", ID: "backup:2025-04-21T10:30:00Z"}
	catalog.Backups[0].Storage.Endpoint = "https://storage.example.test"
	if err := catalog.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateProductionRequiresDigestPinnedImages(t *testing.T) {
	catalog, err := Decode([]byte(validCatalogYAML))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateProduction(); err == nil || !strings.Contains(err.Error(), "pinned by sha256") {
		t.Fatalf("ValidateProduction() error = %v, want digest requirement", err)
	}

	catalog.Backups[0].Restore.Image = "mcr.microsoft.com/mssql/server@sha256:" + strings.Repeat("a", 64)
	if err := catalog.ValidateProduction(); err != nil {
		t.Fatalf("ValidateProduction(digest) error = %v", err)
	}
	catalog.Backups[0].Restore.Image = "mcr.microsoft.com/mssql/server@sha256:invalid"
	if err := catalog.ValidateProduction(); err == nil {
		t.Fatal("ValidateProduction() accepted malformed digest")
	}
}
