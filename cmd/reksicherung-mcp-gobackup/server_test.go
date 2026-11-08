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

	gobackup "github.com/1k-off/reksicherung/internal/reksicherung/mcp/gobackup"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPToolsExposeBoundedMetadataOnlyOperations(t *testing.T) {
	timestamp := time.Date(2025, 1, 2, 3, 4, 5, 6, time.FixedZone("test", 3600))
	fake := &fakeArchiveService{
		backups: []gobackup.Backup{
			{ID: "newest/archive.tar", Size: 120, LastModified: timestamp},
			{ID: "older/archive.tar", Size: 90, LastModified: timestamp.Add(-time.Hour)},
		},
	}
	client, cleanup := connectTestClient(t, fake)
	defer cleanup()

	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	gotNames := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		gotNames = append(gotNames, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint ||
			tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("tool %q lacks read-only annotations: %#v", tool.Name, tool.Annotations)
		}
	}
	if want := []string{"gobackup_latest", "gobackup_list", "gobackup_select"}; !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("registered tools = %v, want %v", gotNames, want)
	}

	listed := callArchiveTool(t, client, "gobackup_list", map[string]any{"limit": 1})
	var list listOutput
	decodeArchiveResult(t, listed, &list)
	if len(list.Backups) != 1 || list.Backups[0].ID != "newest/archive.tar" || !list.Truncated {
		t.Fatalf("list output = %#v", list)
	}
	if list.Backups[0].LastModified != "2025-01-02T02:04:05.000000006Z" {
		t.Fatalf("last_modified = %q", list.Backups[0].LastModified)
	}
	latest := callArchiveTool(t, client, "gobackup_latest", map[string]any{})
	var latestBackup backupOutput
	decodeArchiveResult(t, latest, &latestBackup)
	if latestBackup.ID != "newest/archive.tar" {
		t.Fatalf("latest output = %#v", latestBackup)
	}
	selected := callArchiveTool(t, client, "gobackup_select", map[string]any{"id": "older/archive.tar"})
	var selectedBackup backupOutput
	decodeArchiveResult(t, selected, &selectedBackup)
	if selectedBackup.ID != "older/archive.tar" {
		t.Fatalf("select output = %#v", selectedBackup)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.listCalls != 1 || fake.latestCalls != 1 ||
		!reflect.DeepEqual(fake.selectIDs, []string{"older/archive.tar"}) {
		t.Fatalf("service calls = list %d, latest %d, select %v", fake.listCalls, fake.latestCalls, fake.selectIDs)
	}
}

func TestMCPRejectsInvalidInputsBeforeServiceCall(t *testing.T) {
	fake := &fakeArchiveService{}
	client, cleanup := connectTestClient(t, fake)
	defer cleanup()

	for _, test := range []struct {
		name string
		tool string
		args map[string]any
	}{
		{name: "unknown list field", tool: "gobackup_list", args: map[string]any{"limit": 1, "prefix": "other/"}},
		{name: "excessive limit", tool: "gobackup_list", args: map[string]any{"limit": maxListResults + 1}},
		{name: "parent path ID", tool: "gobackup_select", args: map[string]any{"id": "../outside"}},
		{name: "absolute ID", tool: "gobackup_select", args: map[string]any{"id": "/outside"}},
		{name: "unknown select field", tool: "gobackup_select", args: map[string]any{"id": "ok", "bucket": "elsewhere"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: test.tool, Arguments: test.args})
			if err == nil && !result.IsError {
				t.Fatal("CallTool() succeeded with invalid input")
			}
		})
	}
	if fake.listCalls != 0 || fake.latestCalls != 0 || len(fake.selectIDs) != 0 {
		t.Fatalf("invalid calls reached service: %#v", fake)
	}
}

func TestMCPToolErrorsDoNotExposeUnderlyingDetails(t *testing.T) {
	fake := &fakeArchiveService{listError: errors.New("secret=private-access-key")}
	client, cleanup := connectTestClient(t, fake)
	defer cleanup()

	result := callArchiveTool(t, client, "gobackup_list", map[string]any{})
	if !result.IsError {
		t.Fatal("list result IsError = false, want true")
	}
	if strings.Contains(result.Content[0].(*mcp.TextContent).Text, "private-access-key") {
		t.Fatalf("tool error leaked underlying error: %#v", result.Content)
	}
}

func TestBoundedFrameReaderEnforcesPerMessageLimit(t *testing.T) {
	t.Run("multiple frames", func(t *testing.T) {
		reader := newBoundedFrameReader(io.NopCloser(strings.NewReader("1234567\n7654321\n")), 8)
		got, err := io.ReadAll(reader)
		if err != nil || string(got) != "1234567\n7654321\n" {
			t.Fatalf("ReadAll() = %q, %v", got, err)
		}
	})
	t.Run("oversized frame", func(t *testing.T) {
		reader := newBoundedFrameReader(io.NopCloser(strings.NewReader(strings.Repeat("x", 5001)+"\n")), 5000)
		if _, err := io.ReadAll(reader); !errors.Is(err, errMCPFrameTooLarge) {
			t.Fatalf("ReadAll() error = %v, want frame-size error", err)
		}
	})
}

func connectTestClient(t *testing.T, service archiveService) (*mcp.ClientSession, func()) {
	t.Helper()
	server := newMCPServer(service)
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

func callArchiveTool(t *testing.T, client *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", name, err)
	}
	return result
}

func decodeArchiveResult(t *testing.T, result *mcp.CallToolResult, output any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool returned error: %#v", result.Content)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured result: %v", err)
	}
	if err := json.Unmarshal(encoded, output); err != nil {
		t.Fatalf("unmarshal structured result %s: %v", encoded, err)
	}
}

type fakeArchiveService struct {
	mu          sync.Mutex
	backups     []gobackup.Backup
	listError   error
	listCalls   int
	latestCalls int
	selectIDs   []string
}

func (fake *fakeArchiveService) List(context.Context) ([]gobackup.Backup, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.listCalls++
	return fake.backups, fake.listError
}

func (fake *fakeArchiveService) Latest(context.Context) (gobackup.Backup, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.latestCalls++
	if fake.listError != nil {
		return gobackup.Backup{}, fake.listError
	}
	if len(fake.backups) == 0 {
		return gobackup.Backup{}, errors.New("not found")
	}
	return fake.backups[0], nil
}

func (fake *fakeArchiveService) Select(_ context.Context, id string) (gobackup.Backup, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.selectIDs = append(fake.selectIDs, id)
	for _, backup := range fake.backups {
		if backup.ID == id {
			return backup, nil
		}
	}
	return gobackup.Backup{}, errors.New("not found")
}
