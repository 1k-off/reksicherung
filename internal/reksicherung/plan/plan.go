// Package plan models and validates the database scope and execution policy for
// a Reksicherung run.
//
// Plans are deliberately independent of Airon and configuration formats. An
// orchestration adapter can translate project instructions into a RunPlan,
// normalize it against the current inventory, and persist the returned plan
// before starting any restore.
package plan

import (
	"fmt"
	"sort"
	"strings"
)

const (
	// MaxSelectedDatabases bounds the size of one restore-check run and its
	// structured result.
	MaxSelectedDatabases = 64
	// MaxParallelism bounds per-run concurrency even for untrusted plans.
	MaxParallelism = 64
)

// Selection describes the databases a run will check. Set All to select every
// database in the inventory; otherwise DatabaseIDs must list one or more IDs.
// The two forms are mutually exclusive.
type Selection struct {
	All         bool     `json:"all"`
	DatabaseIDs []string `json:"database_ids,omitempty"`
}

// Ordering describes the execution ordering constraint for selected databases.
type Ordering string

const (
	// Sequential requires at most one database check to run at a time.
	Sequential Ordering = "sequential"
	// Any allows checks to run in any order, subject to MaxParallelism.
	Any Ordering = "any"
)

// ErrorHandling controls what execution does when a database check fails.
type ErrorHandling string

const (
	// Continue allows execution to proceed to the remaining selected databases.
	Continue ErrorHandling = "continue"
	// FailFast stops starting further database checks after the first failure.
	FailFast ErrorHandling = "fail_fast"
)

// RunPlan is an explicit, project-specific policy for checking database
// backups. Normalize validates its policy and resolves Selection against an
// inventory, returning concrete database IDs in deterministic order.
//
// MaxParallelism is a positive upper bound even when Ordering is Any; it is
// separate from the order constraint. Sequential plans must use a value of 1.
type RunPlan struct {
	Selection      Selection     `json:"selection"`
	Ordering       Ordering      `json:"ordering"`
	MaxParallelism int           `json:"max_parallelism"`
	ErrorHandling  ErrorHandling `json:"error_handling"`
}

// Normalize validates p against inventory and returns a canonical copy. It
// rejects malformed or duplicate inventory IDs, unknown or duplicate selected
// IDs, invalid policy values, and impossible ordering/concurrency combinations.
// All-target selections are expanded to IDs sorted lexicographically. Explicit
// selections retain their supplied order so sequential plans preserve the
// requested execution sequence.
func (p RunPlan) Normalize(inventory []string) (RunPlan, error) {
	inventorySet := make(map[string]struct{}, len(inventory))
	for i, id := range inventory {
		if !validDatabaseID(id) {
			return RunPlan{}, fmt.Errorf("inventory database ID at index %d is invalid: %q", i, id)
		}
		if _, exists := inventorySet[id]; exists {
			return RunPlan{}, fmt.Errorf("inventory contains duplicate database ID %q", id)
		}
		inventorySet[id] = struct{}{}
	}

	if p.Ordering != Sequential && p.Ordering != Any {
		return RunPlan{}, fmt.Errorf("invalid ordering %q", p.Ordering)
	}
	if p.ErrorHandling != Continue && p.ErrorHandling != FailFast {
		return RunPlan{}, fmt.Errorf("invalid error handling %q", p.ErrorHandling)
	}
	if p.MaxParallelism < 1 {
		return RunPlan{}, fmt.Errorf("max parallelism must be positive, got %d", p.MaxParallelism)
	}
	if p.MaxParallelism > MaxParallelism {
		return RunPlan{}, fmt.Errorf("max parallelism exceeds limit %d", MaxParallelism)
	}
	if p.Ordering == Sequential && p.MaxParallelism != 1 {
		return RunPlan{}, fmt.Errorf("sequential ordering requires max parallelism 1, got %d", p.MaxParallelism)
	}

	if p.Selection.All && len(p.Selection.DatabaseIDs) > 0 {
		return RunPlan{}, fmt.Errorf("all-target selection cannot also specify database IDs")
	}

	selected := make([]string, 0, len(p.Selection.DatabaseIDs))
	if p.Selection.All {
		selected = make([]string, 0, len(inventorySet))
		for id := range inventorySet {
			selected = append(selected, id)
		}
		sort.Strings(selected)
	} else {
		if len(p.Selection.DatabaseIDs) == 0 {
			return RunPlan{}, fmt.Errorf("database selection must not be empty unless all targets are selected")
		}
		seen := make(map[string]struct{}, len(p.Selection.DatabaseIDs))
		for i, id := range p.Selection.DatabaseIDs {
			if !validDatabaseID(id) {
				return RunPlan{}, fmt.Errorf("selected database ID at index %d is invalid: %q", i, id)
			}
			if _, exists := seen[id]; exists {
				return RunPlan{}, fmt.Errorf("selection contains duplicate database ID %q", id)
			}
			seen[id] = struct{}{}
			if _, exists := inventorySet[id]; !exists {
				return RunPlan{}, fmt.Errorf("selected database ID %q is not in the inventory", id)
			}
			selected = append(selected, id)
		}
	}

	if len(selected) == 0 {
		return RunPlan{}, fmt.Errorf("database selection resolves to no inventory targets")
	}
	if len(selected) > MaxSelectedDatabases {
		return RunPlan{}, fmt.Errorf("selected databases exceed limit %d", MaxSelectedDatabases)
	}
	normalized := p
	normalized.Selection = Selection{DatabaseIDs: selected}
	return normalized, nil
}

// validDatabaseID accepts stable ASCII identifiers, not display names.
func validDatabaseID(id string) bool {
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
		return false
	}
	for i, r := range id {
		isLetterOrDigit := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if i == 0 && !isLetterOrDigit {
			return false
		}
		if !isLetterOrDigit && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}
