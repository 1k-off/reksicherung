package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"

	mysqlsvc "github.com/1k-off/reksicherung/internal/reksicherung/mcp/mysql"
	restoremysql "github.com/1k-off/reksicherung/internal/reksicherung/restore/mysql"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type inspectionService interface {
	ListTables(context.Context, string, string) (mysqlsvc.TableList, error)
	DescribeTable(context.Context, mysqlsvc.TableRef) (mysqlsvc.TableDescription, error)
	Validate(context.Context, mysqlsvc.ValidationQuery) (mysqlsvc.ValidationResult, error)
	CheckIntegrity(context.Context) (mysqlsvc.IntegrityResult, error)
	ScopeToDatabase(string) (inspectionService, error)
}

type listTablesArgs struct {
	Schema *string `json:"schema,omitempty" jsonschema:"Optional schema name; MySQL schemas are databases, so this must match the inspected database. Omit to use it."`
	RunID  *string `json:"run_id,omitempty" jsonschema:"Optional restore run_id; selects the run-scoped database. Required when the process is not pinned to one database."`
}

type describeTableArgs struct {
	Schema *string `json:"schema,omitempty" jsonschema:"Optional schema name; must match the inspected database."`
	Table  string  `json:"table" jsonschema:"Table whose column metadata should be inspected."`
	RunID  *string `json:"run_id,omitempty" jsonschema:"Optional restore run_id; selects the run-scoped database."`
}

type validateTableArgs struct {
	Schema *string `json:"schema,omitempty" jsonschema:"Optional schema name; must match the inspected database."`
	Table  string  `json:"table" jsonschema:"Table to inspect."`
	Limit  int     `json:"limit" jsonschema:"Maximum total rows to count (1 to 100)."`
	RunID  *string `json:"run_id,omitempty" jsonschema:"Optional restore run_id; selects the run-scoped database."`
}

type checkIntegrityArgs struct {
	RunID *string `json:"run_id,omitempty" jsonschema:"Optional restore run_id; selects the run-scoped database."`
}

// Custom unmarshaling makes the typed handlers reject unknown JSON properties.
// The MCP SDK validates the generated schema first; these methods ensure the
// JSON-to-Go conversion is equally strict.
func (args *listTablesArgs) UnmarshalJSON(data []byte) error {
	type input listTablesArgs
	return decodeStrictJSON(data, (*input)(args))
}

func (args *describeTableArgs) UnmarshalJSON(data []byte) error {
	type input describeTableArgs
	return decodeStrictJSON(data, (*input)(args))
}

func (args *validateTableArgs) UnmarshalJSON(data []byte) error {
	type input validateTableArgs
	return decodeStrictJSON(data, (*input)(args))
}

