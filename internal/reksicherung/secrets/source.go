package secrets

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
)

const (
	// DefaultMount is the OpenBao KV v2 mount used by the project convention.
	DefaultMount = "secret"
	// DefaultsPath holds optional project-wide storage coordinates.
	DefaultsPath = "reksicherung/defaults"
	// SourcesPathPrefix is followed by the catalog source id.
	SourcesPathPrefix = "reksicherung/sources/"
)

// Well-known KV field names for per-source material.
const (
	FieldStorageType = "STORAGE_TYPE"

	FieldS3AccessKeyID     = "S3_ACCESS_KEY_ID"
	FieldS3SecretAccessKey = "S3_SECRET_ACCESS_KEY"
	FieldS3Endpoint        = "S3_ENDPOINT"
	FieldS3Region          = "S3_REGION"
	FieldS3Bucket          = "S3_BUCKET"
	FieldS3PathPrefix      = "S3_PATH_PREFIX"

	FieldAzureAccountName   = "AZURE_ACCOUNT_NAME"
	FieldAzureContainer     = "AZURE_CONTAINER"
	FieldAzurePathPrefix    = "AZURE_PATH_PREFIX"
	FieldAzureEndpointSuffix = "AZURE_ENDPOINT_SUFFIX"
	FieldAzureAccountKey    = "AZURE_ACCOUNT_KEY"
	FieldAzureAccountSAS    = "AZURE_ACCOUNT_SAS"
	FieldAzureTenantID      = "AZURE_TENANT_ID"
	FieldAzureClientID      = "AZURE_CLIENT_ID"
	FieldAzureClientSecret  = "AZURE_CLIENT_SECRET"

	FieldResticRepository = "RESTIC_REPOSITORY"
	FieldResticPassword   = "RESTIC_PASSWORD"

	StorageTypeS3    = "s3"
	StorageTypeAzure = "azure"
)

// ErrInvalidSourceID indicates the catalog/OpenBao source id is unsafe.
var ErrInvalidSourceID = errors.New("secrets: invalid source id")

// ErrIncompleteSource indicates required OpenBao fields are missing after merge.
var ErrIncompleteSource = errors.New("secrets: incomplete source material")

// SourceMaterial is the resolved, provider-ready configuration for one catalog
// source. Callers must Clear sensitive fields when finished.
type SourceMaterial struct {
	SourceID    string
	StorageType string

	Endpoint   string
	Region     string
	Bucket     string
	PathPrefix string

	AccessKeyID     []byte
	SecretAccessKey []byte

	AzureAccountName    string
	AzureContainer      string
	AzurePathPrefix     string
	AzureEndpointSuffix string
	AzureAccountKey     []byte
	AzureAccountSAS     []byte
	AzureTenantID       []byte
	AzureClientID       []byte
	AzureClientSecret   []byte

	ResticRepository string
	ResticPassword   []byte
}

// Clear overwrites secret-bearing slices.
func (m *SourceMaterial) Clear() {
	if m == nil {
		return
	}
	clearBytes(m.AccessKeyID)
	clearBytes(m.SecretAccessKey)
	clearBytes(m.AzureAccountKey)
	clearBytes(m.AzureAccountSAS)
	clearBytes(m.AzureTenantID)
	clearBytes(m.AzureClientID)
	clearBytes(m.AzureClientSecret)
	clearBytes(m.ResticPassword)
	m.AccessKeyID = nil
	m.SecretAccessKey = nil
	m.AzureAccountKey = nil
	m.AzureAccountSAS = nil
	m.AzureTenantID = nil
	m.AzureClientID = nil
	m.AzureClientSecret = nil
	m.ResticPassword = nil
}

