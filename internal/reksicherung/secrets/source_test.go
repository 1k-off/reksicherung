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
			FieldStorageType: StorageTypeS3,
			FieldS3Endpoint:  "https://objects.example.test",
			FieldS3Region:    "fsn1",
			FieldS3Bucket:    "backups",
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
	if shop.StorageType != StorageTypeS3 || shop.Bucket != "backups" || shop.PathPrefix != "shop/production/" || string(shop.AccessKeyID) != "shop-key" {
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

func TestResolveSourceRequiresStorageType(t *testing.T) {
	provider := mapProvider{
		SourcesPathPrefix + "app": {
			FieldS3AccessKeyID:     "k",
			FieldS3SecretAccessKey: "s",
			FieldS3Endpoint:        "https://objects.example.test",
			FieldS3Region:          "fsn1",
			FieldS3Bucket:          "backups",
			FieldS3PathPrefix:      "app/",
		},
	}
	if _, err := ResolveSource(context.Background(), provider, "app", "gobackup"); !errors.Is(err, ErrIncompleteSource) {
		t.Fatalf("error = %v, want ErrIncompleteSource", err)
	}
}

func TestResolveSourceRequiresResticFields(t *testing.T) {
	provider := mapProvider{
		SourcesPathPrefix + "app": {
			FieldStorageType:       StorageTypeS3,
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

func TestResolveSourceAzureKeyAndServicePrincipal(t *testing.T) {
	base := map[string]string{
		FieldStorageType:     StorageTypeAzure,
		FieldAzureAccountName: "mystorageacct",
		FieldAzureContainer:  "backups",
		FieldAzurePathPrefix: "shop/production/",
	}

	t.Run("account key gobackup", func(t *testing.T) {
		fields := cloneMap(base)
		fields[FieldAzureAccountKey] = "account-key-value"
		provider := mapProvider{SourcesPathPrefix + "shop": fields}
		material, err := ResolveSource(context.Background(), provider, "shop", "gobackup")
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		defer material.Clear()
		if material.StorageType != StorageTypeAzure || string(material.AzureAccountKey) != "account-key-value" {
			t.Fatalf("material = %#v", material)
		}
	})

	t.Run("service principal gobackup", func(t *testing.T) {
		fields := cloneMap(base)
		fields[FieldAzureTenantID] = "tenant"
		fields[FieldAzureClientID] = "client"
		fields[FieldAzureClientSecret] = "secret"
		provider := mapProvider{SourcesPathPrefix + "shop": fields}
		material, err := ResolveSource(context.Background(), provider, "shop", "gobackup")
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		material.Clear()
	})

	t.Run("mixed auth rejected", func(t *testing.T) {
		fields := cloneMap(base)
		fields[FieldAzureAccountKey] = "key"
		fields[FieldAzureTenantID] = "tenant"
		fields[FieldAzureClientID] = "client"
		fields[FieldAzureClientSecret] = "secret"
		provider := mapProvider{SourcesPathPrefix + "shop": fields}
		if _, err := ResolveSource(context.Background(), provider, "shop", "gobackup"); !errors.Is(err, ErrIncompleteSource) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("s3 fields with azure type rejected", func(t *testing.T) {
		fields := cloneMap(base)
		fields[FieldAzureAccountKey] = "key"
		fields[FieldS3Bucket] = "nope"
		provider := mapProvider{SourcesPathPrefix + "shop": fields}
		if _, err := ResolveSource(context.Background(), provider, "shop", "gobackup"); !errors.Is(err, ErrIncompleteSource) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("restic requires account key or SAS and azure repo", func(t *testing.T) {
		fields := cloneMap(base)
		fields[FieldAzureTenantID] = "tenant"
		fields[FieldAzureClientID] = "client"
		fields[FieldAzureClientSecret] = "secret"
		fields[FieldResticRepository] = "azure:backups:/shop"
		fields[FieldResticPassword] = "pass"
		provider := mapProvider{SourcesPathPrefix + "shop": fields}
		if _, err := ResolveSource(context.Background(), provider, "shop", "restic"); !errors.Is(err, ErrIncompleteSource) {
			t.Fatalf("sp-only restic error = %v", err)
		}

		fields = cloneMap(base)
		fields[FieldAzureAccountKey] = "key"
		fields[FieldResticRepository] = "azure:backups:/shop/"
		fields[FieldResticPassword] = "pass"
		provider = mapProvider{SourcesPathPrefix + "shop": fields}
		material, err := ResolveSource(context.Background(), provider, "shop", "restic")
		if err != nil {
			t.Fatalf("key restic error = %v", err)
		}
		material.Clear()

		fields = cloneMap(base)
		fields[FieldAzureAccountSAS] = "sv=2024&sig=abc"
		fields[FieldResticRepository] = "azure:backups:/shop/"
		fields[FieldResticPassword] = "pass"
		provider = mapProvider{SourcesPathPrefix + "shop": fields}
		material, err = ResolveSource(context.Background(), provider, "shop", "restic")
		if err != nil {
			t.Fatalf("sas restic error = %v", err)
		}
		material.Clear()
	})

	t.Run("sas gobackup", func(t *testing.T) {
		fields := cloneMap(base)
		fields[FieldAzureAccountSAS] = "?sv=2024&sig=abc"
		provider := mapProvider{SourcesPathPrefix + "shop": fields}
		material, err := ResolveSource(context.Background(), provider, "shop", "gobackup")
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		defer material.Clear()
		if string(material.AzureAccountSAS) != "?sv=2024&sig=abc" {
			t.Fatalf("sas = %q", material.AzureAccountSAS)
		}
	})
}

func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
