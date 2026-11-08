// Package config defines and validates the versioned Reksicherung catalog.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/1k-off/reksicherung/internal/reksicherung/plan"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
	"gopkg.in/yaml.v3"
)

const (
	APIVersion = "reksicherung.io/v1alpha1"
	Kind       = "BackupCatalog"
)

// Catalog is the human-maintained source of backup targets that Reksicherung
// may inspect and restore.
type Catalog struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Backups    []BackupSource `yaml:"backups"`
}

// BackupSource describes one independently restorable SQL Server backup.
type BackupSource struct {
	ID         string            `yaml:"id"`
	Provider   string            `yaml:"provider"`
	BackupName string            `yaml:"backupName"`
	Database   Database          `yaml:"database"`
	Storage    S3Storage         `yaml:"storage"`
	Selection  Selection         `yaml:"selection"`
	Restore    Restore           `yaml:"restore"`
	Validation ValidationProfile `yaml:"validation,omitempty"`
	Cleanup    string            `yaml:"cleanup"`
}

type Database struct {
	Type string `yaml:"type"`
	Name string `yaml:"name"`
}

// S3Storage contains S3-compatible repository coordinates and references to
// credentials. It intentionally has no fields for literal secret values.
type S3Storage struct {
	Type        string        `yaml:"type"`
	Endpoint    string        `yaml:"endpoint,omitempty"`
	Bucket      string        `yaml:"bucket"`
	Region      string        `yaml:"region"`
	Prefix      string        `yaml:"prefix,omitempty"`
	Credentials S3Credentials `yaml:"credentials"`
}

type S3Credentials struct {
	AccessKeyRef string `yaml:"accessKeyRef"`
	SecretKeyRef string `yaml:"secretKeyRef"`
}

type Selection struct {
	Policy string `yaml:"policy"`
	ID     string `yaml:"id,omitempty"`
}

type Restore struct {
	Image     string `yaml:"image"`
	CPUMilli  int    `yaml:"cpuMilli"`
	MemoryMiB int    `yaml:"memoryMiB"`
	Timeout   string `yaml:"timeout"`
}

// ValidationProfile enables optional AI-assisted semantic review and can
// document tables that are known to be legitimately empty.
type ValidationProfile struct {
	SemanticReview      bool     `yaml:"semanticReview,omitempty"`
	ExpectedEmptyTables []string `yaml:"expectedEmptyTables,omitempty"`
}

// Decode parses exactly one YAML document, rejects unknown fields, and
// validates all references before returning the catalog.
func Decode(data []byte) (Catalog, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, errors.New("decode catalog: invalid YAML document or unsupported field")
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Catalog{}, errors.New("decode catalog: multiple YAML documents are not allowed")
		}
		return Catalog{}, errors.New("decode catalog: invalid trailing YAML document")
	}

	if err := catalog.Validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

// Validate checks version, uniqueness, supported backends, and bounded
// restore settings without resolving any secret references.
func (catalog Catalog) Validate() error {
	if catalog.APIVersion != APIVersion {
		return fmt.Errorf("catalog apiVersion must be %q", APIVersion)
	}
	if catalog.Kind != Kind {
		return fmt.Errorf("catalog kind must be %q", Kind)
	}
	if len(catalog.Backups) == 0 {
		return errors.New("catalog must define at least one backup")
	}
	if len(catalog.Backups) > plan.MaxSelectedDatabases {
		return fmt.Errorf("catalog exceeds the %d backup source limit", plan.MaxSelectedDatabases)
	}

	ids := make(map[string]struct{}, len(catalog.Backups))
	for i, backup := range catalog.Backups {
		if err := backup.validate(); err != nil {
			return fmt.Errorf("backups[%d]: %w", i, err)
		}
		if _, exists := ids[backup.ID]; exists {
			return fmt.Errorf("backups[%d]: duplicate backup id", i)
		}
		ids[backup.ID] = struct{}{}
	}
	return nil
}

// ValidateProduction applies deployment-specific checks stricter than local
// development validation, including immutable restore image digests.
func (catalog Catalog) ValidateProduction() error {
	if err := catalog.Validate(); err != nil {
		return err
	}
	for i, backup := range catalog.Backups {
		if !isDigestPinned(backup.Restore.Image) {
			return fmt.Errorf("backups[%d]: production restore image must be pinned by sha256 digest", i)
		}
	}
	return nil
}

