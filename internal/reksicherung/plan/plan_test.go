package plan

import (
	"reflect"
	"strings"
	"testing"
)

func TestRunPlanNormalizeValidPolicies(t *testing.T) {
	tests := []struct {
		name string
		plan RunPlan
		want []string
	}{
		{
			name: "explicit targets run sequentially and continue after failures",
			plan: RunPlan{
				Selection:      Selection{DatabaseIDs: []string{"db-b", "db-a"}},
				Ordering:       Sequential,
				MaxParallelism: 1,
				ErrorHandling:  Continue,
			},
			want: []string{"db-b", "db-a"},
		},
		{
			name: "any order may use bounded parallelism and fail fast",
			plan: RunPlan{
				Selection:      Selection{DatabaseIDs: []string{"db-a", "db-b"}},
				Ordering:       Any,
				MaxParallelism: 2,
				ErrorHandling:  FailFast,
			},
			want: []string{"db-a", "db-b"},
		},
		{
			name: "any order may still run one at a time",
			plan: RunPlan{
				Selection:      Selection{DatabaseIDs: []string{"db-a"}},
				Ordering:       Any,
				MaxParallelism: 1,
				ErrorHandling:  Continue,
			},
			want: []string{"db-a"},
		},
		{
			name: "parallelism is a ceiling and may exceed selected count",
			plan: RunPlan{
				Selection:      Selection{DatabaseIDs: []string{"db-a"}},
				Ordering:       Any,
				MaxParallelism: 4,
				ErrorHandling:  Continue,
			},
			want: []string{"db-a"},
		},
	}
	inventory := []string{"db-a", "db-b"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.plan.Normalize(inventory)
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if !reflect.DeepEqual(got.Selection.DatabaseIDs, tt.want) {
				t.Errorf("normalized database IDs = %v, want %v", got.Selection.DatabaseIDs, tt.want)
			}
			if got.Selection.All {
				t.Errorf("normalized selection All = true, want concrete selection")
			}
			if got.Ordering != tt.plan.Ordering || got.ErrorHandling != tt.plan.ErrorHandling {
				t.Errorf("normalized ordering/error handling = %q/%q, want %q/%q",
					got.Ordering, got.ErrorHandling, tt.plan.Ordering, tt.plan.ErrorHandling)
			}
		})
	}
}

func TestRunPlanNormalizeSelection(t *testing.T) {
	tests := []struct {
		name      string
		selection Selection
		inventory []string
		want      []string
		wantError string
	}{
		{
			name:      "all targets expand in stable sorted order",
			selection: Selection{All: true},
			inventory: []string{"db-z", "db-a", "db-middle"},
			want:      []string{"db-a", "db-middle", "db-z"},
		},
		{
			name:      "inventory input order does not affect all-target expansion",
			selection: Selection{All: true},
			inventory: []string{"db-middle", "db-z", "db-a"},
			want:      []string{"db-a", "db-middle", "db-z"},
		},
		{
			name:      "explicit selection preserves requested sequential order",
			selection: Selection{DatabaseIDs: []string{"db-z", "db-a"}},
			inventory: []string{"db-a", "db-z"},
			want:      []string{"db-z", "db-a"},
		},
		{
			name:      "empty inventory cannot satisfy all selection",
			selection: Selection{All: true},
			wantError: "no inventory targets",
		},
		{
			name:      "empty explicit selection is rejected",
			selection: Selection{},
			inventory: []string{"db-a"},
			wantError: "must not be empty",
		},
		{
			name:      "all and explicit IDs are mutually exclusive",
			selection: Selection{All: true, DatabaseIDs: []string{"db-a"}},
			inventory: []string{"db-a"},
			wantError: "cannot also specify",
		},
		{
			name:      "unknown database ID is rejected",
			selection: Selection{DatabaseIDs: []string{"db-missing"}},
			inventory: []string{"db-a"},
			wantError: "not in the inventory",
		},
		{
			name:      "duplicate selected IDs are rejected",
			selection: Selection{DatabaseIDs: []string{"db-a", "db-a"}},
			inventory: []string{"db-a"},
			wantError: "duplicate database ID",
		},
		{
			name:      "whitespace in selected ID is rejected",
			selection: Selection{DatabaseIDs: []string{"db a"}},
			inventory: []string{"db a"},
			wantError: "invalid",
		},
		{
			name:      "path separator in selected ID is rejected",
			selection: Selection{DatabaseIDs: []string{"db/a"}},
			inventory: []string{"db/a"},
			wantError: "invalid",
		},
		{
			name:      "non-ASCII selected ID is rejected",
			selection: Selection{DatabaseIDs: []string{"данные"}},
			inventory: []string{"данные"},
			wantError: "invalid",
		},
		{
			name:      "selected ID over maximum length is rejected",
			selection: Selection{DatabaseIDs: []string{strings.Repeat("a", 129)}},
			inventory: []string{strings.Repeat("a", 129)},
			wantError: "invalid",
		},
		{
			name:      "duplicate inventory IDs are rejected",
			selection: Selection{All: true},
			inventory: []string{"db-a", "db-a"},
			wantError: "inventory contains duplicate",
		},
		{
			name:      "invalid inventory ID is rejected",
			selection: Selection{All: true},
			inventory: []string{" db-a"},
			wantError: "inventory database ID",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPlan(tt.selection)
			got, err := p.Normalize(tt.inventory)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("Normalize() error = %v, want error containing %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if !reflect.DeepEqual(got.Selection.DatabaseIDs, tt.want) {
				t.Errorf("normalized database IDs = %v, want %v", got.Selection.DatabaseIDs, tt.want)
			}
		})
	}
}

