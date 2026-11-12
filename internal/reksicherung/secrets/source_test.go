package secrets

import (
	"context"
	"errors"
	"testing"
)

type mapProvider map[string]map[string]string

func (p mapProvider) Resolve(_ context.Context, reference Reference) ([]byte, error) {
	if !reference.Valid() {
		return nil, ErrInvalidReference
	}
	values, ok := p[reference.Path]
	if !ok {
		return nil, errors.New("not found")
	}
	value, ok := values[reference.Key]
	if !ok || value == "" {
		return nil, errors.New("not found")
	}
	return []byte(value), nil
}

func TestValidSourceID(t *testing.T) {
	for _, id := range []string{"shop-production", "a", "db1"} {
		if !ValidSourceID(id) {
			t.Fatalf("ValidSourceID(%q) = false", id)
		}
	}
	for _, id := range []string{"", "Shop", "-bad", "bad_id", "has.dot", "UPPER"} {
		if ValidSourceID(id) {
			t.Fatalf("ValidSourceID(%q) = true", id)
		}
	}
}

func TestResolveSourceMergesDefaultsAndIsolatesSources(t *testing.T) {
	provider := mapProvider{
		DefaultsPath: {
			FieldS3Endpoint: "https://objects.example.test",
			FieldS3Region:   "fsn1",
			FieldS3Bucket:   "backups",
		},
		SourcesPathPrefix + "shop-production": {
			FieldS3AccessKeyID:     "shop-key",
			FieldS3SecretAccessKey: "shop-secret",
			FieldS3PathPrefix:      "shop/production/",
		},
		SourcesPathPrefix + "reporting-production": {
			FieldS3AccessKeyID:     "report-key",
			FieldS3SecretAccessKey: "report-secret",
			FieldS3PathPrefix:      "reporting/production/",
			FieldS3Bucket:          "other-bucket",
		},
	}

	shop, err := ResolveSource(context.Background(), provider, "shop-production", "gobackup")
	if err != nil {
		t.Fatalf("ResolveSource(shop) error = %v", err)
	}
	defer shop.Clear()
	if shop.Bucket != "backups" || shop.PathPrefix != "shop/production/" || string(shop.AccessKeyID) != "shop-key" {
		t.Fatalf("shop material = %#v", shop)
	}

	reporting, err := ResolveSource(context.Background(), provider, "reporting-production", "gobackup")
	if err != nil {
		t.Fatalf("ResolveSource(reporting) error = %v", err)
	}
	defer reporting.Clear()
	if reporting.Bucket != "other-bucket" || string(reporting.AccessKeyID) != "report-key" {
		t.Fatalf("reporting material = %#v", reporting)
	}
	if string(shop.AccessKeyID) == string(reporting.AccessKeyID) {
		t.Fatal("sources unexpectedly share credentials")
	}
}

func TestResolveSourceRequiresResticFields(t *testing.T) {
	provider := mapProvider{
		SourcesPathPrefix + "app": {
			FieldS3AccessKeyID:     "k",
			FieldS3SecretAccessKey: "s",
			FieldS3Endpoint:        "https://objects.example.test",
			FieldS3Region:          "fsn1",
			FieldS3Bucket:          "backups",
		},
	}
	if _, err := ResolveSource(context.Background(), provider, "app", "restic"); !errors.Is(err, ErrIncompleteSource) {
		t.Fatalf("error = %v, want ErrIncompleteSource", err)
	}
	provider[SourcesPathPrefix+"app"][FieldResticRepository] = "s3:https://objects.example.test/backups/restic/app"
	provider[SourcesPathPrefix+"app"][FieldResticPassword] = "repo-pass"
	material, err := ResolveSource(context.Background(), provider, "app", "restic")
	if err != nil {
		t.Fatalf("ResolveSource(restic) error = %v", err)
	}
	material.Clear()
}
