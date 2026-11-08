// Package catalog converts validated Reksicherung configuration into a
// secret-free inventory suitable for passing to Airon as a JSON input value.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/1k-off/reksicherung/internal/reksicherung/config"
)

const (
	APIVersion = "reksicherung.io/v1alpha1"
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
	ID         string    `json:"id"`
	Provider   string    `json:"provider"`
	BackupName string    `json:"backupName"`
	Database   Database  `json:"database"`
	Storage    Storage   `json:"storage"`
	Selection  Selection `json:"selection"`
	Checks     []string  `json:"checks"`
}

type Database struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type Storage struct {
	Type   string `json:"type"`
	Bucket string `json:"bucket"`
	Region string `json:"region"`
}

type Selection struct {
	Policy string `json:"policy"`
	ID     string `json:"id,omitempty"`
}

// FromConfig validates and converts a catalog into deterministic inventory
// order. IDs are sorted because source YAML order does not affect identity.
func FromConfig(source config.Catalog) (Inventory, error) {
	if err := source.Validate(); err != nil {
		return Inventory{}, fmt.Errorf("build inventory: %w", err)
	}

	entries := make([]Entry, 0, len(source.Backups))
	for _, backup := range source.Backups {
		entries = append(entries, Entry{
			ID:         backup.ID,
			Provider:   backup.Provider,
			BackupName: backup.BackupName,
			Database: Database{
				Type: backup.Database.Type,
				Name: backup.Database.Name,
			},
			Storage: Storage{
				Type:   backup.Storage.Type,
				Bucket: backup.Storage.Bucket,
				Region: backup.Storage.Region,
			},
			Selection: Selection{
				Policy: backup.Selection.Policy,
				ID:     backup.Selection.ID,
			},
			Checks: []string{"restore", "sqlserver-integrity", "semantic-review"},
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return Inventory{
		APIVersion: APIVersion,
		Kind:       Kind,
		Backups:    entries,
	}, nil
}

// Marshal returns compact deterministic JSON suitable for an Airon string
// input variable. The JSON contains no endpoint or secret-reference fields.
func (inventory Inventory) Marshal() ([]byte, error) {
	if inventory.APIVersion != APIVersion || inventory.Kind != Kind || len(inventory.Backups) == 0 {
		return nil, errors.New("marshal inventory: invalid version, kind, or empty backups")
	}
	return json.Marshal(inventory)
}
