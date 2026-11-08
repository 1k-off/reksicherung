package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"

	resticservice "github.com/1k-off/reksicherung/internal/reksicherung/mcp/restic"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listSnapshotsArgs struct {
	Limit int `json:"limit" jsonschema:"Maximum number of snapshots to return (1 to 100)."`
}

type selectSnapshotArgs struct {
	ID string `json:"id" jsonschema:"Full snapshot ID returned by restic_list_snapshots."`
}

func (args *listSnapshotsArgs) UnmarshalJSON(data []byte) error {
	type input listSnapshotsArgs
	return decodeStrictJSON(data, (*input)(args))
}

func (args *selectSnapshotArgs) UnmarshalJSON(data []byte) error {
	type input selectSnapshotArgs
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

type snapshotOutput struct {
	ID       string   `json:"id" jsonschema:"Full Restic snapshot ID; metadata selector only."`
	Time     string   `json:"time" jsonschema:"Snapshot creation time in RFC3339 format."`
	Hostname string   `json:"hostname" jsonschema:"Host name recorded in the snapshot metadata."`
	Tags     []string `json:"tags" jsonschema:"Snapshot tags; paths and file contents are not returned."`
}

type listSnapshotsOutput struct {
	Snapshots []snapshotOutput `json:"snapshots" jsonschema:"Bounded snapshot metadata."`
	Truncated bool             `json:"truncated" jsonschema:"True when additional snapshots were omitted."`
}

func newMCPServer(instance service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "reksicherung-restic",
		Version: "0.1.0",
	}, &mcp.ServerOptions{
		Instructions: "Read-only Restic snapshot discovery. Only bounded snapshot identifiers and metadata are available. No restore or repository-content operations are exposed.",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "restic_list_snapshots",
		Description: "List up to the requested number of recent Restic snapshots from the configured repository. Returns IDs and limited metadata only, never paths or file contents.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listSnapshotsArgs) (*mcp.CallToolResult, listSnapshotsOutput, error) {
		result, err := instance.ListSnapshots(ctx, args.Limit)
		if err != nil {
			return nil, listSnapshotsOutput{}, safeToolError(err)
		}
		out := listSnapshotsOutput{
			Truncated: result.Truncated,
			Snapshots: make([]snapshotOutput, 0, len(result.Snapshots)),
		}
		for _, snapshot := range result.Snapshots {
			out.Snapshots = append(out.Snapshots, toSnapshotOutput(snapshot))
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "restic_select_snapshot",
		Description: "Explicitly select one full snapshot ID and return its metadata for a later, separate workflow. This does not restore or read repository contents.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args selectSnapshotArgs) (*mcp.CallToolResult, snapshotOutput, error) {
		snapshot, err := instance.SelectSnapshot(ctx, args.ID)
		if err != nil {
			return nil, snapshotOutput{}, safeToolError(err)
		}
		return nil, toSnapshotOutput(snapshot), nil
	})
	return server
}

func toSnapshotOutput(snapshot resticservice.Snapshot) snapshotOutput {
	return snapshotOutput{
		ID: snapshot.ID, Time: snapshot.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Hostname: snapshot.Hostname, Tags: append([]string{}, snapshot.Tags...),
	}
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
	case errors.Is(err, resticservice.ErrInvalidInput):
		return errors.New("invalid tool arguments")
	case errors.Is(err, resticservice.ErrNotFound):
		return errors.New("requested snapshot was not found")
	case errors.Is(err, resticservice.ErrTimeout):
		return errors.New("Restic snapshot discovery timed out")
	case errors.Is(err, resticservice.ErrCanceled):
		return errors.New("Restic snapshot discovery canceled")
	default:
		return errors.New("Restic snapshot discovery failed")
	}
}
