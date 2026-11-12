package main

import (
	"context"
	"errors"
	"sync"

	gobackup "github.com/1k-off/reksicherung/internal/reksicherung/mcp/gobackup"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/artifact"
	"github.com/1k-off/reksicherung/internal/reksicherung/restore/staging"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
	"github.com/1k-off/reksicherung/internal/reksicherung/storage/s3"
)

type sourceGateway struct {
	provider   secrets.SecretProvider
	stageStore *staging.Store
	limits     artifact.Limits
	maxBytes   int64

	mu    sync.Mutex
	cache map[string]*cachedSource
}

type cachedSource struct {
	service *gobackup.Service
	stager  *backupStager
}

func newSourceGateway(provider secrets.SecretProvider, stageStore *staging.Store, limits artifact.Limits, maxBytes int64) (*sourceGateway, error) {
	if provider == nil || stageStore == nil || maxBytes < 1 {
		return nil, errors.New("invalid source gateway configuration")
	}
	return &sourceGateway{
		provider:   provider,
		stageStore: stageStore,
		limits:     limits,
		maxBytes:   maxBytes,
		cache:      make(map[string]*cachedSource),
	}, nil
}

func (gateway *sourceGateway) List(ctx context.Context, sourceID string) ([]gobackup.Backup, error) {
	service, _, err := gateway.forSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	return service.List(ctx)
}

func (gateway *sourceGateway) Latest(ctx context.Context, sourceID string) (gobackup.Backup, error) {
	service, _, err := gateway.forSource(ctx, sourceID)
	if err != nil {
		return gobackup.Backup{}, err
	}
	return service.Latest(ctx)
}

func (gateway *sourceGateway) Select(ctx context.Context, sourceID, backupID string) (gobackup.Backup, error) {
	service, _, err := gateway.forSource(ctx, sourceID)
	if err != nil {
		return gobackup.Backup{}, err
	}
	return service.Select(ctx, backupID)
}

func (gateway *sourceGateway) Stage(ctx context.Context, sourceID, backupID, memberPath string) (stagedArtifact, error) {
	_, stager, err := gateway.forSource(ctx, sourceID)
	if err != nil {
		return stagedArtifact{}, err
	}
	return stager.Stage(ctx, backupID, memberPath)
}

func (gateway *sourceGateway) ListCandidates(ctx context.Context, sourceID, backupID string) (archiveCandidateList, error) {
	_, stager, err := gateway.forSource(ctx, sourceID)
	if err != nil {
		return archiveCandidateList{}, err
	}
	return stager.ListCandidates(ctx, backupID)
}

func (gateway *sourceGateway) forSource(ctx context.Context, sourceID string) (*gobackup.Service, *backupStager, error) {
	if !secrets.ValidSourceID(sourceID) {
		return nil, nil, errors.New("invalid source_id")
	}
	gateway.mu.Lock()
	if cached, ok := gateway.cache[sourceID]; ok {
		gateway.mu.Unlock()
		return cached.service, cached.stager, nil
	}
	gateway.mu.Unlock()

	material, err := secrets.ResolveSource(ctx, gateway.provider, sourceID, "gobackup")
	if err != nil {
		return nil, nil, errors.New("could not resolve OpenBao material for source_id")
	}
	defer material.Clear()

	store, err := s3.New(s3.Config{
		Endpoint:        material.Endpoint,
		Region:          material.Region,
		Bucket:          material.Bucket,
		AccessKeyID:     string(material.AccessKeyID),
		SecretAccessKey: string(material.SecretAccessKey),
	})
	if err != nil {
		return nil, nil, errors.New("invalid S3 object-storage configuration for source_id")
	}
	service, err := gobackup.New(store, gobackup.Config{
		Prefix:         material.PathPrefix,
		PageSize:       100,
		MaxBackups:     1000,
		MaxPages:       10,
		MaxObjectBytes: gateway.maxBytes,
	})
	if err != nil {
		return nil, nil, errors.New("could not initialize GoBackup discovery for source_id")
	}
	stager, err := newBackupStager(service, gateway.stageStore, gateway.limits)
	if err != nil {
		return nil, nil, errors.New("could not initialize staging for source_id")
	}

	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if cached, ok := gateway.cache[sourceID]; ok {
		return cached.service, cached.stager, nil
	}
	gateway.cache[sourceID] = &cachedSource{service: service, stager: stager}
	return service, stager, nil
}
