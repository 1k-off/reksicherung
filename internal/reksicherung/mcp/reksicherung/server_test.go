package reksicherung

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/1k-off/reksicherung/internal/reksicherung/plan"
	"github.com/1k-off/reksicherung/internal/reksicherung/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRestoreCheckToolRoundTrip(t *testing.T) {
	service := &fakeService{result: ServiceResult{
		RunID:      "run-123",
		AironRunID: "airon-456",
		Databases: []DatabaseResult{{
			DatabaseID: "billing-prod",
			Status:     workflow.OutcomePassed,
			Checks: []CheckResult{
				{ID: "restore", Status: workflow.CheckPassed, Required: true},
				{ID: "integrity", Status: workflow.CheckPassed, Required: true},
			},
		}},
		// The service may account for run-level outcomes (such as cleanup)
		// that are intentionally not exposed as database checks here.
		Verdict: workflow.VerdictFail,
	}}
	client, cleanup := connectTestClient(t, service)
	defer cleanup()

	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "restore_check" {
		t.Fatalf("registered tools = %#v, want only restore_check", tools.Tools)
	}

	args := map[string]any{
		"plan": map[string]any{
			"selection": map[string]any{"all": false, "database_ids": []string{"billing-prod"}},
			"ordering":  "sequential", "max_parallelism": 1, "error_handling": "continue",
		},
		"airon_run_id": "airon-456",
	}
	result := callTool(t, client, args)
	if result.IsError {
		t.Fatalf("restore_check failed: %#v", result.Content)
	}
	var got Result
	decodeResult(t, result, &got)
	want := Result{
		RunID: "run-123", AironRunID: "airon-456",
		Databases: []DatabaseResult{{
			DatabaseID: "billing-prod",
			Status:     workflow.OutcomePassed,
			Checks: []CheckResult{
				{ID: "restore", Status: workflow.CheckPassed, Required: true},
				{ID: "integrity", Status: workflow.CheckPassed, Required: true},
			},
		}},
		Verdict: workflow.VerdictFail,
	}
	if !equalJSON(got, want) {
		t.Fatalf("restore_check result = %#v, want %#v", got, want)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	if service.calls != 1 {
		t.Fatalf("service calls = %d, want 1", service.calls)
	}
	gotRequest := service.request
	if gotRequest.AironRunID != "airon-456" ||
		gotRequest.Plan.Selection.All ||
		len(gotRequest.Plan.Selection.DatabaseIDs) != 1 ||
		gotRequest.Plan.Selection.DatabaseIDs[0] != "billing-prod" ||
		gotRequest.Plan.Ordering != plan.Sequential ||
		gotRequest.Plan.MaxParallelism != 1 ||
		gotRequest.Plan.ErrorHandling != plan.Continue {
		t.Fatalf("service request = %#v", gotRequest)
	}
}

func TestRestoreCheckUsesAironRunIDMetadata(t *testing.T) {
	service := &fakeService{result: ServiceResult{
		RunID:      "run-123",
		AironRunID: "airon-789",
		Databases: []DatabaseResult{{
			DatabaseID: "db-1",
			Status:     workflow.OutcomePassed,
			Checks:     []CheckResult{{ID: "restore", Status: workflow.CheckPassed, Required: true}},
		}},
		Verdict: workflow.VerdictPass,
	}}
	client, cleanup := connectTestClient(t, service)
	defer cleanup()

	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "restore_check",
		Arguments: validArgs(),
		Meta:      mcp.Meta{AironRunIDMetaKey: "airon-789"},
	})
	if err != nil || result.IsError {
		t.Fatalf("CallTool() error = %v, result = %#v", err, result)
	}
	var got Result
	decodeResult(t, result, &got)
	if got.AironRunID != "airon-789" {
		t.Fatalf("result Airon run ID = %q, want metadata value", got.AironRunID)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.request.AironRunID != "airon-789" {
		t.Fatalf("service Airon run ID = %q, want metadata value", service.request.AironRunID)
	}
}