func (backup BackupSource) validate() error {
	if !validIdentifier(backup.ID) {
		return errors.New("id must match [a-zA-Z0-9][a-zA-Z0-9._-]{0,127}")
	}
	if backup.Provider != "gobackup" && backup.Provider != "restic" {
		return errors.New("provider must be gobackup or restic")
	}
	if !validName(backup.BackupName) {
		return errors.New("backupName must be a non-empty printable name of at most 256 characters")
	}
	if backup.Database.Type != "sqlserver" {
		return errors.New("database.type must be sqlserver")
	}
	if len(backup.Database.Name) > 128 || !validName(backup.Database.Name) {
		return errors.New("database.name must be a non-empty printable name of at most 128 characters")
	}
	if err := backup.Storage.validate(); err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	switch backup.Selection.Policy {
	case "latest":
		if backup.Selection.ID != "" {
			return errors.New("selection.id must be omitted when policy is latest")
		}
	case "id":
		if !validName(backup.Selection.ID) {
			return errors.New("selection.id is required for policy id and must be a printable name of at most 256 characters")
		}
	default:
		return errors.New("selection.policy must be latest or id")
	}
	if err := backup.Restore.validate(); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if err := backup.Validation.validate(); err != nil {
		return fmt.Errorf("validation: %w", err)
	}
	if backup.Cleanup != "delete" && backup.Cleanup != "hold_for_review" {
		return errors.New("cleanup must be delete or hold_for_review")
	}
	return nil
}

func (profile ValidationProfile) validate() error {
	seen := make(map[string]struct{}, len(profile.ExpectedEmptyTables))
	for _, table := range profile.ExpectedEmptyTables {
		if !validTableReference(table) {
			return errors.New("expectedEmptyTables entries must be schema.table names")
		}
		key := strings.ToLower(table)
		if _, exists := seen[key]; exists {
			return errors.New("expectedEmptyTables entries must be unique")
		}
		seen[key] = struct{}{}
	}
	if len(profile.ExpectedEmptyTables) > 0 && !profile.SemanticReview {
		return errors.New("expectedEmptyTables requires semanticReview to be enabled")
	}
	return nil
}

func (storage S3Storage) validate() error {
	if storage.Type != "s3" {
		return errors.New("type must be s3")
	}
	if !validBucket(storage.Bucket) {
		return errors.New("bucket must be a valid S3 bucket name")
	}
	if !validName(storage.Region) {
		return errors.New("region must be a non-empty printable name")
	}
	if storage.Prefix != "" {
		for _, segment := range strings.Split(storage.Prefix, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return errors.New("prefix must not contain empty, dot, or parent path segments")
			}
		}
		if strings.HasPrefix(storage.Prefix, "/") || strings.Contains(storage.Prefix, "\\") {
			return errors.New("prefix must be a relative object key prefix")
		}
	}
	if storage.Endpoint != "" {
		endpoint, err := url.Parse(storage.Endpoint)
		if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" ||
			endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") ||
			(endpoint.Scheme != "https" && endpoint.Scheme != "http") {
			return errors.New("endpoint must be an http(s) URL without credentials, query, or fragment")
		}
		if endpoint.Scheme == "http" && !isLoopbackHost(endpoint.Hostname()) {
			return errors.New("non-loopback S3 endpoints must use https")
		}
	}
	if _, err := secrets.ParseReference(storage.Credentials.AccessKeyRef); err != nil {
		return errors.New("credentials.accessKeyRef must be a valid secret reference")
	}
	if _, err := secrets.ParseReference(storage.Credentials.SecretKeyRef); err != nil {
		return errors.New("credentials.secretKeyRef must be a valid secret reference")
	}
	return nil
}

func (restore Restore) validate() error {
	if strings.TrimSpace(restore.Image) == "" || strings.ContainsAny(restore.Image, "\r\n\t ") {
		return errors.New("image must be a non-empty image reference without whitespace")
	}
	if strings.HasPrefix(restore.Image, "-") {
		return errors.New("image must not look like a runtime command-line option")
	}
	for _, r := range restore.Image {
		if unicode.IsControl(r) {
			return errors.New("image must be a printable image reference")
		}
	}
	if restore.CPUMilli < 250 || restore.CPUMilli > 16000 {
		return errors.New("cpuMilli must be between 250 and 16000")
	}
	if restore.MemoryMiB < 1024 || restore.MemoryMiB > 65536 {
		return errors.New("memoryMiB must be between 1024 and 65536")
	}
	timeout, err := time.ParseDuration(restore.Timeout)
	if err != nil || timeout < time.Minute || timeout > 24*time.Hour {
		return errors.New("timeout must be between 1m and 24h")
	}
	return nil
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i, r := range value {
		alphaNum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if (i == 0 && !alphaNum) || (i > 0 && !alphaNum && r != '.' && r != '_' && r != '-') {
			return false
		}
	}
	return true
}

func validName(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validBucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || value[0] == '.' || value[0] == '-' ||
		value[len(value)-1] == '.' || value[len(value)-1] == '-' || strings.Contains(value, "..") ||
		net.ParseIP(value) != nil {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validTableReference(table string) bool {
	if len(table) > 257 || strings.TrimSpace(table) != table {
		return false
	}
	parts := strings.Split(table, ".")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 128 || strings.TrimSpace(part) != part {
			return false
		}
		for _, r := range part {
			if unicode.IsControl(r) || r == '.' {
				return false
			}
		}
	}
	return true
}

func isDigestPinned(image string) bool {
	_, digest, found := strings.Cut(image, "@sha256:")
	if !found || len(digest) != 64 {
		return false
	}
	for _, r := range digest {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
