// Package workflow contains deterministic restore-check decision logic.
package workflow

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type CheckStatus string

const (
	CheckPassed       CheckStatus = "passed"
	CheckFailed       CheckStatus = "failed"
	CheckInconclusive CheckStatus = "inconclusive"
	CheckWarning      CheckStatus = "warning"
)

type Verdict string

const (
	VerdictPass         Verdict = "PASS"
	VerdictFail         Verdict = "FAIL"
	VerdictInconclusive Verdict = "INCONCLUSIVE"
)

// CheckResult is one deterministic check or structured AI assessment. Warnings
// are informational and do not independently fail a required check.
type CheckResult struct {
	ID       string
	Status   CheckStatus
	Required bool
	Summary  string
	Warnings []Warning
}

// Warning describes an observation, such as an empty table. It does not affect
// the aggregate verdict unless the check itself is failed.
type Warning struct {
	Code    string
	Summary string
}

// Evaluation includes the authoritative result and stable, machine-readable
// reasons for it.
type Evaluation struct {
	Verdict Verdict
	Reasons []string
}

// Evaluate computes a verdict from explicit required-check outcomes.
//
// Any required failure yields FAIL, even if another required check is
// inconclusive. Otherwise, any missing or inconclusive required result yields
// INCONCLUSIVE. PASS requires every required check to be present and passed.
// Optional checks and warnings are reported by callers but do not affect the
// verdict.
func Evaluate(results []CheckResult, requiredIDs []string) (Evaluation, error) {
	if len(requiredIDs) == 0 {
		return Evaluation{}, errors.New("workflow: at least one required check must be configured")
	}
	required := make(map[string]struct{}, len(requiredIDs))
	for _, id := range requiredIDs {
		if !validCheckID(id) {
			return Evaluation{}, fmt.Errorf("workflow: invalid required check ID %q", id)
		}
		if _, exists := required[id]; exists {
			return Evaluation{}, fmt.Errorf("workflow: duplicate required check ID %q", id)
		}
		required[id] = struct{}{}
	}

	byID := make(map[string]CheckResult, len(results))
	for _, result := range results {
		if !validCheckID(result.ID) {
			return Evaluation{}, fmt.Errorf("workflow: invalid result check ID %q", result.ID)
		}
		if _, exists := byID[result.ID]; exists {
			return Evaluation{}, fmt.Errorf("workflow: duplicate check result %q", result.ID)
		}
		switch result.Status {
		case CheckPassed, CheckFailed, CheckInconclusive, CheckWarning:
		default:
			return Evaluation{}, fmt.Errorf("workflow: invalid status %q for check %q", result.Status, result.ID)
		}
		if _, isRequired := required[result.ID]; result.Required != isRequired {
			return Evaluation{}, fmt.Errorf("workflow: required flag for check %q does not match configured policy", result.ID)
		}
		if result.Status == CheckWarning && result.Required {
			return Evaluation{}, fmt.Errorf("workflow: required check %q cannot have warning-only status", result.ID)
		}
		for _, warning := range result.Warnings {
			if !validCheckID(warning.Code) || strings.TrimSpace(warning.Summary) == "" {
				return Evaluation{}, fmt.Errorf("workflow: invalid warning in check %q", result.ID)
			}
		}
		byID[result.ID] = result
	}

	evaluation := Evaluation{Verdict: VerdictPass}
	hasFailure := false
	hasInconclusive := false
	for id := range required {
		result, exists := byID[id]
		if !exists {
			hasInconclusive = true
			evaluation.Reasons = append(evaluation.Reasons, "required check missing: "+id)
			continue
		}
		switch result.Status {
		case CheckPassed:
		case CheckFailed:
			hasFailure = true
			evaluation.Reasons = append(evaluation.Reasons, "required check failed: "+id)
		case CheckInconclusive:
			hasInconclusive = true
			evaluation.Reasons = append(evaluation.Reasons, "required check inconclusive: "+id)
		default:
			return Evaluation{}, fmt.Errorf("workflow: unsupported required status %q for check %q", result.Status, id)
		}
	}
	switch {
	case hasFailure:
		evaluation.Verdict = VerdictFail
	case hasInconclusive:
		evaluation.Verdict = VerdictInconclusive
	}
	sort.Strings(evaluation.Reasons)
	return evaluation, nil
}

func validCheckID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i, r := range id {
		alphaNumeric := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if i == 0 && !alphaNumeric {
			return false
		}
		if !alphaNumeric && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}
