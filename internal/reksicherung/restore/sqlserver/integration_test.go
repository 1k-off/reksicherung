//go:build integration

package sqlserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
	podmanruntime "github.com/1k-off/reksicherung/internal/reksicherung/runtime/container/podman"
)

func TestPodmanSQLServerOperations(t *testing.T) {
	if os.Getenv("REKSICHERUNG_PODMAN_INTEGRATION") != "1" {
		t.Skip("set REKSICHERUNG_PODMAN_INTEGRATION=1 to start SQL Server under Podman")
	}

	const password = "Aa1!restore-integration-only"
	suffix := integrationSuffix(t)
	runID := "sqlserver-integration-" + suffix
	database, err := DatabaseNameForRun(runID)
	if err != nil {
		t.Fatalf("derive isolated database name: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	runtime := podmanruntime.New("")
	target, err := runtime.Start(ctx, container.Spec{
		RunID:       runID,
		Image:       "mcr.microsoft.com/mssql/server:2022-latest",
		CPUs:        2,
		MemoryBytes: 3 * 1024 * 1024 * 1024,
		DiskBytes:   1 * 1024 * 1024 * 1024,
		Timeout:     6 * time.Minute,
		Environment: []container.EnvironmentVariable{
			{Name: "ACCEPT_EULA", Value: "Y"},
			{Name: "MSSQL_SA_PASSWORD", Value: password},
		},
	})
	if err != nil {
		t.Fatalf("start SQL Server container (check Podman image access and resource quotas): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if err := runtime.Remove(cleanupCtx, target); err != nil {
			t.Errorf("remove SQL Server container: %v", err)
		}
	})

	operations, err := New(runtime, target, database, password)
	if err != nil {
		t.Fatalf("construct SQL Server operations: %v", err)
	}
	if err := operations.WaitReady(ctx); err != nil {
		t.Fatalf("wait for SQL Server readiness: %v", err)
	}

	createQuery := "CREATE DATABASE [" + database + "];"
	if _, err := runtime.ExecWithEnvironment(ctx, target, []string{
		sqlcmdPath, "-b", "-C", "-S", "localhost", "-U", "sa", "-d", "master", "-Q", createQuery,
	}, []container.EnvironmentVariable{{Name: "SQLCMDPASSWORD", Value: password}}); err != nil {
		t.Fatalf("create isolated integration database: %v", err)
	}
	result, err := operations.CheckIntegrity(ctx)
	if err != nil {
		t.Fatalf("check integration database integrity: %v", err)
	}
	if !result.Passed || result.Database != database {
		t.Fatalf("integrity result = %#v", result)
	}
	if err := operations.DropDatabase(ctx); err != nil {
		t.Fatalf("drop isolated integration database: %v", err)
	}
}

func integrationSuffix(t *testing.T) string {
	t.Helper()
	random := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		t.Fatalf("generate integration identifier: %v", err)
	}
	return hex.EncodeToString(random)
}
