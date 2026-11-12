// Package catalog converts validated Reksicherung configuration into a
// secret-free inventory suitable for passing to an orchestrator or operator
// as a JSON input value.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/1k-off/reksicherung/internal/reksicherung/config"
)

const (
	APIVersion = "reksicherung.1node.xyz/v1alpha2"
	Kind       = "BackupInventory"
)

// Inventory is the stable, serialized view of backup targets exposed to
// orchestration. It deliberately excludes secret references and endpoints.
type Inventory struct {
	APIVersion string  `json:"apiVersion"`
	Kind       string  `json:"kind"`
	Backups    []Entry `json:"backups"`
}

type Entry struct {
	ID                  string    `json:"id"`
	SourceLocation      string    `json:"sourceLocation"`
	Provider            string    `json:"provider"`
	Database            Database  `json:"database"`
	Selection           Selection `json:"selection"`
	Checks              []string  `json:"checks"`
	ExpectedEmptyTables []string  `json:"expectedEmptyTables,omitempty"`
}

type Database struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type Selection struct {
	Policy string `json:"policy"`
	ID     string `json:"id,omitempty"`
}

// FromConfig validates and converts a catalog into deterministic inventory
// order. IDs are sorted because source YAML order does not affect identity.
func FromConfig(source config.Catalog, sourceLocation string) (Inventory, error) {
	if err := source.Validate(); err != nil {
		return Inventory{}, fmt.Errorf("build inventory: %w", err)
	}
	if !validSourceLocation(sourceLocation) {
		return Inventory{}, errors.New("build inventory: source location must be a safe relative path")
	}

	entries := make([]Entry, 0, len(source.Backups))
	for _, backup := range source.Backups {
		checks := []string{"restore", "sqlserver-integrity"}
		if backup.Validation.SemanticReview {
			checks = append(checks, "semantic-review")
		}
		entries = append(entries, Entry{
			ID:             backup.ID,
			SourceLocation: sourceLocation + "#backups/" + backup.ID,
			Provider:       backup.Provider,
			Database: Database{
				Type: backup.Database.Type,
				Name: backup.Database.Name,
			},
			Selection: Selection{
				Policy: backup.Selection.Policy,
				ID:     backup.Selection.ID,
			},
			Checks:              checks,
			ExpectedEmptyTables: append([]string(nil), backup.Validation.ExpectedEmptyTables...),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return Inventory{
		APIVersion: APIVersion,
		Kind:       Kind,
		Backups:    entries,
	}, nil
}

func validSourceLocation(location string) bool {
	if location == "" || len(location) > 512 || strings.HasPrefix(location, "/") ||
		strings.ContainsAny(location, "\\#?") {
		return false
	}
	for _, segment := range strings.Split(location, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, r := range segment {
			if unicode.IsControl(r) {
				return false
			}
			alphaNum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
			if !alphaNum && r != '.' && r != '_' && r != '-' {
				return false
			}
		}
	}
	return true
}

// Marshal returns compact deterministic JSON suitable as an orchestrator string
// input variable. The JSON contains no endpoint or secret-reference fields.
func (inventory Inventory) Marshal() ([]byte, error) {
	if inventory.APIVersion != APIVersion || inventory.Kind != Kind || len(inventory.Backups) == 0 {
		return nil, errors.New("marshal inventory: invalid version, kind, or empty backups")
	}
	return json.Marshal(inventory)
}
