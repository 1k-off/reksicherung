package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"

	sqlserver "github.com/1k-off/reksicherung/internal/reksicherung/restore/sqlserver"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type restoreService interface {
	RestoreBackup(context.Context, string, io.ReadCloser, string) error
	ImportBACPAC(context.Context, string, io.ReadCloser) error
	CheckIntegrity(context.Context, string) (sqlserver.IntegrityResult, error)
	DropDatabase(context.Context, string) error
}

type artifactStore interface {
	Open(string, string) (*os.File, staging.Artifact, error)
}

type restoreBackupArgs struct {
	RunID          string `json:"run_id" jsonschema:"Unique Reksicherung run ID; used to derive the only database name this operation can modify."`
	ArtifactID     string `json:"artifact_id" jsonschema:"Opaque ID returned by the backup provider's stage tool."`
	SourceDatabase string `json:"source_database" jsonschema:"Expected source database name recorded in the backup header."`
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
	CommandComplete bool   `json:"command_completed" jsonschema:"True only when DBCC CHECKDB completed without a SQL command error; this is not a clean-integrity verdict."`
}

type dropOutput struct {
	Removed  bool   `json:"removed"`
	Database string `json:"database"`
}

func newRestoreMCPServer(service restoreService, artifacts artifactStore) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "reksicherung-sqlserver-restore",
		Version: "0.1.0",
	}, &mcp.ServerOptions{
		Instructions: "Operate only on the pre-existing SQL Server configured for this MCP service. Restore and cleanup are limited to a database derived from the supplied run_id. Backups are staged in a shared operator-managed volume; no container or service lifecycle is managed here. DBCC command completion is not a clean-integrity verdict.",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sqlserver_restore_backup",
		Description: "Restore one staged .bak or import one staged .bacpac into the run-scoped database on the pre-existing SQL Server. For .bak, the backup header must identify source_database. Does not create or manage containers.",
		Annotations: mutatingToolAnnotations(false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args restoreBackupArgs) (*mcp.CallToolResult, restoreBackupOutput, error) {
		if !validOpaqueID(args.ArtifactID) || strings.TrimSpace(args.SourceDatabase) != args.SourceDatabase {
			return nil, restoreBackupOutput{}, errors.New("invalid restore request")
		}
		database, err := sqlserver.DatabaseNameForRun(args.RunID)
		if err != nil {
			return nil, restoreBackupOutput{}, errors.New("invalid restore request")
		}
		file, artifact, err := openStagedArtifact(artifacts, args.ArtifactID)
		if err != nil {
			return nil, restoreBackupOutput{}, errors.New("staged backup artifact not found")
		}
		switch artifact.Kind {
		case staging.KindBAK:
			err = service.RestoreBackup(ctx, args.RunID, file, args.SourceDatabase)
		case staging.KindBACPAC:
			err = service.ImportBACPAC(ctx, args.RunID, file)
		default:
			_ = file.Close()
			return nil, restoreBackupOutput{}, errors.New("staged backup artifact not found")
		}
		if err != nil {
			return nil, restoreBackupOutput{}, safeRestoreError(err)
		}
		return nil, restoreBackupOutput{Restored: true, Database: database, Artifact: args.ArtifactID}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sqlserver_check_restore_integrity",
		Description: "Run fixed non-repair DBCC CHECKDB on the run-scoped database. command_completed is not a statement that the database is free of consistency errors.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, integrityOutput, error) {
		result, err := service.CheckIntegrity(ctx, args.RunID)
		if err != nil {
			return nil, integrityOutput{}, safeRestoreError(err)
		}
		return nil, integrityOutput{Database: result.Database, CommandComplete: result.CommandCompleted}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sqlserver_drop_restore_database",
		Description: "Drop only the database derived from run_id. This is destructive and cannot remove an arbitrary named database.",
		Annotations: mutatingToolAnnotations(true, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, dropOutput, error) {
		database, err := sqlserver.DatabaseNameForRun(args.RunID)
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

func openStagedArtifact(artifacts artifactStore, id string) (*os.File, staging.Artifact, error) {
	if file, artifact, err := artifacts.Open(id, staging.KindBAK); err == nil {
		return file, artifact, nil
	}
	return artifacts.Open(id, staging.KindBACPAC)
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
	case errors.Is(err, sqlserver.ErrInvalidBackupMetadata):
		return errors.New("backup metadata is invalid or does not match the requested source database")
	case errors.Is(err, sqlserver.ErrRemoteBackupLimit):
		return errors.New("backup exceeded the configured size limit")
	case errors.Is(err, sqlserver.ErrTimeout):
		return errors.New("SQL Server restore operation timed out")
	case errors.Is(err, sqlserver.ErrCanceled):
		return errors.New("SQL Server restore operation canceled")
	default:
		return errors.New("SQL Server restore operation failed")
	}
}
