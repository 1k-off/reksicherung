package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"

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

func (stager *backupStager) Stage(ctx context.Context, backupID string) (stagedArtifact, error) {
	if ctx == nil || !validBackupID(backupID) {
		return stagedArtifact{}, errors.New("invalid backup selection")
	}
	source, err := stager.source.Open(ctx, backupID)
	if err != nil {
		return stagedArtifact{}, errors.New("could not open selected backup")
	}
	resolved, kind, err := artifact.Resolve(ctx, source, filepath.Base(backupID), stager.limits)
	if err != nil {
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