// ValidSourceID reports whether id may be used as an OpenBao path segment and
// catalog backup id (lowercase DNS-like label with hyphens).
func ValidSourceID(id string) bool {
	if id == "" || len(id) > 127 {
		return false
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return id[0] >= 'a' && id[0] <= 'z' || id[0] >= '0' && id[0] <= '9'
}

// SourcePath returns the KV path for a source id.
func SourcePath(sourceID string) (string, error) {
	if !ValidSourceID(sourceID) {
		return "", ErrInvalidSourceID
	}
	return SourcesPathPrefix + sourceID, nil
}

// SourceReference builds openbao://secret/reksicherung/sources/<id>#<field>.
func SourceReference(sourceID, field string) (Reference, error) {
	path, err := SourcePath(sourceID)
	if err != nil {
		return Reference{}, err
	}
	if !validKey(field) {
		return Reference{}, ErrInvalidReference
	}
	return Reference{Provider: "openbao", Mount: DefaultMount, Path: path, Key: field}, nil
}

// ResolveSource loads defaults + source KV and merges them for the given
// provider ("gobackup" or "restic"). Missing required fields fail closed.
func ResolveSource(ctx context.Context, provider SecretProvider, sourceID, backupProvider string) (SourceMaterial, error) {
	if provider == nil || !ValidSourceID(sourceID) {
		return SourceMaterial{}, ErrInvalidSourceID
	}
	if backupProvider != "gobackup" && backupProvider != "restic" {
		return SourceMaterial{}, fmt.Errorf("%w: unsupported provider", ErrIncompleteSource)
	}

	defaults, _ := resolvePathMap(ctx, provider, DefaultsPath)
	path, err := SourcePath(sourceID)
	if err != nil {
		return SourceMaterial{}, err
	}
	source, err := resolvePathMap(ctx, provider, path)
	if err != nil {
		return SourceMaterial{}, err
	}

	merged := mergeMaps(defaults, source)
	storageType := strings.ToLower(strings.TrimSpace(merged[FieldStorageType]))
	material := SourceMaterial{
		SourceID:            sourceID,
		StorageType:         storageType,
		Endpoint:            strings.TrimSpace(merged[FieldS3Endpoint]),
		Region:              strings.TrimSpace(merged[FieldS3Region]),
		Bucket:              strings.TrimSpace(merged[FieldS3Bucket]),
		PathPrefix:          strings.TrimSpace(merged[FieldS3PathPrefix]),
		AccessKeyID:         []byte(merged[FieldS3AccessKeyID]),
		SecretAccessKey:     []byte(merged[FieldS3SecretAccessKey]),
		AzureAccountName:    strings.TrimSpace(merged[FieldAzureAccountName]),
		AzureContainer:      strings.TrimSpace(merged[FieldAzureContainer]),
		AzurePathPrefix:     strings.TrimSpace(merged[FieldAzurePathPrefix]),
		AzureEndpointSuffix: strings.TrimSpace(merged[FieldAzureEndpointSuffix]),
		AzureAccountKey:     []byte(merged[FieldAzureAccountKey]),
		AzureAccountSAS:     []byte(merged[FieldAzureAccountSAS]),
		AzureTenantID:       []byte(merged[FieldAzureTenantID]),
		AzureClientID:       []byte(merged[FieldAzureClientID]),
		AzureClientSecret:   []byte(merged[FieldAzureClientSecret]),
		ResticRepository:    strings.TrimSpace(merged[FieldResticRepository]),
		ResticPassword:      []byte(merged[FieldResticPassword]),
	}

	switch storageType {
	case StorageTypeS3:
		if err := validateS3Material(&material, backupProvider); err != nil {
			material.Clear()
			return SourceMaterial{}, err
		}
	case StorageTypeAzure:
		if err := validateAzureMaterial(&material, backupProvider); err != nil {
			material.Clear()
			return SourceMaterial{}, err
		}
	default:
		material.Clear()
		return SourceMaterial{}, fmt.Errorf("%w: STORAGE_TYPE must be s3 or azure", ErrIncompleteSource)
	}
	return material, nil
}

func validateS3Material(material *SourceMaterial, backupProvider string) error {
	if hasAnyAzureFields(material) {
		return fmt.Errorf("%w: AZURE_* fields are not valid when STORAGE_TYPE=s3", ErrIncompleteSource)
	}
	if len(material.AccessKeyID) == 0 || len(material.SecretAccessKey) == 0 {
		return fmt.Errorf("%w: missing S3 credentials", ErrIncompleteSource)
	}
	if material.Endpoint == "" || material.Region == "" || material.Bucket == "" {
		return fmt.Errorf("%w: missing S3 endpoint, region, or bucket", ErrIncompleteSource)
	}
	if !validS3Endpoint(material.Endpoint) {
		return fmt.Errorf("%w: invalid S3 endpoint", ErrIncompleteSource)
	}
	switch backupProvider {
	case "gobackup":
		if !validGoBackupPrefix(material.PathPrefix) {
			return fmt.Errorf("%w: missing or invalid S3_PATH_PREFIX", ErrIncompleteSource)
		}
	case "restic":
		if material.ResticRepository == "" || len(material.ResticPassword) == 0 {
			return fmt.Errorf("%w: missing RESTIC_REPOSITORY or RESTIC_PASSWORD", ErrIncompleteSource)
		}
	}
	return nil
}

func validateAzureMaterial(material *SourceMaterial, backupProvider string) error {
	if hasAnyS3Fields(material) {
		return fmt.Errorf("%w: S3_* fields are not valid when STORAGE_TYPE=azure", ErrIncompleteSource)
	}
	if material.AzureAccountName == "" || material.AzureContainer == "" {
		return fmt.Errorf("%w: missing AZURE_ACCOUNT_NAME or AZURE_CONTAINER", ErrIncompleteSource)
	}
	if !validAzureAccountName(material.AzureAccountName) || !validAzureContainer(material.AzureContainer) {
		return fmt.Errorf("%w: invalid AZURE_ACCOUNT_NAME or AZURE_CONTAINER", ErrIncompleteSource)
	}
	if material.AzureEndpointSuffix != "" && !validAzureEndpointSuffix(material.AzureEndpointSuffix) {
		return fmt.Errorf("%w: invalid AZURE_ENDPOINT_SUFFIX", ErrIncompleteSource)
	}

	hasKey := len(material.AzureAccountKey) > 0
	hasSAS := len(material.AzureAccountSAS) > 0
	hasSP := len(material.AzureTenantID) > 0 || len(material.AzureClientID) > 0 || len(material.AzureClientSecret) > 0
	completeSP := len(material.AzureTenantID) > 0 && len(material.AzureClientID) > 0 && len(material.AzureClientSecret) > 0
	modes := 0
	if hasKey {
		modes++
	}
	if hasSAS {
		modes++
	}
	if hasSP {
		modes++
	}
	switch {
	case modes > 1:
		return fmt.Errorf("%w: azure credentials must be exactly one of account key, SAS, or service principal", ErrIncompleteSource)
	case hasKey:
		// ok
	case hasSAS:
		// ok for Store and restic
	case completeSP:
		// ok for gobackup Store; restic checked below
	case hasSP:
		return fmt.Errorf("%w: incomplete azure service principal fields", ErrIncompleteSource)
	default:
		return fmt.Errorf("%w: missing azure credentials", ErrIncompleteSource)
	}

	switch backupProvider {
	case "gobackup":
		if !validGoBackupPrefix(material.AzurePathPrefix) {
			return fmt.Errorf("%w: missing or invalid AZURE_PATH_PREFIX", ErrIncompleteSource)
		}
	case "restic":
		if !hasKey && !hasSAS {
			return fmt.Errorf("%w: azure restic sources require AZURE_ACCOUNT_KEY or AZURE_ACCOUNT_SAS", ErrIncompleteSource)
		}
		if material.ResticRepository == "" || len(material.ResticPassword) == 0 {
			return fmt.Errorf("%w: missing RESTIC_REPOSITORY or RESTIC_PASSWORD", ErrIncompleteSource)
		}
		if !strings.HasPrefix(material.ResticRepository, "azure:") {
			return fmt.Errorf("%w: RESTIC_REPOSITORY must use azure: scheme", ErrIncompleteSource)
		}
	}
	return nil
}

func hasAnyS3Fields(material *SourceMaterial) bool {
	return material.Endpoint != "" || material.Region != "" || material.Bucket != "" ||
		material.PathPrefix != "" || len(material.AccessKeyID) > 0 || len(material.SecretAccessKey) > 0
}

func hasAnyAzureFields(material *SourceMaterial) bool {
	return material.AzureAccountName != "" || material.AzureContainer != "" ||
		material.AzurePathPrefix != "" || material.AzureEndpointSuffix != "" ||
		len(material.AzureAccountKey) > 0 || len(material.AzureAccountSAS) > 0 ||
		len(material.AzureTenantID) > 0 || len(material.AzureClientID) > 0 ||
		len(material.AzureClientSecret) > 0
}

func resolvePathMap(ctx context.Context, provider SecretProvider, path string) (map[string]string, error) {
	keys := []string{
		FieldStorageType,
		FieldS3AccessKeyID, FieldS3SecretAccessKey, FieldS3Endpoint, FieldS3Region,
		FieldS3Bucket, FieldS3PathPrefix,
		FieldAzureAccountName, FieldAzureContainer, FieldAzurePathPrefix, FieldAzureEndpointSuffix,
		FieldAzureAccountKey, FieldAzureAccountSAS, FieldAzureTenantID, FieldAzureClientID, FieldAzureClientSecret,
		FieldResticRepository, FieldResticPassword,
	}
	out := make(map[string]string, len(keys))
	var sawAny bool
	for _, key := range keys {
		ref := Reference{Provider: "openbao", Mount: DefaultMount, Path: path, Key: key}
		value, err := provider.Resolve(ctx, ref)
		if err != nil {
			continue
		}
		sawAny = true
		out[key] = string(value)
		clearBytes(value)
	}
	if path != DefaultsPath && !sawAny {
		return nil, ErrIncompleteSource
	}
	return out, nil
}

func mergeMaps(defaults, source map[string]string) map[string]string {
	out := make(map[string]string, len(defaults)+len(source))
	for k, v := range defaults {
		if strings.TrimSpace(v) != "" {
			out[k] = v
		}
	}
	for k, v := range source {
		if strings.TrimSpace(v) != "" {
			out[k] = v
		}
	}
	return out
}

func validGoBackupPrefix(prefix string) bool {
	if prefix == "" || !strings.HasSuffix(prefix, "/") || strings.HasPrefix(prefix, "/") ||
		strings.Contains(prefix, `\`) || len(prefix) > 1024 {
		return false
	}
	trimmed := strings.TrimSuffix(prefix, "/")
	if trimmed == "" {
		return false
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, r := range segment {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}

func validS3Endpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return false
	}
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		if strings.EqualFold(host, "localhost") {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}
	return true
}

func validAzureAccountName(value string) bool {
	if len(value) < 3 || len(value) > 24 {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func validAzureContainer(value string) bool {
	if len(value) < 3 || len(value) > 63 || strings.Contains(value, "..") {
		return false
	}
	if value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validAzureEndpointSuffix(value string) bool {
	if value == "" || len(value) > 253 || strings.Contains(value, "..") || strings.ContainsAny(value, "/:\\ ") {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
