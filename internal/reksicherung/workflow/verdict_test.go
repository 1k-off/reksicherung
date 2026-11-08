package workflow

import (
	"reflect"
	"testing"
)

func TestEvaluateRequiredChecks(t *testing.T) {
	tests := []struct {
		name       string
		results    []CheckResult
		required   []string
		want       Verdict
		wantReason []string
	}{
		{
			name: "all required checks pass",
			results: []CheckResult{
				{ID: "restore", Status: CheckPassed, Required: true},
				{ID: "integrity", Status: CheckPassed, Required: true},
				{ID: "semantic", Status: CheckPassed, Required: true},
			},
			required: []string{"restore", "integrity", "semantic"},
			want:     VerdictPass,
		},
		{
			name: "empty table warning does not fail semantic check",
			results: []CheckResult{
				{ID: "restore", Status: CheckPassed, Required: true},
				{
					ID:       "semantic",
					Status:   CheckPassed,
					Required: true,
					Warnings: []Warning{{Code: "empty_table", Summary: "An observed table has no rows."}},
				},
			},
			required: []string{"restore", "semantic"},
			want:     VerdictPass,
		},
		{
			name: "required failure takes precedence",
			results: []CheckResult{
				{ID: "restore", Status: CheckFailed, Required: true},
				{ID: "semantic", Status: CheckInconclusive, Required: true},
			},
			required:   []string{"restore", "semantic"},
			want:       VerdictFail,
			wantReason: []string{"required check failed: restore", "required check inconclusive: semantic"},
		},
		{
			name: "inconclusive required check",
			results: []CheckResult{
				{ID: "restore", Status: CheckPassed, Required: true},
				{ID: "semantic", Status: CheckInconclusive, Required: true},
			},
			required:   []string{"restore", "semantic"},
			want:       VerdictInconclusive,
			wantReason: []string{"required check inconclusive: semantic"},
		},
		{
			name: "missing required result",
			results: []CheckResult{
				{ID: "restore", Status: CheckPassed, Required: true},
			},
			required:   []string{"restore", "integrity"},
			want:       VerdictInconclusive,
			wantReason: []string{"required check missing: integrity"},
		},
		{
			name: "optional failure does not affect verdict",
			results: []CheckResult{
				{ID: "restore", Status: CheckPassed, Required: true},
				{ID: "optional", Status: CheckFailed},
			},
			required: []string{"restore"},
			want:     VerdictPass,
		},
		{
			name: "optional warning does not affect verdict",
			results: []CheckResult{
				{ID: "restore", Status: CheckPassed, Required: true},
				{ID: "optional", Status: CheckWarning},
			},
			required: []string{"restore"},
			want:     VerdictPass,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := Evaluate(test.results, test.required)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if got.Verdict != test.want {
				t.Errorf("Evaluate() verdict = %q, want %q", got.Verdict, test.want)
			}
			if !reflect.DeepEqual(got.Reasons, test.wantReason) {
				t.Errorf("Evaluate() reasons = %#v, want %#v", got.Reasons, test.wantReason)
			}
		})
	}
}

func TestEvaluateRejectsAmbiguousOrInvalidInputs(t *testing.T) {
	tests := []struct {
		name     string
		results  []CheckResult
		required []string
	}{
		{
			name: "empty required checks",
		},
		{
			name:     "duplicate required ID",
			required: []string{"restore", "restore"},
		},
		{
			name:     "invalid required ID",
			required: []string{"../restore"},
		},
		{
			name:     "duplicate result",
			required: []string{"restore"},
			results: []CheckResult{
				{ID: "restore", Status: CheckPassed, Required: true},
				{ID: "restore", Status: CheckPassed, Required: true},
			},
		},
		{
			name:     "required policy mismatch",
			required: []string{"restore"},
			results:  []CheckResult{{ID: "restore", Status: CheckPassed}},
		},
		{
			name:     "warning cannot satisfy required check",
			required: []string{"restore"},
			results:  []CheckResult{{ID: "restore", Status: CheckWarning, Required: true}},
		},
		{
			name:     "invalid warning",
			required: []string{"restore"},
			results: []CheckResult{
				{ID: "restore", Status: CheckPassed, Required: true, Warnings: []Warning{{Code: " ", Summary: "empty"}}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Evaluate(test.results, test.required); err == nil {
				t.Fatal("Evaluate() error = nil, want invalid-input error")
			}
		})
	}
}
