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

	sqlserver "github.com/1k-off/reksicherung/internal/reksicherung/mcp/sqlserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLoadConfigRequiresOneMatchingTargetDatabase(t *testing.T) {
	tests := []struct {
		name     string
		dsn      string
		database string
		wantErr  bool
	}{
		{
			name:     "configured URL target",
			dsn:      "sqlserver://inspector:secret@127.0.0.1:1433?database=RestoredDb",
			database: "RestoredDb",
		},
		{
			name:     "target mismatch",
			dsn:      "sqlserver://inspector:secret@127.0.0.1:1433?database=OtherDb",
			database: "RestoredDb",
			wantErr:  true,
		},
		{
			name:     "duplicate target parameters",
			dsn:      "sqlserver://inspector:secret@127.0.0.1:1433?database=RestoredDb&database=OtherDb",
			database: "RestoredDb",
			wantErr:  true,
		},
		{
			name:     "not a SQL Server URL",
			dsn:      "not a connection string",
			database: "RestoredDb",
			wantErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{dsnEnv: test.dsn, databaseEnv: test.database}
			got, err := loadConfig(func(key string) string { return values[key] })
			if (err != nil) != test.wantErr {
				t.Fatalf("loadConfig() error = %v, wantErr %v", err, test.wantErr)
			}
			if err == nil && (got.database != test.database || got.dsn != test.dsn) {
				t.Fatalf("loadConfig() = %#v, want configured DSN and target", got)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatalf("loadConfig() leaked connection credentials: %v", err)
			}
		})
	}
}

func TestBoundedFrameReaderLimitsEachProtocolMessage(t *testing.T) {
	t.Run("multiple frames below individual cap", func(t *testing.T) {
		reader := newBoundedFrameReader(io.NopCloser(strings.NewReader("1234567\n7654321\n")), 8)
		got, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		if string(got) != "1234567\n7654321\n" {
			t.Fatalf("ReadAll() = %q", got)
		}
	})

	t.Run("oversized frame", func(t *testing.T) {
		reader := newBoundedFrameReader(io.NopCloser(strings.NewReader("12345678\n")), 8)
		if _, err := io.ReadAll(reader); !errors.Is(err, errMCPFrameTooLarge) {
			t.Fatalf("ReadAll() error = %v, want frame-size error", err)
		}
	})

	t.Run("oversized frame split across reader buffers", func(t *testing.T) {
		input := strings.Repeat("x", 5001) + "\n"
		reader := newBoundedFrameReader(io.NopCloser(strings.NewReader(input)), 5000)
		if _, err := io.ReadAll(reader); !errors.Is(err, errMCPFrameTooLarge) {
			t.Fatalf("ReadAll() error = %v, want frame-size error", err)
		}
	})
}

func TestMCPToolRegistrationAndBoundedSemantics(t *testing.T) {
	fake := &fakeInspectionService{
		tables: sqlserver.TableList{
			Tables:    []sqlserver.Table{{Schema: "dbo", Name: "orders"}},
			Truncated: true,
		},
		description: sqlserver.TableDescription{
			Ref:       sqlserver.TableRef{Database: "RestoredDb", Schema: "dbo", Table: "orders"},
			Columns:   []sqlserver.Column{{Name: "status", DataType: "nvarchar", MaxLength: 64, Nullable: true}},
			Truncated: false,
		},
		validation: sqlserver.ValidationResult{ObservedRows: 2, Limit: 2, Truncated: true},
	}
	client, closeSessions := connectMCPTestClient(t, fake, "RestoredDb")
	defer closeSessions()

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
	wantNames := []string{
		"sqlserver_describe_table",
		"sqlserver_list_tables",
		"sqlserver_validate_table",
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("registered tools = %v, want %v", gotNames, wantNames)
	}

	listResult := callTool(t, client, "sqlserver_list_tables", map[string]any{"schema": "dbo"})
	var listed listTablesOutput
	decodeStructuredResult(t, listResult, &listed)
	if len(listed.Tables) != 1 || listed.Tables[0] != (tableOutput{Schema: "dbo", Name: "orders"}) || !listed.Truncated {
		t.Fatalf("list output = %#v", listed)
	}
	if fake.listDatabase != "RestoredDb" || fake.listSchema != "dbo" {
		t.Fatalf("list target = %q.%q, want configured database and requested schema", fake.listDatabase, fake.listSchema)
	}

	descriptionResult := callTool(t, client, "sqlserver_describe_table", map[string]any{
		"schema": "dbo",
		"table":  "orders",
	})
	var described describeTableOutput
	decodeStructuredResult(t, descriptionResult, &described)
	if described.Schema != "dbo" || described.Table != "orders" || len(described.Columns) != 1 ||
		described.Columns[0].Name != "status" {
		t.Fatalf("describe output = %#v", described)
	}

	validationResult := callTool(t, client, "sqlserver_validate_table", map[string]any{
		"schema": "dbo",
		"table":  "orders",
		"limit":  2,
	})
	var validated validateTableOutput
	decodeStructuredResult(t, validationResult, &validated)
	if validated != (validateTableOutput{ObservedRows: 2, Limit: 2, Truncated: true}) {
		t.Fatalf("validation output = %#v", validated)
	}
	if fake.validationQuery.Table.Database != "RestoredDb" ||
		len(fake.validationQuery.Predicates) != 0 || fake.validationQuery.Limit != 2 {
		t.Fatalf("validation request = %#v, want target-scoped bounded request", fake.validationQuery)
	}
	for _, result := range []*mcp.CallToolResult{listResult, descriptionResult, validationResult} {
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"secret-row-value", "password", "dsn", "RestoredDb"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Errorf("tool output leaked %q: %s", forbidden, encoded)
			}
		}
	}
}

