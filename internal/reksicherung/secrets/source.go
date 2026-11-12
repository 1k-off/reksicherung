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
	// DefaultsPath holds optional project-wide S3 endpoint/region/bucket.
	DefaultsPath = "reksicherung/defaults"
	// SourcesPathPrefix is followed by the catalog source id.
	SourcesPathPrefix = "reksicherung/sources/"
)

// Well-known KV field names for per-source material.
const (
	FieldS3AccessKeyID     = "S3_ACCESS_KEY_ID"
	FieldS3SecretAccessKey = "S3_SECRET_ACCESS_KEY"
	FieldS3Endpoint        = "S3_ENDPOINT"
	FieldS3Region          = "S3_REGION"
	FieldS3Bucket          = "S3_BUCKET"
	FieldS3PathPrefix      = "S3_PATH_PREFIX"
	FieldResticRepository  = "RESTIC_REPOSITORY"
	FieldResticPassword    = "RESTIC_PASSWORD"
)

// ErrInvalidSourceID indicates the catalog/OpenBao source id is unsafe.
var ErrInvalidSourceID = errors.New("secrets: invalid source id")

// ErrIncompleteSource indicates required OpenBao fields are missing after merge.
var ErrIncompleteSource = errors.New("secrets: incomplete source material")

// SourceMaterial is the resolved, provider-ready configuration for one catalog
// source. Callers must Clear sensitive fields when finished.
type SourceMaterial struct {
	SourceID         string
	Endpoint         string
	Region           string
	Bucket           string
	PathPrefix       string
	AccessKeyID      []byte
	SecretAccessKey  []byte
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
	clearBytes(m.ResticPassword)
	m.AccessKeyID = nil
	m.SecretAccessKey = nil
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
	material := SourceMaterial{
		SourceID:         sourceID,
		Endpoint:         strings.TrimSpace(merged[FieldS3Endpoint]),
		Region:           strings.TrimSpace(merged[FieldS3Region]),
		Bucket:           strings.TrimSpace(merged[FieldS3Bucket]),
		PathPrefix:       strings.TrimSpace(merged[FieldS3PathPrefix]),
		AccessKeyID:      []byte(merged[FieldS3AccessKeyID]),
		SecretAccessKey:  []byte(merged[FieldS3SecretAccessKey]),
		ResticRepository: strings.TrimSpace(merged[FieldResticRepository]),
		ResticPassword:   []byte(merged[FieldResticPassword]),
	}

	if len(material.AccessKeyID) == 0 || len(material.SecretAccessKey) == 0 {
		material.Clear()
		return SourceMaterial{}, fmt.Errorf("%w: missing S3 credentials", ErrIncompleteSource)
	}
	if material.Endpoint == "" || material.Region == "" || material.Bucket == "" {
		material.Clear()
		return SourceMaterial{}, fmt.Errorf("%w: missing S3 endpoint, region, or bucket", ErrIncompleteSource)
	}
	if !validS3Endpoint(material.Endpoint) {
		material.Clear()
		return SourceMaterial{}, fmt.Errorf("%w: invalid S3 endpoint", ErrIncompleteSource)
	}
	switch backupProvider {
	case "gobackup":
		if !validGoBackupPrefix(material.PathPrefix) {
			material.Clear()
			return SourceMaterial{}, fmt.Errorf("%w: missing or invalid S3_PATH_PREFIX", ErrIncompleteSource)
		}
	case "restic":
		if material.ResticRepository == "" || len(material.ResticPassword) == 0 {
			material.Clear()
			return SourceMaterial{}, fmt.Errorf("%w: missing RESTIC_REPOSITORY or RESTIC_PASSWORD", ErrIncompleteSource)
		}
	}
	return material, nil
}

func resolvePathMap(ctx context.Context, provider SecretProvider, path string) (map[string]string, error) {
	keys := []string{
		FieldS3AccessKeyID, FieldS3SecretAccessKey, FieldS3Endpoint, FieldS3Region,
		FieldS3Bucket, FieldS3PathPrefix, FieldResticRepository, FieldResticPassword,
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

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
