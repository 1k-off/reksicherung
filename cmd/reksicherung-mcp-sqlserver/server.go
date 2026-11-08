package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"

	sqlserver "github.com/1k-off/reksicherung/internal/reksicherung/mcp/sqlserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type inspectionService interface {
	ListTables(context.Context, string, string) (sqlserver.TableList, error)
	DescribeTable(context.Context, sqlserver.TableRef) (sqlserver.TableDescription, error)
	Validate(context.Context, sqlserver.ValidationQuery) (sqlserver.ValidationResult, error)
}

type listTablesArgs struct {
	Schema *string `json:"schema,omitempty" jsonschema:"Optional schema name; omit to list tables in all schemas."`
}

type describeTableArgs struct {
	Schema string `json:"schema" jsonschema:"Schema containing the table."`
	Table  string `json:"table" jsonschema:"Table whose column metadata should be inspected."`
}

type validateTableArgs struct {
	Schema string `json:"schema" jsonschema:"Schema containing the table."`
	Table  string `json:"table" jsonschema:"Table to inspect."`
	Limit  int    `json:"limit" jsonschema:"Maximum total rows to count (1 to 100)."`
}

// Custom unmarshaling makes the typed handlers reject unknown JSON properties.
// The MCP SDK validates the generated schema first; these methods ensure the
// JSON-to-Go conversion is equally strict, including nested predicate objects.
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

type tableOutput struct {
	Schema string `json:"schema" jsonschema:"Table schema."`
	Name   string `json:"name" jsonschema:"Table name."`
}

type listTablesOutput struct {
	Tables    []tableOutput `json:"tables" jsonschema:"User table metadata."`
	Truncated bool          `json:"truncated" jsonschema:"True when more tables exist than were returned."`
}

type columnOutput struct {
	Name      string `json:"name" jsonschema:"Column name."`
	DataType  string `json:"data_type" jsonschema:"SQL Server data type."`
	MaxLength int    `json:"max_length" jsonschema:"Maximum length reported by SQL Server."`
	Precision int    `json:"precision" jsonschema:"Numeric precision."`
	Scale     int    `json:"scale" jsonschema:"Numeric scale."`
	Nullable  bool   `json:"nullable" jsonschema:"Whether the column allows NULL."`
}

type describeTableOutput struct {
	Schema    string         `json:"schema" jsonschema:"Inspected table schema."`
	Table     string         `json:"table" jsonschema:"Inspected table name."`
	Columns   []columnOutput `json:"columns" jsonschema:"Column metadata only; no row values."`
	Truncated bool           `json:"truncated" jsonschema:"True when more columns exist than were returned."`
}

type validateTableOutput struct {
	ObservedRows int  `json:"observed_rows" jsonschema:"Matching rows observed, capped at limit."`
	Limit        int  `json:"limit" jsonschema:"Maximum number of matching rows counted."`
	Truncated    bool `json:"truncated" jsonschema:"True when the matching row count reached the cap."`
}

func newMCPServer(service inspectionService, database string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "reksicherung-sqlserver",
		Version: "0.1.0",
	}, &mcp.ServerOptions{
		Instructions: "Read-only SQL Server restore inspection. Results contain schema metadata or bounded aggregates, never row values. Only the configured target database is available.",
		// SDK diagnostics are disabled so protocol internals cannot accidentally
		// place prompts or tool arguments in logs.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sqlserver_list_tables",
		Description: "List at most 200 user tables in the configured database, optionally restricted to one schema. Returns names and truncation metadata only.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listTablesArgs) (*mcp.CallToolResult, listTablesOutput, error) {
		schema := ""
		if args.Schema != nil {
			schema = *args.Schema
		}
		result, err := service.ListTables(ctx, database, schema)
		if err != nil {
			return nil, listTablesOutput{}, safeToolError(err)
		}
		out := listTablesOutput{Truncated: result.Truncated, Tables: make([]tableOutput, 0, len(result.Tables))}
		for _, table := range result.Tables {
			out.Tables = append(out.Tables, tableOutput{Schema: table.Schema, Name: table.Name})
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sqlserver_describe_table",
		Description: "Return bounded SQL Server column metadata for a table in the configured database. Does not return row values.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args describeTableArgs) (*mcp.CallToolResult, describeTableOutput, error) {
		description, err := service.DescribeTable(ctx, sqlserver.TableRef{
			Database: database,
			Schema:   args.Schema,
			Table:    args.Table,
		})
		if err != nil {
			return nil, describeTableOutput{}, safeToolError(err)
		}
		out := describeTableOutput{
			Schema:    description.Ref.Schema,
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
		Name:        "sqlserver_validate_table",
		Description: "Count total rows in one table, capped at 100. Caller-supplied predicates and row values are not accepted; only the aggregate count and truncation flag are returned.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args validateTableArgs) (*mcp.CallToolResult, validateTableOutput, error) {
		result, err := service.Validate(ctx, sqlserver.ValidationQuery{
			Table: sqlserver.TableRef{
				Database: database,
				Schema:   args.Schema,
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
	case errors.Is(err, sqlserver.ErrInvalidInput):
		return errors.New("invalid tool arguments")
	case errors.Is(err, sqlserver.ErrNotFound):
		return errors.New("requested table was not found")
	case errors.Is(err, sqlserver.ErrTimeout):
		return errors.New("SQL Server inspection timed out")
	case errors.Is(err, sqlserver.ErrCanceled):
		return errors.New("SQL Server inspection canceled")
	default:
		return errors.New("SQL Server inspection failed")
	}
}
