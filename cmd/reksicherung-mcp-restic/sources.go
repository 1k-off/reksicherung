package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	resticservice "github.com/1k-off/reksicherung/internal/reksicherung/mcp/restic"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/artifact"
	resticrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/restic"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
)

type resticSourceGateway struct {
	provider   secrets.SecretProvider
	stageStore *staging.Store
	limits     artifact.Limits
	maxBytes   int64
	cacheDir   string

	mu    sync.Mutex
	cache map[string]*cachedResticSource
}

type cachedResticSource struct {
	service *resticservice.Service
	stager  *resticArtifactStager
}

func newResticSourceGateway(provider secrets.SecretProvider, stageStore *staging.Store, limits artifact.Limits, maxBytes int64, cacheDir string) (*resticSourceGateway, error) {
	if provider == nil || stageStore == nil || maxBytes < 1 || strings.TrimSpace(cacheDir) == "" {
		return nil, errors.New("invalid Restic source gateway configuration")
	}
	return &resticSourceGateway{
		provider: provider, stageStore: stageStore, limits: limits, maxBytes: maxBytes,
		cacheDir: cacheDir, cache: make(map[string]*cachedResticSource),
	}, nil
}

func (gateway *resticSourceGateway) ListSnapshots(ctx context.Context, sourceID string, limit int) (resticservice.SnapshotList, error) {
	cached, err := gateway.forSource(ctx, sourceID)
	if err != nil {
		return resticservice.SnapshotList{}, err
	}
	return cached.service.ListSnapshots(ctx, limit)
}

func (gateway *resticSourceGateway) SelectSnapshot(ctx context.Context, sourceID, id string) (resticservice.Snapshot, error) {
	cached, err := gateway.forSource(ctx, sourceID)
	if err != nil {
		return resticservice.Snapshot{}, err
	}
	return cached.service.SelectSnapshot(ctx, id)
}

func (gateway *resticSourceGateway) Stage(ctx context.Context, sourceID, snapshotID, backupPath string) (resticStagedArtifact, error) {
	cached, err := gateway.forSource(ctx, sourceID)
	if err != nil {
		return resticStagedArtifact{}, err
	}
	return cached.stager.Stage(ctx, snapshotID, backupPath)
}

func (gateway *resticSourceGateway) forSource(ctx context.Context, sourceID string) (*cachedResticSource, error) {
	if !secrets.ValidSourceID(sourceID) {
		return nil, errors.New("invalid source_id")
	}
	gateway.mu.Lock()
	if cached, ok := gateway.cache[sourceID]; ok {
		gateway.mu.Unlock()
		return cached, nil
	}
	gateway.mu.Unlock()

	material, err := secrets.ResolveSource(ctx, gateway.provider, sourceID, "restic")
	if err != nil {
		return nil, errors.New("could not resolve OpenBao material for source_id")
	}
	defer material.Clear()

	environment := []string{
		"AWS_ACCESS_KEY_ID=" + string(material.AccessKeyID),
		"AWS_SECRET_ACCESS_KEY=" + string(material.SecretAccessKey),
		"AWS_DEFAULT_REGION=" + material.Region,
		// Distroless nonroot cannot create /.cache; keep restic cache on the
		// shared artifact volume.
		"RESTIC_CACHE_DIR=" + gateway.cacheDir,
		"XDG_CACHE_HOME=" + gateway.cacheDir,
	}
	service, err := resticservice.New(resticservice.Options{
		Binary: "restic", Repository: material.ResticRepository, Password: string(material.ResticPassword),
		Environment: environment, Timeout: 2 * time.Minute, MaxSnapshots: 100,
	})
	if err != nil {
		return nil, errors.New("could not initialize Restic snapshot discovery for source_id")
	}

	restoreEnv := []resticrestore.EnvironmentVariable{
		{Name: "RESTIC_PASSWORD", Value: string(material.ResticPassword)},
		{Name: "AWS_ACCESS_KEY_ID", Value: string(material.AccessKeyID)},
		{Name: "AWS_SECRET_ACCESS_KEY", Value: string(material.SecretAccessKey)},
		{Name: "AWS_DEFAULT_REGION", Value: material.Region},
		{Name: "RESTIC_CACHE_DIR", Value: gateway.cacheDir},
		{Name: "XDG_CACHE_HOME", Value: gateway.cacheDir},
	}
	client, err := resticrestore.New(resticrestore.Config{
		Executable: "restic", Repository: material.ResticRepository, Timeout: 6 * time.Hour,
		MaxOutputBytes: 4 << 20, MaxStreamBytes: gateway.maxBytes, Environment: restoreEnv,
	}, resticProcessRunner{})
	if err != nil {
		return nil, errors.New("could not initialize Restic file reader for source_id")
	}
	stager, err := newResticArtifactStager(client, gateway.stageStore, gateway.limits)
	if err != nil {
		return nil, errors.New("could not initialize Restic staging for source_id")
	}

	cached := &cachedResticSource{service: service, stager: stager}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if existing, ok := gateway.cache[sourceID]; ok {
		return existing, nil
	}
	gateway.cache[sourceID] = cached
	return cached, nil
}
