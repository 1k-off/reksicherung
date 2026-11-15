package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/runregistry"
)

func TestHoldRecordsDatabaseType(t *testing.T) {
	for _, dbType := range []runregistry.DatabaseType{runregistry.DatabaseTypeMySQL, runregistry.DatabaseTypeSQLServer} {
		t.Run(string(dbType), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runs.db")
			var stdout, stderr bytes.Buffer
			args := []string{"hold", "-registry", path, "-run-id", "held-1", "-database-type", string(dbType), "-verdict", "pass"}
			if err := run(context.Background(), args, &stdout, &stderr); err != nil {
				t.Fatalf("hold error = %v", err)
			}
			store, err := runregistry.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			record, err := store.Get(context.Background(), "held-1")
			if err != nil {
				t.Fatal(err)
			}
			if record.DatabaseType != dbType || record.DatabaseName == "" ||
				record.CleanupMode != runregistry.CleanupHold || record.CleanupStatus != runregistry.CleanupRetained {
				t.Fatalf("record = %#v", record)
			}
		})
	}
}

func TestHoldRequiresExplicitValidDatabaseType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	tests := [][]string{
		{"hold", "-registry", path, "-run-id", "h", "-verdict", "pass"},
		{"hold", "-registry", path, "-run-id", "h", "-database-type", "postgres", "-verdict", "pass"},
		{"hold", "-registry", path, "-run-id", "h", "-database-type", "mysql"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), args, &stdout, &stderr)
		if err == nil || !(strings.Contains(err.Error(), "usage: reksicherung hold") || strings.Contains(err.Error(), "invalid -database-type")) {
			t.Fatalf("run(%v) error = %v, want usage/invalid type error", args, err)
		}
	}
}
