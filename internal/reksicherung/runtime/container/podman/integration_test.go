//go:build integration

package podman

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/runtime/container"
)

func TestSQLServerContainerCanBeStartedQueriedAndRemoved(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real SQL Server container")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	password := "Aa1!" + randomTestSuffix(t)
	runtime := New("")
	spec := container.Spec{
		RunID:       "integration-" + randomTestSuffix(t),
		Image:       "mcr.microsoft.com/mssql/server:2022-latest",
		CPUs:        2,
		MemoryBytes: 3 * 1024 * 1024 * 1024,
		Environment: []container.EnvironmentVariable{
			{Name: "ACCEPT_EULA", Value: "Y"},
			{Name: "MSSQL_SA_PASSWORD", Value: password},
			{Name: "SQLCMDPASSWORD", Value: password},
		},
	}

	instance, err := runtime.Start(ctx, spec)
	if err != nil {
		t.Fatalf("start SQL Server container: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if err := runtime.Remove(cleanupCtx, instance.ID); err != nil {
			t.Errorf("remove SQL Server container: %v", err)
		}
	})

	var queryOutput []byte
	var queryErr error
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		queryOutput, queryErr = runtime.Exec(ctx, instance.ID, []string{
			"/opt/mssql-tools18/bin/sqlcmd",
			"-C",
			"-S", "localhost",
			"-U", "sa",
			"-Q", "SELECT 1",
		})
		if queryErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for SQL Server readiness: %v", ctx.Err())
		case <-time.After(3 * time.Second):
		}
	}
	if queryErr != nil {
		t.Fatalf("query SQL Server after startup: %v", queryErr)
	}
	if !strings.Contains(string(queryOutput), "1") {
		t.Fatalf("SELECT 1 output = %q, want result containing 1", queryOutput)
	}
}

func randomTestSuffix(t *testing.T) string {
	t.Helper()
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		t.Fatalf("generate test-only random value: %v", err)
	}
	return fmt.Sprintf("%x", random)
}
