package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	gobackup "github.com/1k-off/reksicherung/internal/reksicherung/mcp/gobackup"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
	"github.com/1k-off/reksicherung/internal/reksicherung/storage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxListResults = 8

type multiSourceArchives interface {
	List(context.Context, string) ([]gobackup.Backup, error)
	Latest(context.Context, string) (gobackup.Backup, error)
	Select(context.Context, string, string) (gobackup.Backup, error)
}

type multiSourceStaging interface {
	Stage(context.Context, string, string, string) (stagedArtifact, error)
	ListCandidates(context.Context, string, string) (archiveCandidateList, error)
}

type listArgs struct {
	SourceID string `json:"source_id" jsonschema:"Catalog source_id; OpenBao path secret/reksicherung/sources/<source_id>."`
	Limit    int    `json:"limit,omitempty" jsonschema:"Maximum number of metadata records to return (1 to 8; defaults to 8)."`
}

type latestArgs struct {
	SourceID string `json:"source_id" jsonschema:"Catalog source_id; OpenBao path secret/reksicherung/sources/<source_id>."`
}

type selectArgs struct {
	SourceID string `json:"source_id" jsonschema:"Catalog source_id; OpenBao path secret/reksicherung/sources/<source_id>."`
	ID       string `json:"id" jsonschema:"Exact backup ID returned by list; object contents are never read."`
}

type stageArgs struct {
	SourceID   string `json:"source_id" jsonschema:"Catalog source_id; OpenBao path secret/reksicherung/sources/<source_id>."`
	ID         string `json:"id" jsonschema:"Exact backup ID returned by list or select."`
	MemberPath string `json:"member_path,omitempty" jsonschema:"Optional exact archive member path from gobackup_list_archive_candidates when multiple .bak/.bacpac files exist."`
}

type listCandidatesArgs struct {
	SourceID string `json:"source_id" jsonschema:"Catalog source_id; OpenBao path secret/reksicherung/sources/<source_id>."`
	ID       string `json:"id" jsonschema:"Exact backup ID returned by list or select."`
}

