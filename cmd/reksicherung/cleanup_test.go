package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/runregistry"
)

func TestCLICleanupAndReconcileRequireFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "cleanup missing flags", args: []string{"cleanup"}, want: "usage: reksicherung cleanup"},
		{name: "reconcile missing registry", args: []string{"reconcile"}, want: "usage: reksicherung reconcile"},
		{name: "reconcile invalid interval via once missing registry", args: []string{"reconcile", "-once"}, want: "usage: reksicherung reconcile"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), test.args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run(%v) error = %v, want substring %q", test.args, err, test.want)
			}
		})
	}
}

func TestCLIHelpAdvertisesDatabaseCleanupNotContainers(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(help) error = %v", err)
	}
	text := strings.ToLower(stdout.String())
	for _, required := range []string{"cleanup", "reconcile", "hold_for_review"} {
		if !strings.Contains(text, required) {
			t.Errorf("help missing %q: %s", required, stdout.String())
		}
	}
	for _, forbidden := range []string{"podman", "docker compose", "remove container"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("help advertises container management %q: %s", forbidden, stdout.String())
		}
	}
}

type recordingDropper struct{ ids []string }

func (d *recordingDropper) DropDatabase(_ context.Context, runID string) error {
	d.ids = append(d.ids, runID)
	return nil
}

func TestLazyDropperBuildsOnFirstUseOnly(t *testing.T) {
	built := 0
	inner := &recordingDropper{}
	lazy := &lazyDropper{build: func(context.Context) (runregistry.DatabaseDropper, error) {
		built++
		return inner, nil
	}}
	if built != 0 {
		t.Fatalf("built before use = %d", built)
	}
	for _, id := range []string{"a", "b"} {
		if err := lazy.DropDatabase(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if built != 1 || len(inner.ids) != 2 {
		t.Fatalf("built=%d drops=%v, want 1 build and 2 drops", built, inner.ids)
	}
}

func TestLazyDropperPropagatesBuildError(t *testing.T) {
	lazy := &lazyDropper{build: func(context.Context) (runregistry.DatabaseDropper, error) {
		return nil, errors.New("boom")
	}}
	if err := lazy.DropDatabase(context.Background(), "a"); err == nil {
		t.Fatal("DropDatabase() error = nil")
	}
}

func TestLazyDropperRetriesAfterBuildError(t *testing.T) {
	builds := 0
	inner := &recordingDropper{}
	lazy := &lazyDropper{build: func(context.Context) (runregistry.DatabaseDropper, error) {
		builds++
		if builds == 1 {
			return nil, errors.New("transient")
		}
		return inner, nil
	}}
	if err := lazy.DropDatabase(context.Background(), "a"); err == nil {
		t.Fatal("first DropDatabase() error = nil, want build error")
	}
	if err := lazy.DropDatabase(context.Background(), "b"); err != nil {
		t.Fatalf("second DropDatabase() error = %v, want success after retry", err)
	}
	if err := lazy.DropDatabase(context.Background(), "c"); err != nil {
		t.Fatal(err)
	}
	if builds != 2 || len(inner.ids) != 2 || inner.ids[0] != "b" || inner.ids[1] != "c" {
		t.Fatalf("builds=%d drops=%v, want 2 builds (success cached) and drops [b c]", builds, inner.ids)
	}
}

func TestEngineDropperFromEnvDoesNotRequireUnusedEngineConfig(t *testing.T) {
	// No env at all: construction must succeed because engines initialize lazily.
	dropper := newEngineDropperFromEnv(func(string) string { return "" })
	record := runregistry.Record{ID: "run-1", DatabaseType: runregistry.DatabaseTypeMySQL}
	err := dropper.DropRecordDatabase(context.Background(), record)
	if err == nil || !strings.Contains(err.Error(), "MySQL") {
		t.Fatalf("mysql drop error = %v, want MySQL config error", err)
	}
	record.DatabaseType = ""
	err = dropper.DropRecordDatabase(context.Background(), record)
	if err == nil || !strings.Contains(err.Error(), "SQL cleanup") {
		t.Fatalf("legacy drop error = %v, want SQL Server config error", err)
	}
}

func TestLoadCleanupMySQLConfigDefaultsAndRequirements(t *testing.T) {
	if _, err := loadCleanupMySQLConfig(func(string) string { return "" }); err == nil {
		t.Fatal("missing OpenBao config: error = nil")
	}
	env := map[string]string{"REKSICHERUNG_OPENBAO_ADDR": "http://bao", "REKSICHERUNG_OPENBAO_TOKEN": "t"}
	config, err := loadCleanupMySQLConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if config.mysqlCmd != "mysql" || config.mysqlCheckCmd != "mysqlcheck" {
		t.Fatalf("defaults = %#v", config)
	}
}
