package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/restore/artifact"
	resticrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/restic"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
)

type resticFileOpener interface {
	OpenBackupFile(context.Context, resticrestore.Selection, string) (resticrestore.Snapshot, io.ReadCloser, error)
}

type resticArtifactStager struct {
	client resticFileOpener
	store  *staging.Store
	limits artifact.Limits
}

type resticStageArgs struct {
	SnapshotID string `json:"snapshot_id" jsonschema:"Full Restic snapshot ID returned by restic_list_snapshots."`
	BackupPath string `json:"backup_path" jsonschema:"One exact absolute file path in the selected snapshot; no wildcards or directory restore."`
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

func newResticArtifactClient(ctx context.Context, config serverConfig, provider secrets.SecretProvider, maxBytes int64) (*resticrestore.Client, error) {
	password, err := resolveSecret(ctx, provider, config.passwordRef)
	if err != nil {
		return nil, errors.New("could not resolve configured Restic repository password")
	}
	defer clear(password)

	environment := []resticrestore.EnvironmentVariable{{Name: "RESTIC_PASSWORD", Value: string(password)}}
	for _, item := range config.environment {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			return nil, errors.New("invalid Restic backend environment")
		}
		environment = append(environment, resticrestore.EnvironmentVariable{Name: name, Value: value})
	}
	for _, backend := range config.backendSecrets {
		value, err := resolveSecret(ctx, provider, backend.reference)
		if err != nil {
			return nil, errors.New("could not resolve configured Restic backend secret")
		}
		environment = append(environment, resticrestore.EnvironmentVariable{Name: backend.name, Value: string(value)})
		clear(value)
	}
	client, err := resticrestore.New(resticrestore.Config{
		Executable: "restic", Repository: config.repository, Timeout: 6 * time.Hour,
		MaxOutputBytes: 4 << 20, MaxStreamBytes: maxBytes, Environment: environment,
	}, resticProcessRunner{})
	if err != nil {
		return nil, errors.New("could not initialize Restic file reader")
	}
	return client, nil
}