// stageToolResult is the gobackup_stage_artifact output. On success artifact
// fields are set. On ambiguity, code is "ambiguous_archive" and candidates are
// populated (IsError=true on the CallToolResult).
type stageToolResult struct {
	ArtifactID string             `json:"artifact_id,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	Size       int64              `json:"size,omitempty"`
	Code       string             `json:"code,omitempty"`
	Message    string             `json:"message,omitempty"`
	Candidates []archiveCandidate `json:"candidates,omitempty"`
	Truncated  bool               `json:"truncated,omitempty"`
}

func (args *listArgs) UnmarshalJSON(data []byte) error {
	type input listArgs
	return decodeStrictJSON(data, (*input)(args))
}

func (args *latestArgs) UnmarshalJSON(data []byte) error {
	type input latestArgs
	return decodeStrictJSON(data, (*input)(args))
}

func (args *selectArgs) UnmarshalJSON(data []byte) error {
	type input selectArgs
	return decodeStrictJSON(data, (*input)(args))
}

func (args *stageArgs) UnmarshalJSON(data []byte) error {
	type input stageArgs
	return decodeStrictJSON(data, (*input)(args))
}

func (args *listCandidatesArgs) UnmarshalJSON(data []byte) error {
	type input listCandidatesArgs
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

type backupOutput struct {
	ID           string `json:"id" jsonschema:"Backup ID relative to the source prefix."`
	Size         int64  `json:"size" jsonschema:"Object size in bytes."`
	LastModified string `json:"last_modified" jsonschema:"Storage LastModified timestamp in RFC3339Nano format."`
}

type listOutput struct {
	Backups   []backupOutput `json:"backups" jsonschema:"Newest-first archive metadata, capped by limit."`
	Truncated bool           `json:"truncated" jsonschema:"True when this bounded response omitted additional matching archives."`
}

func newMCPServer(service multiSourceArchives, stagingServices ...multiSourceStaging) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "reksicherung-gobackup",
		Version: "0.1.0",
	}, &mcp.ServerOptions{
		Instructions: "GoBackup archive discovery per catalog source_id. Credentials and prefixes are resolved from OpenBao. Tools return object metadata only and never expose archive bytes.",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gobackup_list",
		Description: "List newest-first GoBackup archive metadata for one source_id. Returns at most 8 records and indicates if more were omitted.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listArgs) (*mcp.CallToolResult, listOutput, error) {
		if !secrets.ValidSourceID(args.SourceID) {
			return nil, listOutput{}, errors.New("invalid source_id")
		}
		limit := args.Limit
		if limit == 0 {
			limit = maxListResults
		}
		if limit < 1 || limit > maxListResults {
			return nil, listOutput{}, errors.New("limit must be between 1 and 8")
		}
		backups, err := service.List(ctx, args.SourceID)
		if err != nil {
			return nil, listOutput{}, safeToolError(err)
		}
		out := listOutput{Truncated: len(backups) > limit, Backups: make([]backupOutput, 0, min(len(backups), limit))}
		for _, backup := range backups[:min(len(backups), limit)] {
			out.Backups = append(out.Backups, toBackupOutput(backup))
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gobackup_latest",
		Description: "Return metadata for the newest GoBackup archive for one source_id. Does not read archive contents.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args latestArgs) (*mcp.CallToolResult, backupOutput, error) {
		if !secrets.ValidSourceID(args.SourceID) {
			return nil, backupOutput{}, errors.New("invalid source_id")
		}
		backup, err := service.Latest(ctx, args.SourceID)
		if err != nil {
			return nil, backupOutput{}, safeToolError(err)
		}
		return nil, toBackupOutput(backup), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gobackup_select",
		Description: "Look up metadata for one exact backup ID under one source_id. Does not read archive contents.",
		Annotations: readOnlyToolAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args selectArgs) (*mcp.CallToolResult, backupOutput, error) {
		if !secrets.ValidSourceID(args.SourceID) {
			return nil, backupOutput{}, errors.New("invalid source_id")
		}
		if !validBackupID(args.ID) {
			return nil, backupOutput{}, errors.New("invalid backup ID")
		}
		backup, err := service.Select(ctx, args.SourceID, args.ID)
		if err != nil {
			return nil, backupOutput{}, safeToolError(err)
		}
		return nil, toBackupOutput(backup), nil
	})

	if len(stagingServices) != 0 && stagingServices[0] != nil {
		destructive, idempotent, openWorld := false, false, true
		mcp.AddTool(server, &mcp.Tool{
			Name: "gobackup_list_archive_candidates",
			Description: "Download one GoBackup archive for source_id and list terminal .bak/.bacpac member paths " +
				"(metadata only). Use when staging reports ambiguous_archive, then pass member_path to gobackup_stage_artifact.",
			Annotations: readOnlyToolAnnotations(),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, args listCandidatesArgs) (*mcp.CallToolResult, archiveCandidateList, error) {
			if !secrets.ValidSourceID(args.SourceID) {
				return nil, archiveCandidateList{}, errors.New("invalid source_id")
			}
			if !validBackupID(args.ID) {
				return nil, archiveCandidateList{}, errors.New("invalid backup ID")
			}
			result, err := stagingServices[0].ListCandidates(ctx, args.SourceID, args.ID)
			if err != nil {
				return nil, archiveCandidateList{}, errors.New("GoBackup archive candidate listing failed")
			}
			return nil, result, nil
		})

		mcp.AddTool(server, &mcp.Tool{
			Name: "gobackup_stage_artifact",
			Description: "Read the selected GoBackup object for source_id, resolve nested wrappers, and stage one .bak or BACPAC file. " +
				"When multiple terminal members exist, returns ambiguous_archive with candidates; retry with member_path.",
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: &destructive,
				IdempotentHint:  idempotent,
				OpenWorldHint:   &openWorld,
			},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, args stageArgs) (*mcp.CallToolResult, stageToolResult, error) {
			if !secrets.ValidSourceID(args.SourceID) {
				return nil, stageToolResult{}, errors.New("invalid source_id")
			}
			if !validBackupID(args.ID) {
				return nil, stageToolResult{}, errors.New("invalid backup ID")
			}
			if args.MemberPath != "" && !validMemberPath(args.MemberPath) {
				return nil, stageToolResult{}, errors.New("invalid member_path")
			}
			result, err := stagingServices[0].Stage(ctx, args.SourceID, args.ID, args.MemberPath)
			if err != nil {
				var ambiguous *ambiguousArchiveError
				if errors.As(err, &ambiguous) {
					return &mcp.CallToolResult{IsError: true}, stageToolResult{
						Code:       "ambiguous_archive",
						Message:    "archive contains multiple candidate artifacts; pass member_path",
						Candidates: ambiguous.Candidates,
						Truncated:  ambiguous.Truncated,
					}, nil
				}
				return nil, stageToolResult{}, errors.New("GoBackup artifact staging failed")
			}
			return nil, stageToolResult{
				ArtifactID: result.ArtifactID,
				Kind:       result.Kind,
				Size:       result.Size,
			}, nil
		})
	}
	return server
}

func toBackupOutput(backup gobackup.Backup) backupOutput {
	return backupOutput{
		ID:           backup.ID,
		Size:         backup.Size,
		LastModified: backup.LastModified.UTC().Format(time.RFC3339Nano),
	}
}

func validBackupID(id string) bool {
	if id == "" || len(id) > 1024 || !utf8.ValidString(id) || strings.HasPrefix(id, "/") ||
		strings.HasSuffix(id, "/") || strings.Contains(id, `\`) ||
		(len(id) >= 2 && ((id[0] >= 'a' && id[0] <= 'z') || (id[0] >= 'A' && id[0] <= 'Z')) && id[1] == ':') {
		return false
	}
	for _, character := range id {
		if unicode.IsControl(character) {
			return false
		}
	}
	for _, segment := range strings.Split(id, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
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
	if errors.Is(err, storage.ErrObjectNotFound) {
		return errors.New("requested backup was not found")
	}
	return errors.New("GoBackup archive discovery failed")
}
