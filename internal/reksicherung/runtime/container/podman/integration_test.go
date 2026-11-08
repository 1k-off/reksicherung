//go:build integration

package podman

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
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
		DiskBytes:   1 * 1024 * 1024 * 1024,
		Timeout:     3 * time.Minute,
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
		if err := runtime.Remove(cleanupCtx, instance); err != nil {
			t.Errorf("remove SQL Server container: %v", err)
		}
	})

	var queryOutput []byte
	var queryErr error
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		queryOutput, queryErr = runtime.Exec(ctx, instance, []string{
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

	const artifact = "reksicherung-transfer-test.bak"
	artifactContents := "integration artifact stream"
	if err := runtime.Transfer(ctx, instance, artifact, io.NopCloser(strings.NewReader(artifactContents)), int64(len(artifactContents))); err != nil {
		t.Fatalf("stream artifact into SQL Server container: %v", err)
	}
	gotArtifact, err := runtime.Exec(ctx, instance, []string{"cat", artifactDirectory + "/" + artifact})
	if err != nil {
		t.Fatalf("read streamed artifact from SQL Server container: %v", err)
	}
	if string(gotArtifact) != artifactContents {
		t.Fatalf("container artifact = %q, want %q", gotArtifact, artifactContents)
	}

	if _, err := runtime.Exec(ctx, instance, []string{"dd", "if=/dev/zero", "of=/dev/null", "bs=1", "count=1"}); err != nil {
		t.Fatalf("check disk quota probe utility: %v", err)
	}
	probePath := artifactDirectory + "/reksicherung-disk-quota-probe"
	if _, err := runtime.Exec(ctx, instance, []string{
		"dd", "if=/dev/zero", "of=" + probePath, "bs=1M", "count=1025",
	}); err == nil {
		_, cleanupErr := runtime.Exec(ctx, instance, []string{"rm", "-f", "--", probePath})
		if cleanupErr != nil {
			t.Errorf("remove over-quota probe file: %v", cleanupErr)
		}
		t.Fatal("container wrote 1025 MiB with a 1 GiB writable-layer limit")
	}
	if _, err := runtime.Exec(ctx, instance, []string{"rm", "-f", "--", probePath}); err != nil {
		t.Fatalf("remove partial over-quota probe file: %v", err)
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