func TestRestoreCheckSchemaAndDecoderAgreeOnAllSelection(t *testing.T) {
	service := &fakeService{result: ServiceResult{
		RunID: "run-123",
		Databases: []DatabaseResult{{
			DatabaseID: "db-1",
			Status:     workflow.OutcomePassed,
			Checks:     []CheckResult{{ID: "restore", Status: workflow.CheckPassed, Required: true}},
		}},
		Verdict: workflow.VerdictPass,
	}}
	client, cleanup := connectTestClient(t, service)
	defer cleanup()

	args := map[string]any{
		"plan": map[string]any{
			"selection": map[string]any{"all": true, "database_ids": []string{}},
			"ordering":  "any", "max_parallelism": 1, "error_handling": "continue",
		},
	}
	result := callTool(t, client, args)
	if result.IsError {
		t.Fatalf("all-selection restore_check failed: %#v", result.Content)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if !service.request.Plan.Selection.All || len(service.request.Plan.Selection.DatabaseIDs) != 0 {
		t.Fatalf("service received selection %#v, want all=true and no explicit IDs", service.request.Plan.Selection)
	}
}

func TestRestoreCheckRejectsConflictingAironRunIDMetadata(t *testing.T) {
	service := &fakeService{result: ServiceResult{RunID: "run-123"}}
	client, cleanup := connectTestClient(t, service)
	defer cleanup()

	args := validArgs()
	args["airon_run_id"] = "airon-arg"
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "restore_check",
		Arguments: args,
		Meta:      mcp.Meta{AironRunIDMetaKey: "airon-meta"},
	})
	if err == nil && !result.IsError {
		t.Fatal("CallTool() accepted conflicting Airon run IDs")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.calls != 0 {
		t.Fatalf("service calls = %d, want 0 for conflicting correlation IDs", service.calls)
	}
}

func TestRestoreCheckRejectsMalformedUnknownAndOversizedInput(t *testing.T) {
	service := &fakeService{}
	client, cleanup := connectTestClient(t, service)
	defer cleanup()

	basePlan := map[string]any{
		"selection": map[string]any{"all": false, "database_ids": []string{"db-1"}},
		"ordering":  "sequential", "max_parallelism": 1, "error_handling": "continue",
	}
	cases := []struct {
		name string
		args map[string]any
	}{
		{
			name: "unknown top-level field",
			args: map[string]any{"plan": basePlan, "unexpected": "must not pass"},
		},
		{
			name: "unknown plan field",
			args: map[string]any{"plan": map[string]any{
				"selection": map[string]any{"all": false, "database_ids": []string{"db-1"}},
				"ordering":  "sequential", "max_parallelism": 1, "error_handling": "continue",
				"backup_id": "not-an-accepted-input",
			}},
		},
		{
			name: "unknown selection field",
			args: map[string]any{"plan": map[string]any{
				"selection": map[string]any{
					"all": false, "database_ids": []string{"db-1"}, "database_name": "unknown-field-value-sentinel",
				},
				"ordering": "sequential", "max_parallelism": 1, "error_handling": "continue",
			}},
		},
		{
			name: "missing all-selection discriminator",
			args: map[string]any{"plan": map[string]any{
				"selection": map[string]any{"database_ids": []string{"db-1"}},
				"ordering":  "sequential", "max_parallelism": 1, "error_handling": "continue",
			}},
		},
		{
			name: "all selection with explicit IDs",
			args: map[string]any{"plan": map[string]any{
				"selection": map[string]any{"all": true, "database_ids": []string{"db-1"}},
				"ordering":  "any", "max_parallelism": 1, "error_handling": "continue",
			}},
		},
		{
			name: "explicit selection without IDs",
			args: map[string]any{"plan": map[string]any{
				"selection": map[string]any{"all": false, "database_ids": []string{}},
				"ordering":  "any", "max_parallelism": 1, "error_handling": "continue",
			}},
		},
		{
			name: "invalid execution policy",
			args: map[string]any{"plan": map[string]any{
				"selection": map[string]any{"all": false, "database_ids": []string{"db-1"}},
				"ordering":  "sequential", "max_parallelism": 2, "error_handling": "continue",
			}},
		},
		{
			name: "excessive parallelism",
			args: map[string]any{"plan": map[string]any{
				"selection": map[string]any{"all": false, "database_ids": []string{"db-1"}},
				"ordering":  "any", "max_parallelism": plan.MaxParallelism + 1, "error_handling": "continue",
			}},
		},
		{
			name: "oversized payload",
			args: map[string]any{
				"plan":       basePlan,
				"unexpected": strings.Repeat("x", maxRequestBytes),
			},
		},
		{
			name: "too many selected databases",
			args: map[string]any{"plan": map[string]any{
				"selection": map[string]any{
					"all": false, "database_ids": databaseIDs(maxSelectedDBs + 1),
				},
				"ordering": "any", "max_parallelism": 8, "error_handling": "continue",
			}},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "restore_check", Arguments: test.args,
			})
			if err == nil && !result.IsError {
				t.Fatal("CallTool() succeeded with invalid input")
			}
			if err != nil && (strings.Contains(err.Error(), "must not pass") ||
				strings.Contains(err.Error(), "unknown-field-value-sentinel")) {
				t.Fatalf("invalid-input error leaked caller data: %v", err)
			}
		})
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.calls != 0 {
		t.Fatalf("invalid requests reached service %d times", service.calls)
	}
}

