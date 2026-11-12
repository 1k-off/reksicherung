package main

import (
	"context"
	"errors"
	"io"

	"github.com/1k-off/reksicherung/internal/reksicherung/restore/artifact"
	resticrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/restic"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
)

type resticFileOpener interface {
	OpenBackupFile(context.Context, resticrestore.Selection, string) (resticrestore.Snapshot, io.ReadCloser, error)
}

type resticArtifactStager struct {
	client resticFileOpener
	store  *staging.Store
	limits artifact.Limits
}

type resticStagedArtifact struct {
	ArtifactID string `json:"artifact_id" jsonschema:"Opaque ID for the resolved artifact; pass to the SQL Server restore MCP tool."`
	Kind       string `json:"kind" jsonschema:"Resolved artifact kind: bak or bacpac."`
	Size       int64  `json:"size" jsonschema:"Resolved artifact size in bytes."`
	SnapshotID string `json:"snapshot_id" jsonschema:"Selected full snapshot ID."`
}

func newResticArtifactStager(client resticFileOpener, store *staging.Store, limits artifact.Limits) (*resticArtifactStager, error) {
	if client == nil || store == nil || limits.MaxInputBytes < 1 || limits.MaxExpandedBytes < 1 ||
		limits.MaxTemporaryBytes < 1 {
		return nil, errors.New("invalid Restic artifact staging configuration")
	}
	return &resticArtifactStager{client: client, store: store, limits: limits}, nil
}

func (stager *resticArtifactStager) Stage(ctx context.Context, snapshotID, backupPath string) (resticStagedArtifact, error) {
	if ctx == nil {
		return resticStagedArtifact{}, errors.New("invalid Restic artifact selection")
	}
	snapshot, source, err := stager.client.OpenBackupFile(ctx,
		resticrestore.Selection{Policy: resticrestore.SelectionID, ID: snapshotID}, backupPath)
	if err != nil {
		return resticStagedArtifact{}, errors.New("could not open selected Restic backup file")
	}
	resolved, kind, err := artifact.Resolve(ctx, source, backupPath, stager.limits)
	if err != nil {
		return resticStagedArtifact{}, errors.New("selected Restic file does not contain one supported SQL database artifact")
	}
	stageKind := staging.KindBAK
	if kind == artifact.KindBACPAC {
		stageKind = staging.KindBACPAC
	}
	saved, err := stager.store.Save(ctx, resolved, stageKind)
	if err != nil {
		return resticStagedArtifact{}, errors.New("could not stage selected Restic artifact")
	}
	return resticStagedArtifact{
		ArtifactID: saved.ID, Kind: saved.Kind, Size: saved.Size, SnapshotID: snapshot.ID,
	}, nil
}
