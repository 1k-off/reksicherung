package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	sqlserver "github.com/1k-off/reksicherung/internal/reksicherung/restore/sqlserver"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRestoreMCPServerScopesOperationsToRunID(t *testing.T) {
	stage, err := staging.New(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := stage.Save(context.Background(), io.NopCloser(strings.NewReader("native backup")), staging.KindBAK)
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
	if len(tools.Tools) != 3 {
		t.Fatalf("registered tools = %d, want 3", len(tools.Tools))
	}

	const runID = "run-123"
	restored := callRestoreTool(t, client, "sqlserver_restore_backup", map[string]any{
		"run_id": runID, "artifact_id": staged.ID, "source_database": "Production",
	})
	var restoreResult restoreBackupOutput
	decodeRestoreResult(t, restored, &restoreResult)
	database, err := sqlserver.DatabaseNameForRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if !restoreResult.Restored || restoreResult.Database != database ||
		service.runID != runID || service.sourceDatabase != "Production" || service.backupContents != "native backup" {
		t.Fatalf("restore result/service = %#v / %#v", restoreResult, service)
	}

	integrity := callRestoreTool(t, client, "sqlserver_check_restore_integrity", map[string]any{"run_id": runID})
	var integrityResult integrityOutput
	decodeRestoreResult(t, integrity, &integrityResult)
	if !integrityResult.CommandComplete || integrityResult.Database != database {
		t.Fatalf("integrity result = %#v", integrityResult)
	}

	dropped := callRestoreTool(t, client, "sqlserver_drop_restore_database", map[string]any{"run_id": runID})
	var dropResult dropOutput
	decodeRestoreResult(t, dropped, &dropResult)
	if !dropResult.Removed || dropResult.Database != database || service.dropRunID != runID {
		t.Fatalf("drop result/service = %#v / %#v", dropResult, service)
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
		Name:      "sqlserver_restore_backup",
		Arguments: map[string]any{"run_id": "run-1", "artifact_id": "../unsafe", "source_database": "db"},
	})
	if err != nil || !result.IsError {
		t.Fatalf("unsafe artifact ID result = %#v, error = %v", result, err)
	}
	result, err = client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "sqlserver_drop_restore_database",
		Arguments: map[string]any{"run_id": "run-1", "database": "master"},
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
	staged, err := stage.Save(context.Background(), io.NopCloser(strings.NewReader("secret backup")), staging.KindBAK)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeRestoreService{restoreErr: errors.New("password leaked")}
	client, closeSessions := connectRestoreTestClient(t, service, stage)
	defer closeSessions()
	result := callRestoreTool(t, client, "sqlserver_restore_backup", map[string]any{
		"run_id": "run-1", "artifact_id": staged.ID, "source_database": "db",
	})
	if !result.IsError || strings.Contains(string(result.Content[0].(*mcp.TextContent).Text), "password") {
		t.Fatalf("error was not sanitized: %#v", result.Content)
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
	backupContents string
	restoreErr     error
	restoreCalls   int
	dropRunID      string
}

func (service *fakeRestoreService) RestoreBackup(_ context.Context, runID string, archive io.ReadCloser, sourceDatabase string) error {
	defer archive.Close()
	service.restoreCalls++
	service.runID, service.sourceDatabase = runID, sourceDatabase
	data, err := io.ReadAll(archive)
	if err != nil {
		return err
	}
	service.backupContents = string(data)
	return service.restoreErr
}

func (service *fakeRestoreService) ImportBACPAC(_ context.Context, runID string, archive io.ReadCloser) error {
	defer archive.Close()
	service.restoreCalls++
	service.runID = runID
	data, err := io.ReadAll(archive)
	if err != nil {
		return err
	}
	service.backupContents = string(data)
	return service.restoreErr
}

func (service *fakeRestoreService) CheckIntegrity(_ context.Context, runID string) (sqlserver.IntegrityResult, error) {
	database, err := sqlserver.DatabaseNameForRun(runID)
	return sqlserver.IntegrityResult{Database: database, CommandCompleted: true}, err
}

func (service *fakeRestoreService) DropDatabase(_ context.Context, runID string) error {
	service.dropRunID = runID
	return nil
}
