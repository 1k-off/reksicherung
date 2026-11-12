package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/1k-off/reksicherung/internal/reksicherung/restore/artifact"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
)

type artifactOpener interface {
	Open(context.Context, string) (io.ReadCloser, error)
}

type stagedArtifact struct {
	ArtifactID string `json:"artifact_id" jsonschema:"Opaque ID for the resolved artifact; pass to the SQL Server restore MCP tool."`
	Kind       string `json:"kind" jsonschema:"Resolved database artifact kind: bak or bacpac."`
	Size       int64  `json:"size" jsonschema:"Resolved artifact size in bytes."`
}

type archiveCandidate struct {
	Path     string `json:"path" jsonschema:"Exact member path inside the archive."`
	Size     int64  `json:"size" jsonschema:"Uncompressed member size in bytes."`
	KindHint string `json:"kind_hint" jsonschema:"Suggested artifact kind from the file extension: bak or bacpac."`
}

type archiveCandidateList struct {
	Candidates []archiveCandidate `json:"candidates" jsonschema:"Terminal .bak/.bacpac members; metadata only."`
	Truncated  bool               `json:"truncated" jsonschema:"True when additional candidates were omitted by the cap."`
}

// ambiguousArchiveError is returned to MCP handlers with a parseable candidate
// list. It must remain distinct from opaque staging failures.
type ambiguousArchiveError struct {
	Candidates []archiveCandidate
	Truncated  bool
}

func (err *ambiguousArchiveError) Error() string {
	return "archive contains multiple candidate artifacts; pass member_path"
}

type backupStager struct {
	source artifactOpener
	store  *staging.Store
	limits artifact.Limits
}

func newBackupStager(source artifactOpener, store *staging.Store, limits artifact.Limits) (*backupStager, error) {
	if source == nil || store == nil || limits.MaxInputBytes < 1 ||
		limits.MaxExpandedBytes < 1 || limits.MaxTemporaryBytes < 1 {
		return nil, errors.New("invalid backup staging configuration")
	}
	return &backupStager{source: source, store: store, limits: limits}, nil
}

func (stager *backupStager) Stage(ctx context.Context, backupID, memberPath string) (stagedArtifact, error) {
	if ctx == nil || !validBackupID(backupID) {
		return stagedArtifact{}, errors.New("invalid backup selection")
	}
	if memberPath != "" && !validMemberPath(memberPath) {
		return stagedArtifact{}, errors.New("invalid member_path")
	}
	source, err := stager.source.Open(ctx, backupID)
	if err != nil {
		return stagedArtifact{}, errors.New("could not open selected backup")
	}
	resolved, kind, err := artifact.ResolveWith(ctx, source, filepath.Base(backupID), stager.limits, artifact.ResolveOptions{
		MemberPath: memberPath,
	})
	if err != nil {
		if ambiguous, ok := artifact.AsAmbiguous(err); ok {
			return stagedArtifact{}, &ambiguousArchiveError{
				Candidates: toArchiveCandidates(ambiguous.Candidates),
				Truncated:  ambiguous.Truncated,
			}
		}
		return stagedArtifact{}, errors.New("selected backup does not contain one supported SQL database artifact")
	}
	stageKind := staging.KindBAK
	if kind == artifact.KindBACPAC {
		stageKind = staging.KindBACPAC
	}
	saved, err := stager.store.Save(ctx, resolved, stageKind)
	if err != nil {
		return stagedArtifact{}, errors.New("could not stage selected backup artifact")
	}
	return stagedArtifact{ArtifactID: saved.ID, Kind: saved.Kind, Size: saved.Size}, nil
}

func (stager *backupStager) ListCandidates(ctx context.Context, backupID string) (archiveCandidateList, error) {
	if ctx == nil || !validBackupID(backupID) {
		return archiveCandidateList{}, errors.New("invalid backup selection")
	}
	source, err := stager.source.Open(ctx, backupID)
	if err != nil {
		return archiveCandidateList{}, errors.New("could not open selected backup")
	}
	listed, err := artifact.ListCandidates(ctx, source, filepath.Base(backupID), stager.limits)
	if err != nil {
		return archiveCandidateList{}, errors.New("could not list archive candidates")
	}
	return archiveCandidateList{
		Candidates: toArchiveCandidates(listed.Candidates),
		Truncated:  listed.Truncated,
	}, nil
}

func toArchiveCandidates(candidates []artifact.Candidate) []archiveCandidate {
	out := make([]archiveCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, archiveCandidate{
			Path:     candidate.Path,
			Size:     candidate.Size,
			KindHint: string(candidate.KindHint),
		})
	}
	return out
}

func validMemberPath(path string) bool {
	if path == "" || len(path) > 1024 || !utf8.ValidString(path) ||
		strings.HasPrefix(path, "/") || strings.Contains(path, `\`) ||
		strings.Contains(path, "\x00") {
		return false
	}
	for _, character := range path {
		if unicode.IsControl(character) {
			return false
		}
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".bak") || strings.HasSuffix(lower, ".bacpac")
}
