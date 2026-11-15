package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	mysqlrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/mysql"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRestoreMCPServerScopesOperationsToRunID(t *testing.T) {
	stage, err := staging.New(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := stage.Save(context.Background(), io.NopCloser(strings.NewReader("CREATE TABLE t(i INT);")), staging.KindSQL)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeRestoreService{}
	client, closeSessions := connectRestoreTestClient(t, service, stage)
	defer closeSessions()

	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if len(names) != 3 || !names["mysql_restore_backup"] || !names["mysql_check_restore_integrity"] ||
		!names["mysql_drop_restore_database"] {
		t.Fatalf("registered tools = %v", names)
	}

	const runID = "run-123"
	database, err := mysqlrestore.DatabaseNameForRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	restored := callRestoreTool(t, client, "mysql_restore_backup", map[string]any{
		"run_id": runID, "artifact_id": staged.ID, "source_database": "production",
	})
	var restoreResult restoreBackupOutput
	decodeRestoreResult(t, restored, &restoreResult)
	if !restoreResult.Restored || restoreResult.Database != database ||
		service.runID != runID || service.sourceDatabase != "production" || service.dump != "CREATE TABLE t(i INT);" {
		t.Fatalf("restore result/service = %#v / %#v", restoreResult, service)
	}

	integrity := callRestoreTool(t, client, "mysql_check_restore_integrity", map[string]any{"run_id": runID})
	var integrityResult integrityOutput
	decodeRestoreResult(t, integrity, &integrityResult)
	if !integrityResult.CommandComplete || integrityResult.Database != database {
		t.Fatalf("integrity result = %#v", integrityResult)
	}

	dropped := callRestoreTool(t, client, "mysql_drop_restore_database", map[string]any{"run_id": runID})
	var dropResult dropOutput
	decodeRestoreResult(t, dropped, &dropResult)
	if !dropResult.Removed || dropResult.Database != database || service.dropRunID != runID {
		t.Fatalf("drop result/service = %#v / %#v", dropResult, service)
	}
}

func TestRestoreMCPOpensOnlyKindSQLArtifacts(t *testing.T) {
	stage, err := staging.New(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	bak, err := stage.Save(context.Background(), io.NopCloser(strings.NewReader("native")), staging.KindBAK)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeRestoreService{}
	client, closeSessions := connectRestoreTestClient(t, service, stage)
	defer closeSessions()
	result := callRestoreTool(t, client, "mysql_restore_backup", map[string]any{
		"run_id": "run-1", "artifact_id": bak.ID, "source_database": "db",
	})
	if !result.IsError || service.restoreCalls != 0 {
		t.Fatalf("non-sql artifact accepted: %#v / %#v", result, service)
	}
}

func TestRestoreMCPRejectsInvalidArtifactAndUnknownFields(t *testing.T) {
	stage, err := staging.New(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeRestoreService{}
	client, closeSessions := connectRestoreTestClient(t, service, stage)
	defer closeSessions()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "mysql_restore_backup",
		Arguments: map[string]any{"run_id": "run-1", "artifact_id": "../unsafe", "source_database": "db"},
	})
	if err != nil || !result.IsError {
		t.Fatalf("unsafe artifact ID result = %#v, error = %v", result, err)
	}
	result, err = client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "mysql_drop_restore_database",
		Arguments: map[string]any{"run_id": "run-1", "database": "mysql"},
	})
	if err == nil && !result.IsError {
		t.Fatal("caller-selected database was accepted")
	}
	if service.restoreCalls != 0 || service.dropRunID != "" {
		t.Fatalf("invalid requests invoked service: %#v", service)
	}
}

func TestRestoreMCPErrorsAreSanitized(t *testing.T) {
	stage, err := staging.New(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := stage.Save(context.Background(), io.NopCloser(strings.NewReader("secret dump")), staging.KindSQL)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeRestoreService{restoreErr: errors.New("password leaked")}
	client, closeSessions := connectRestoreTestClient(t, service, stage)
	defer closeSessions()
	result := callRestoreTool(t, client, "mysql_restore_backup", map[string]any{
		"run_id": "run-1", "artifact_id": staged.ID, "source_database": "db",
	})
	if !result.IsError || strings.Contains(result.Content[0].(*mcp.TextContent).Text, "password") {
		t.Fatalf("error was not sanitized: %#v", result.Content)
	}
}

func TestRestoreMCPInvalidDumpErrorIsActionable(t *testing.T) {
	stage, err := staging.New(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := stage.Save(context.Background(), io.NopCloser(strings.NewReader("x")), staging.KindSQL)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeRestoreService{restoreErr: mysqlrestore.ErrInvalidDump}
	client, closeSessions := connectRestoreTestClient(t, service, stage)
	defer closeSessions()
	result := callRestoreTool(t, client, "mysql_restore_backup", map[string]any{
		"run_id": "run-1", "artifact_id": staged.ID, "source_database": "db",
	})
	if !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "invalid or references") {
		t.Fatalf("result = %#v", result.Content)
	}
}

func connectRestoreTestClient(t *testing.T, service restoreService, store artifactStore) (*mcp.ClientSession, func()) {
	t.Helper()
	server := newRestoreMCPServer(service, store)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		t.Fatalf("client.Connect() error = %v", err)
	}
	return clientSession, func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	}
}

func callRestoreTool(t *testing.T, client *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", name, err)
	}
	return result
}

func decodeRestoreResult(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool returned error: %#v", result.Content)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		t.Fatal(err)
	}
}

type fakeRestoreService struct {
	runID          string
	sourceDatabase string
	dump           string
	restoreErr     error
	restoreCalls   int
	dropRunID      string
}

func (service *fakeRestoreService) RestoreDump(_ context.Context, runID string, dump io.ReadCloser, sourceDatabase string) error {
	defer dump.Close()
	service.restoreCalls++
	service.runID, service.sourceDatabase = runID, sourceDatabase
	data, err := io.ReadAll(dump)
	if err != nil {
		return err
	}
	service.dump = string(data)
	return service.restoreErr
}

func (service *fakeRestoreService) CheckIntegrity(_ context.Context, runID string) (mysqlrestore.IntegrityResult, error) {
	database, err := mysqlrestore.DatabaseNameForRun(runID)
	return mysqlrestore.IntegrityResult{Database: database, CommandCompleted: true}, err
}

func (service *fakeRestoreService) DropDatabase(_ context.Context, runID string) error {
	service.dropRunID = runID
	return nil
}