func TestRestoreCheckServiceErrorsAreRedacted(t *testing.T) {
	const secret = "private-access-key"
	service := &fakeService{err: errors.New("restore failed using password " + secret)}
	client, cleanup := connectTestClient(t, service)
	defer cleanup()

	result := callTool(t, client, validArgs())
	if !result.IsError {
		t.Fatal("restore_check result IsError = false, want true")
	}
	assertNoLeak(t, result, secret)
	assertNoLeak(t, result, "password")
}

func TestRestoreCheckDoesNotExposeUntrustedServiceData(t *testing.T) {
	const secret = "private-access-key"
	service := &fakeService{result: ServiceResult{
		RunID: "password=" + secret,
		Databases: []DatabaseResult{{
			DatabaseID: "db-1",
			Status:     workflow.OutcomePassed,
		}},
		Verdict: workflow.VerdictPass,
	}}
	client, cleanup := connectTestClient(t, service)
	defer cleanup()

	result := callTool(t, client, validArgs())
	if !result.IsError {
		t.Fatal("invalid service result was returned as successful")
	}
	assertNoLeak(t, result, secret)
}

func TestRestoreCheckBoundsServiceResult(t *testing.T) {
	service := &fakeService{result: ServiceResult{
		RunID:      "run-123",
		AironRunID: "airon-456",
		Databases: []DatabaseResult{{
			DatabaseID: "db-1",
			Status:     workflow.OutcomePassed,
			Checks:     make([]CheckResult, maxResultChecks+1),
		}},
		Verdict: workflow.VerdictPass,
	}}
	client, cleanup := connectTestClient(t, service)
	defer cleanup()

	result := callTool(t, client, validArgs())
	if !result.IsError {
		t.Fatal("oversized service result was returned as successful")
	}
}

func TestRestoreCheckRejectsUnpersistedAironCorrelationID(t *testing.T) {
	service := &fakeService{result: ServiceResult{
		RunID: "run-123",
		Databases: []DatabaseResult{{
			DatabaseID: "db-1",
			Status:     workflow.OutcomePassed,
			Checks:     []CheckResult{{ID: "restore", Status: workflow.CheckPassed, Required: true}},
		}},
		Verdict: workflow.VerdictPass,
	}}
	client, cleanup := connectTestClient(t, service)
	defer cleanup()

	args := validArgs()
	args["airon_run_id"] = "airon-requested"
	result := callTool(t, client, args)
	if !result.IsError {
		t.Fatal("restore_check succeeded without a persisted Airon correlation ID")
	}
	assertNoLeak(t, result, "airon-requested")
}

func connectTestClient(t *testing.T, service Service) (*mcp.ClientSession, func()) {
	t.Helper()
	server := NewServer(service)
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

func callTool(t *testing.T, client *mcp.ClientSession, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "restore_check", Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	return result
}

func validArgs() map[string]any {
	return map[string]any{"plan": map[string]any{
		"selection": map[string]any{"all": false, "database_ids": []string{"db-1"}},
		"ordering":  "sequential", "max_parallelism": 1, "error_handling": "continue",
	}}
}

func decodeResult(t *testing.T, result *mcp.CallToolResult, output any) {
	t.Helper()
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured result: %v", err)
	}
	if err := json.Unmarshal(encoded, output); err != nil {
		t.Fatalf("unmarshal structured result: %v", err)
	}
}

func assertNoLeak(t *testing.T, result *mcp.CallToolResult, secret string) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("tool result leaked %q: %s", secret, encoded)
	}
}

func databaseIDs(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = "db-" + string(rune('a'+i%26)) + "-" + string(rune('A'+i/26))
	}
	return ids
}

func equalJSON(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

type fakeService struct {
	mu      sync.Mutex
	request Request
	result  ServiceResult
	err     error
	calls   int
}

func (fake *fakeService) RestoreCheck(_ context.Context, request Request) (ServiceResult, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	fake.request = request
	return fake.result, fake.err
}
