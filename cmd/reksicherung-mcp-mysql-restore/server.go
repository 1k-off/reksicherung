package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"

	mysqlrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/mysql"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type restoreService interface {
	RestoreDump(context.Context, string, io.ReadCloser, string) error
	CheckIntegrity(context.Context, string) (mysqlrestore.IntegrityResult, error)
	DropDatabase(context.Context, string) error
}

type artifactStore interface {
	Open(string, string) (*os.File, staging.Artifact, error)
}

type restoreBackupArgs struct {
	RunID          string `json:"run_id" jsonschema:"Unique Reksicherung run ID; used to derive the only database name this operation can modify."`
	ArtifactID     string `json:"artifact_id" jsonschema:"Opaque ID returned by the backup provider's stage tool for a logical SQL dump."`
	SourceDatabase string `json:"source_database" jsonschema:"Source database name the dump is allowed to reference (single-database dumps only)."`
}

type runArgs struct {
	RunID string `json:"run_id" jsonschema:"Unique Reksicherung run ID used to derive the database name."`
}

func (args *restoreBackupArgs) UnmarshalJSON(data []byte) error {
	type input restoreBackupArgs
	return decodeStrictJSON(data, (*input)(args))
}

func (args *runArgs) UnmarshalJSON(data []byte) error {
	type input runArgs
	return decodeStrictJSON(data, (*input)(args))
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON value")
	}
	return nil
}

type restoreBackupOutput struct {
	Restored bool   `json:"restored"`
	Database string `json:"database"`
	Artifact string `json:"artifact_id"`
}

type integrityOutput struct {
	Database        string `json:"database"`
	CommandComplete bool   `json:"command_completed" jsonschema:"True only when mysqlcheck --check completed without a command error; this is not a clean-integrity verdict."`
}

type dropOutput struct {
	Removed  bool   `json:"removed"`
	Database string `json:"database"`
}

func newRestoreMCPServer(service restoreService, artifacts artifactStore) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "reksicherung-mysql-restore",
		Version: "0.1.0",
	}, &mcp.ServerOptions{
		Instructions: "Operate only on the pre-existing MySQL server configured for this MCP service. Restore and cleanup are limited to a database derived from the supplied run_id. Only single-database logical SQL dumps are accepted; dumps that reference other databases are rejected. No container or service lifecycle is managed here. mysqlcheck command completion is not a clean-integrity verdict. If a restore fails part-way, call mysql_drop_restore_database for the run_id.",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "mysql_restore_backup",
		Description: "Restore one staged logical SQL dump (kind sql) into the run-scoped database on the pre-existing MySQL server. The dump may reference only source_database; CREATE DATABASE/USE are rewritten to the run-scoped name and any other database reference is rejected. Does not create or manage containers.",
		Annotations: mutatingToolAnnotations(false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args restoreBackupArgs) (*mcp.CallToolResult, restoreBackupOutput, error) {
		if !validOpaqueID(args.ArtifactID) || !mysqlrestore.ValidDatabaseName(args.SourceDatabase) {
			return nil, restoreBackupOutput{}, errors.New("invalid restore request")
		}
		database, err := mysqlrestore.DatabaseNameForRun(args.RunID)
		if err != nil {
			return nil, restoreBackupOutput{}, errors.New("invalid restore request")
		}
		file, _, err := artifacts.Open(args.ArtifactID, staging.KindSQL)
		if err != nil {
			return nil, restoreBackupOutput{}, errors.New("staged backup artifact not found")
		}
		if err := service.RestoreDump(ctx, args.RunID, file, args.SourceDatabase); err != nil {
			return nil, restoreBackupOutput{}, safeRestoreError(err)
		}
		return nil, restoreBackupOutput{Restored: true, Database: database, Artifact: args.ArtifactID}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "mysql_check_restore_integrity",
		Description: "Run fixed mysqlcheck --check on the run-scoped database. command_completed is not a statement that the database is free of consistency errors.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, integrityOutput, error) {
		result, err := service.CheckIntegrity(ctx, args.RunID)
		if err != nil {
			return nil, integrityOutput{}, safeRestoreError(err)
		}
		return nil, integrityOutput{Database: result.Database, CommandComplete: result.CommandCompleted}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "mysql_drop_restore_database",
		Description: "Drop only the database derived from run_id. This is destructive and cannot remove an arbitrary named database.",
		Annotations: mutatingToolAnnotations(true, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, dropOutput, error) {
		database, err := mysqlrestore.DatabaseNameForRun(args.RunID)
		if err != nil {
			return nil, dropOutput{}, errors.New("invalid cleanup request")
		}
		if err := service.DropDatabase(ctx, args.RunID); err != nil {
			return nil, dropOutput{}, safeRestoreError(err)
		}
		return nil, dropOutput{Removed: true, Database: database}, nil
	})
	return server
}

func validOpaqueID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, character := range id {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func readOnlyToolAnnotations() *mcp.ToolAnnotations {
	destructive, openWorld := false, true
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: &destructive,
		IdempotentHint:  true,
		OpenWorldHint:   &openWorld,
	}
}

func mutatingToolAnnotations(idempotent, destructive bool) *mcp.ToolAnnotations {
	readOnly, openWorld := false, true
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  idempotent,
		OpenWorldHint:   &openWorld,
	}
}

func safeRestoreError(err error) error {
	switch {
	case errors.Is(err, mysqlrestore.ErrInvalidDump):
		return errors.New("SQL dump is invalid or references a database other than the requested source database")
	case errors.Is(err, mysqlrestore.ErrDumpLimit):
		return errors.New("SQL dump exceeded the configured size limit")
	case errors.Is(err, mysqlrestore.ErrTimeout):
		return errors.New("MySQL restore operation timed out")
	case errors.Is(err, mysqlrestore.ErrCanceled):
		return errors.New("MySQL restore operation canceled")
	default:
		return errors.New("MySQL restore operation failed")
	}
}