func TestMCPRejectsUnknownAndInvalidInputsBeforeServiceCall(t *testing.T) {
	fake := &fakeInspectionService{}
	client, closeSessions := connectMCPTestClient(t, fake, "RestoredDb")
	defer closeSessions()

	for _, test := range []struct {
		name string
		tool string
		args map[string]any
	}{
		{
			name: "unknown top-level property",
			tool: "sqlserver_list_tables",
			args: map[string]any{"schema": "dbo", "database": "master"},
		},
		{
			name: "caller-selected predicates rejected",
			tool: "sqlserver_validate_table",
			args: map[string]any{
				"schema": "dbo", "table": "orders", "limit": 1,
				"predicates": []any{map[string]any{
					"column": "email", "operator": "like", "value": "customer@example.invalid%",
				}},
			},
		},
		{
			name: "invalid limit type",
			tool: "sqlserver_validate_table",
			args: map[string]any{"schema": "dbo", "table": "orders", "limit": "100"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: test.tool, Arguments: test.args,
			})
			if err == nil {
				t.Fatal("CallTool() succeeded with invalid input")
			}
			if strings.Contains(err.Error(), "customer@example.invalid") {
				t.Fatalf("CallTool() validation error echoed the supplied row value: %v", err)
			}
		})
	}
	if fake.calls != 0 {
		t.Fatalf("invalid calls reached service %d times", fake.calls)
	}
}

func TestMCPToolErrorsAreSafeAndRowValuesAreNeverReturned(t *testing.T) {
	fake := &fakeInspectionService{validationError: errors.New("password=do-not-return")}
	client, closeSessions := connectMCPTestClient(t, fake, "RestoredDb")
	defer closeSessions()

	result := callTool(t, client, "sqlserver_validate_table", map[string]any{
		"schema": "dbo", "table": "orders", "limit": 1,
	})
	if !result.IsError {
		t.Fatalf("CallTool() IsError = false, want true")
	}
	if strings.Contains(string(result.Content[0].(*mcp.TextContent).Text), "do-not-return") {
		t.Fatalf("tool error leaked underlying error: %#v", result.Content)
	}
}

func connectMCPTestClient(t *testing.T, service inspectionService, database string) (*mcp.ClientSession, func()) {
	t.Helper()
	server := newMCPServer(service, database)
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
		t.Fatalf("tool returned error: %#v", result.Content)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(encoded, output); err != nil {
		t.Fatalf("unmarshal structured content %s: %v", encoded, err)
	}
}

type fakeInspectionService struct {
	mu              sync.Mutex
	calls           int
	listDatabase    string
	listSchema      string
	tables          sqlserver.TableList
	description     sqlserver.TableDescription
	validation      sqlserver.ValidationResult
	validationQuery sqlserver.ValidationQuery
	validationError error
}

func (fake *fakeInspectionService) ListTables(_ context.Context, database, schema string) (sqlserver.TableList, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	fake.listDatabase, fake.listSchema = database, schema
	return fake.tables, nil
}

func (fake *fakeInspectionService) DescribeTable(_ context.Context, ref sqlserver.TableRef) (sqlserver.TableDescription, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	fake.description.Ref = ref
	return fake.description, nil
}

func (fake *fakeInspectionService) Validate(_ context.Context, query sqlserver.ValidationQuery) (sqlserver.ValidationResult, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	fake.validationQuery = query
	if fake.validationError != nil {
		return sqlserver.ValidationResult{}, fake.validationError
	}
	return fake.validation, nil
}
