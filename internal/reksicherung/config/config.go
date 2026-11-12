// Package config defines and validates the versioned Reksicherung catalog.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"

	"github.com/1k-off/reksicherung/internal/reksicherung/plan"
	"github.com/1k-off/reksicherung/internal/reksicherung/secrets"
	"gopkg.in/yaml.v3"
)

const (
	APIVersion = "reksicherung.1node.xyz/v1alpha2"
	Kind       = "BackupCatalog"
)

// Catalog is the human-maintained list of backup sources. Storage credentials
// and coordinates are resolved from OpenBao by source id; they are not catalog
// fields.
type Catalog struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Backups    []BackupSource `yaml:"backups"`
}

// BackupSource describes one independently restorable SQL Server backup source.
type BackupSource struct {
	ID         string            `yaml:"id"`
	Provider   string            `yaml:"provider"`
	BackupPath string            `yaml:"backupPath,omitempty"`
	Database   Database          `yaml:"database"`
	Selection  Selection         `yaml:"selection"`
	Validation ValidationProfile `yaml:"validation,omitempty"`
	Cleanup    string            `yaml:"cleanup"`
}

type Database struct {
	Type string `yaml:"type"`
	Name string `yaml:"name"`
}

type Selection struct {
	Policy string `yaml:"policy"`
	ID     string `yaml:"id,omitempty"`
}

// ValidationProfile enables optional AI-assisted semantic review and can
// document tables that are known to be legitimately empty.
type ValidationProfile struct {
	SemanticReview      bool     `yaml:"semanticReview,omitempty"`
	ExpectedEmptyTables []string `yaml:"expectedEmptyTables,omitempty"`
}

// Decode parses exactly one YAML document, rejects unknown fields, and
// validates the catalog.
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

// Validate checks version, uniqueness, and supported source fields without
// resolving OpenBao material.
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

// ValidateProduction keeps the production entry point. Storage HTTPS is
// enforced when OpenBao material is resolved, not from catalog fields.
func (catalog Catalog) ValidateProduction() error {
	return catalog.Validate()
}

func (backup BackupSource) validate() error {
	if !secrets.ValidSourceID(backup.ID) {
		return errors.New("id must match [a-z0-9][a-z0-9-]{0,126}")
	}
	if backup.Provider != "gobackup" && backup.Provider != "restic" {
		return errors.New("provider must be gobackup or restic")
	}
	if backup.Provider == "restic" {
		if err := validateResticBackupPath(backup.BackupPath); err != nil {
			return err
		}
	} else if backup.BackupPath != "" {
		return errors.New("backupPath is only valid for Restic")
	}
	if backup.Database.Type != "sqlserver" {
		return errors.New("database.type must be sqlserver")
	}
	if len(backup.Database.Name) > 128 || !validName(backup.Database.Name) {
		return errors.New("database.name must be a non-empty printable name of at most 128 characters")
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
	if err := backup.Validation.validate(); err != nil {
		return fmt.Errorf("validation: %w", err)
	}
	if backup.Cleanup != "delete" && backup.Cleanup != "hold_for_review" {
		return errors.New("cleanup must be delete or hold_for_review")
	}
	return nil
}

func validateResticBackupPath(value string) error {
	if value == "" || len(value) > 4096 || !strings.HasPrefix(value, "/") ||
		strings.TrimSpace(value) != value || path.Clean(value) != value ||
		strings.ContainsAny(value, "\\*?[]{}\x00\r\n\t") {
		return errors.New("backupPath must be an exact absolute file path without traversal or glob characters")
	}
	for _, component := range strings.Split(value, "/") {
		if component == "." || component == ".." {
			return errors.New("backupPath must not contain dot or parent path segments")
		}
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("backupPath must be printable")
		}
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