func (args *checkIntegrityArgs) UnmarshalJSON(data []byte) error {
	type input checkIntegrityArgs
	return decodeStrictJSON(data, (*input)(args))
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

// Table schema is deliberately omitted from outputs: in MySQL it is the
// run-scoped database name, which callers already select via run_id.
type tableOutput struct {
	Name string `json:"name" jsonschema:"Table name."`
}

type listTablesOutput struct {
	Tables    []tableOutput `json:"tables" jsonschema:"Base table names."`
	Truncated bool          `json:"truncated" jsonschema:"True when more tables exist than were returned."`
}

type columnOutput struct {
	Name      string `json:"name" jsonschema:"Column name."`
	DataType  string `json:"data_type" jsonschema:"MySQL data type from information_schema."`
	MaxLength int    `json:"max_length" jsonschema:"Character maximum length, or 0 when not applicable."`
	Precision int    `json:"precision" jsonschema:"Numeric precision, or 0 when not applicable."`
	Scale     int    `json:"scale" jsonschema:"Numeric scale, or 0 when not applicable."`
	Nullable  bool   `json:"nullable" jsonschema:"Whether the column allows NULL."`
}

type describeTableOutput struct {
	Table     string         `json:"table" jsonschema:"Inspected table name."`
	Columns   []columnOutput `json:"columns" jsonschema:"Column metadata only; no row values."`
	Truncated bool           `json:"truncated" jsonschema:"True when more columns exist than were returned."`
}

type validateTableOutput struct {
	ObservedRows int  `json:"observed_rows" jsonschema:"Rows observed, capped at limit."`
	Limit        int  `json:"limit" jsonschema:"Maximum number of rows counted."`
	Truncated    bool `json:"truncated" jsonschema:"True when the row count reached the cap."`
}

type checkIntegrityOutput struct {
	CommandCompleted bool `json:"command_completed" jsonschema:"True when CHECK TABLE commands completed without a command error for every table. This is completion only, not a clean-integrity verdict."`
	TablesChecked    int  `json:"tables_checked" jsonschema:"Number of tables CHECK TABLE was run against."`
	Truncated        bool `json:"truncated" jsonschema:"True when the table inventory exceeded the cap and some tables were not checked; command_completed is then false."`
}

func resolveInspectDatabase(defaultDatabase string, runID *string) (string, error) {
	if runID == nil || *runID == "" {
		return defaultDatabase, nil
	}
	name, err := restoremysql.DatabaseNameForRun(*runID)
	if err != nil {
		return "", errors.New("invalid tool arguments")
	}
	return name, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func newMCPServer(service inspectionService, database string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "reksicherung-mysql",
		Version: "0.1.0",
	}, &mcp.ServerOptions{
		Instructions: "Read-only MySQL restore inspection. Results contain schema metadata, bounded aggregates, or CHECK TABLE command-completion status, never row values and never a clean-integrity verdict. Pass run_id to target the restore-scoped database when the process is not pinned to one database.",
		// SDK diagnostics are disabled so protocol internals cannot accidentally
		// place prompts or tool arguments in logs.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "mysql_list_tables",
		Description: "List at most 200 base tables in the target database. Returns names and truncation metadata only. Pass run_id to select the restore-scoped database.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listTablesArgs) (*mcp.CallToolResult, listTablesOutput, error) {
		target, err := resolveInspectDatabase(database, args.RunID)
		if err != nil {
			return nil, listTablesOutput{}, err
		}
		scoped, err := service.ScopeToDatabase(target)
		if err != nil {
			return nil, listTablesOutput{}, safeToolError(err)
		}
		result, err := scoped.ListTables(ctx, target, optionalString(args.Schema))
		if err != nil {
			return nil, listTablesOutput{}, safeToolError(err)
		}
		out := listTablesOutput{Truncated: result.Truncated, Tables: make([]tableOutput, 0, len(result.Tables))}
		for _, table := range result.Tables {
			out.Tables = append(out.Tables, tableOutput{Name: table.Name})
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "mysql_describe_table",
		Description: "Return bounded MySQL column metadata (from information_schema) for a table in the target database. Does not return row values. Pass run_id to select the restore-scoped database.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args describeTableArgs) (*mcp.CallToolResult, describeTableOutput, error) {
		target, err := resolveInspectDatabase(database, args.RunID)
		if err != nil {
			return nil, describeTableOutput{}, err
		}
		scoped, err := service.ScopeToDatabase(target)
		if err != nil {
			return nil, describeTableOutput{}, safeToolError(err)
		}
		description, err := scoped.DescribeTable(ctx, mysqlsvc.TableRef{
			Database: target,
			Schema:   optionalString(args.Schema),
			Table:    args.Table,
		})
		if err != nil {
			return nil, describeTableOutput{}, safeToolError(err)
		}
		out := describeTableOutput{
			Table:     description.Ref.Table,
			Truncated: description.Truncated,
			Columns:   make([]columnOutput, 0, len(description.Columns)),
		}
		for _, column := range description.Columns {
			out.Columns = append(out.Columns, columnOutput{
				Name: column.Name, DataType: column.DataType, MaxLength: column.MaxLength,
				Precision: column.Precision, Scale: column.Scale, Nullable: column.Nullable,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "mysql_validate_table",
		Description: "Count total rows in one table, capped at 100. Caller-supplied predicates and row values are not accepted; only the aggregate count and truncation flag are returned. Pass run_id to select the restore-scoped database.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args validateTableArgs) (*mcp.CallToolResult, validateTableOutput, error) {
		target, err := resolveInspectDatabase(database, args.RunID)
		if err != nil {
			return nil, validateTableOutput{}, err
		}
		scoped, err := service.ScopeToDatabase(target)
		if err != nil {
			return nil, validateTableOutput{}, safeToolError(err)
		}
		result, err := scoped.Validate(ctx, mysqlsvc.ValidationQuery{
			Table: mysqlsvc.TableRef{
				Database: target,
				Schema:   optionalString(args.Schema),
				Table:    args.Table,
			},
			Limit: args.Limit,
		})
		if err != nil {
			return nil, validateTableOutput{}, safeToolError(err)
		}
		return nil, validateTableOutput{
			ObservedRows: result.ObservedRows,
			Limit:        result.Limit,
			Truncated:    result.Truncated,
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "mysql_check_integrity",
		Description: "Run fixed default-option CHECK TABLE on each base table (up to the cap) of the target database with a bounded timeout. Returns command-completion status only; CHECK TABLE diagnostic rows are discarded, so completion does not mean the database is free of corruption. Pass run_id to select the restore-scoped database.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args checkIntegrityArgs) (*mcp.CallToolResult, checkIntegrityOutput, error) {
		target, err := resolveInspectDatabase(database, args.RunID)
		if err != nil {
			return nil, checkIntegrityOutput{}, err
		}
		scoped, err := service.ScopeToDatabase(target)
		if err != nil {
			return nil, checkIntegrityOutput{}, safeToolError(err)
		}
		result, err := scoped.CheckIntegrity(ctx)
		if err != nil {
			return nil, checkIntegrityOutput{}, safeToolError(err)
		}
		return nil, checkIntegrityOutput{
			CommandCompleted: result.CommandCompleted,
			TablesChecked:    result.TablesChecked,
			Truncated:        result.Truncated,
		}, nil
	})

	return server
}

func readOnlyToolAnnotations() *mcp.ToolAnnotations {
	destructive := false
	openWorld := false
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: &destructive,
		IdempotentHint:  true,
		OpenWorldHint:   &openWorld,
	}
}

func safeToolError(err error) error {
	switch {
	case errors.Is(err, mysqlsvc.ErrInvalidInput):
		return errors.New("invalid tool arguments")
	case errors.Is(err, mysqlsvc.ErrNotFound):
		return errors.New("requested table was not found")
	case errors.Is(err, mysqlsvc.ErrTimeout):
		return errors.New("MySQL inspection timed out")
	case errors.Is(err, mysqlsvc.ErrCanceled):
		return errors.New("MySQL inspection canceled")
	case errors.Is(err, mysqlsvc.ErrIntegrityCheckTimeout):
		return errors.New("MySQL integrity check timed out")
	case errors.Is(err, mysqlsvc.ErrIntegrityCheckCanceled):
		return errors.New("MySQL integrity check canceled")
	case errors.Is(err, mysqlsvc.ErrIntegrityCheckFailed):
		return errors.New("MySQL integrity check failed")
	default:
		return errors.New("MySQL inspection failed")
	}
}
