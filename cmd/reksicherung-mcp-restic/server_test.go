package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	resticservice "github.com/1k-off/reksicherung/internal/reksicherung/mcp/restic"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testSnapshotID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestMCPRegistersOnlyBoundedReadOnlySnapshotTools(t *testing.T) {
	fake := &fakeSnapshotService{
		list: resticservice.SnapshotList{
			Snapshots: []resticservice.Snapshot{{
				ID: testSnapshotID, Time: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
				Hostname: "archive-host", Tags: []string{"daily"},
			}},
			Truncated: true,
		},
		selected: resticservice.Snapshot{
			ID: testSnapshotID, Time: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
			Hostname: "archive-host", Tags: []string{"daily"},
		},
	}
	client, closeSessions := connectMCPTestClient(t, fake)
	defer closeSessions()

	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	gotNames := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		gotNames = append(gotNames, tool.Name)
	}
	if want := []string{"restic_list_snapshots", "restic_select_snapshot"}; !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("registered tools = %v, want %v", gotNames, want)
	}

	listed := callTool(t, client, "restic_list_snapshots", map[string]any{"source_id": "reporting-production", "limit": 1})
	var listOutput listSnapshotsOutput
	decodeStructuredResult(t, listed, &listOutput)
	if len(listOutput.Snapshots) != 1 || !listOutput.Truncated ||
		listOutput.Snapshots[0].ID != testSnapshotID {
		t.Fatalf("list output = %#v", listOutput)
	}

	selected := callTool(t, client, "restic_select_snapshot", map[string]any{
		"source_id": "reporting-production", "id": testSnapshotID,
	})
	var selectedOutput snapshotOutput
	decodeStructuredResult(t, selected, &selectedOutput)
	if selectedOutput.ID != testSnapshotID {
		t.Fatalf("selection output = %#v", selectedOutput)
	}
	if fake.listLimit != 1 || fake.selectedID != testSnapshotID || fake.sourceID != "reporting-production" {
		t.Fatalf("service calls used limit=%d selected=%q source=%q", fake.listLimit, fake.selectedID, fake.sourceID)
	}
}

func TestMCPRejectsUnknownArgumentsAndHidesServiceErrors(t *testing.T) {
	fake := &fakeSnapshotService{listError: errors.New("password=must-not-leak")}
	client, closeSessions := connectMCPTestClient(t, fake)
	defer closeSessions()

	for _, test := range []struct {
		tool string
		args map[string]any
	}{
		{tool: "restic_list_snapshots", args: map[string]any{"limit": 2}},
		{tool: "restic_list_snapshots", args: map[string]any{"source_id": "reporting-production", "repository": "other"}},
		{tool: "restic_select_snapshot", args: map[string]any{"source_id": "reporting-production", "id": testSnapshotID, "restore": true}},
	} {
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
			Name: test.tool, Arguments: test.args,
		})
		if err == nil && (result == nil || !result.IsError) {
			t.Fatalf("CallTool(%s) succeeded with invalid arguments", test.tool)
		}
	}
	if fake.calls != 0 {
		t.Fatalf("invalid tool input reached service %d times", fake.calls)
	}

	result := callTool(t, client, "restic_list_snapshots", map[string]any{"source_id": "reporting-production", "limit": 1})
	if !result.IsError {
		t.Fatal("service failure did not produce an MCP tool error")
	}
	if strings.Contains(resultText(result), "must-not-leak") {
		t.Fatalf("tool error leaked service details: %s", resultText(result))
	}
}

func TestBoundedFrameReaderLimitsIndividualProtocolFrames(t *testing.T) {
	reader := newBoundedFrameReader(io.NopCloser(strings.NewReader("1234567\n7654321\n")), 8)
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "1234567\n7654321\n" {
		t.Fatalf("ReadAll() = %q, %v", got, err)
	}
	reader = newBoundedFrameReader(io.NopCloser(strings.NewReader(strings.Repeat("x", 5001)+"\n")), 5000)
	if _, err := io.ReadAll(reader); !errors.Is(err, errMCPFrameTooLarge) {
		t.Fatalf("ReadAll(oversized) error = %v, want frame-size error", err)
	}
}

func connectMCPTestClient(t *testing.T, instance multiSourceRestic) (*mcp.ClientSession, func()) {
	t.Helper()
	server := newMCPServer(instance)
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

func callTool(t *testing.T, client *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", name, err)
	}
	return result
}

func decodeStructuredResult(t *testing.T, result *mcp.CallToolResult, output any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool returned error: %s", resultText(result))
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(encoded, output); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
}

func resultText(result *mcp.CallToolResult) string {
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

type fakeSnapshotService struct {
	mu         sync.Mutex
	calls      int
	listLimit  int
	selectedID string
	sourceID   string
	list       resticservice.SnapshotList
	selected   resticservice.Snapshot
	listError  error
}

func (fake *fakeSnapshotService) ListSnapshots(_ context.Context, sourceID string, limit int) (resticservice.SnapshotList, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	fake.listLimit = limit
	fake.sourceID = sourceID
	return fake.list, fake.listError
}

func (fake *fakeSnapshotService) SelectSnapshot(_ context.Context, sourceID, id string) (resticservice.Snapshot, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	fake.selectedID = id
	fake.sourceID = sourceID
	return fake.selected, nil
}