func TestRunPlanEnforcesRunAndConcurrencyLimits(t *testing.T) {
	inventory := make([]string, MaxSelectedDatabases+1)
	for i := range inventory {
		inventory[i] = "database-" + strings.Repeat("x", i+1)
	}
	allTargets := validPlan(Selection{All: true})
	if _, err := allTargets.Normalize(inventory); err == nil || !strings.Contains(err.Error(), "selected databases exceed limit") {
		t.Fatalf("Normalize(all targets) error = %v, want selected database limit error", err)
	}

	excessiveConcurrency := validPlan(Selection{DatabaseIDs: []string{"db-a"}})
	excessiveConcurrency.Ordering = Any
	excessiveConcurrency.MaxParallelism = MaxParallelism + 1
	if _, err := excessiveConcurrency.Normalize([]string{"db-a"}); err == nil || !strings.Contains(err.Error(), "max parallelism exceeds limit") {
		t.Fatalf("Normalize(excessive concurrency) error = %v, want parallelism limit error", err)
	}
}

func TestRunPlanNormalizeRejectsInvalidPolicies(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*RunPlan)
		inventory []string
		wantError string
	}{
		{
			name:      "invalid ordering",
			mutate:    func(p *RunPlan) { p.Ordering = "parallel" },
			inventory: []string{"db-a"},
			wantError: "invalid ordering",
		},
		{
			name:      "invalid error handling",
			mutate:    func(p *RunPlan) { p.ErrorHandling = "ignore" },
			inventory: []string{"db-a"},
			wantError: "invalid error handling",
		},
		{
			name:      "zero concurrency",
			mutate:    func(p *RunPlan) { p.MaxParallelism = 0 },
			inventory: []string{"db-a"},
			wantError: "must be positive",
		},
		{
			name:      "negative concurrency",
			mutate:    func(p *RunPlan) { p.MaxParallelism = -1 },
			inventory: []string{"db-a"},
			wantError: "must be positive",
		},
		{
			name:      "sequential ordering cannot have parallel workers",
			mutate:    func(p *RunPlan) { p.MaxParallelism = 2 },
			inventory: []string{"db-a", "db-b"},
			wantError: "sequential ordering requires",
		},
		{
			name: "failure behavior is independent of target selection",
			mutate: func(p *RunPlan) {
				p.ErrorHandling = FailFast
				p.Ordering = Any
				p.MaxParallelism = 1
			},
			inventory: []string{"db-a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPlan(Selection{DatabaseIDs: []string{"db-a"}})
			tt.mutate(&p)
			_, err := p.Normalize(tt.inventory)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Normalize() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Normalize() error = %v, want error containing %q", err, tt.wantError)
			}
		})
	}
}

func TestRunPlanNormalizeReturnsIndependentSelection(t *testing.T) {
	p := validPlan(Selection{DatabaseIDs: []string{"db-a"}})
	got, err := p.Normalize([]string{"db-a"})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	got.Selection.DatabaseIDs[0] = "changed"
	if p.Selection.DatabaseIDs[0] != "db-a" {
		t.Errorf("normalizing aliased caller selection: input ID = %q, want %q", p.Selection.DatabaseIDs[0], "db-a")
	}
}

func validPlan(selection Selection) RunPlan {
	return RunPlan{
		Selection:      selection,
		Ordering:       Sequential,
		MaxParallelism: 1,
		ErrorHandling:  Continue,
	}
}
