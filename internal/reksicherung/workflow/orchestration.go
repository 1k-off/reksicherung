package workflow

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/1k-off/reksicherung/internal/reksicherung/plan"
)

// Database describes a selectable inventory item and the checks that must
// pass for it. IDs are stable inventory IDs, not display names.
type Database struct {
	ID               string
	RequiredCheckIDs []string
}

// DatabaseRunner performs the workflow for one database. Implementations own
// integrations such as backup providers, restore environments, validation
// tools, and orchestrator callbacks; the coordinator depends on none of those systems.
//
// Implementations should honor context cancellation and return sanitized
// errors. A returned error is recorded as a failed database execution.
type DatabaseRunner interface {
	RunDatabase(context.Context, string) ([]CheckResult, error)
}

// Coordinator normalizes and executes one run plan using an injected runner.
type Coordinator struct {
	Runner DatabaseRunner
}

// OutcomeState records whether a selected database completed, failed, was
// canceled, or was not started because of fail-fast policy.
type OutcomeState string

const (
	OutcomePassed       OutcomeState = "passed"
	OutcomeFailed       OutcomeState = "failed"
	OutcomeInconclusive OutcomeState = "inconclusive"
	OutcomeCanceled     OutcomeState = "canceled"
	OutcomeNotStarted   OutcomeState = "not_started"
)

// DatabaseOutcome is the explicit record for one selected database. Checks
// are ordered by check ID, and outcomes follow the normalized plan's order.
type DatabaseOutcome struct {
	ID         string
	State      OutcomeState
	Checks     []CheckResult
	Evaluation Evaluation
	Error      string
	Reason     string
}

// RunResult contains the validated plan, one outcome per selected database,
// and the authoritative aggregate evaluation.
type RunResult struct {
	Plan       plan.RunPlan
	Outcomes   []DatabaseOutcome
	Evaluation Evaluation
}

// Run validates and normalizes the requested plan before starting any
// database work. It executes at most MaxParallelism databases concurrently.
// Sequential plans retain their explicit selection order. Any-order plans
// retain normalized selection order in the returned outcomes regardless of
// completion order.
//
// Operational errors and cancellation are represented in Outcomes and do not
// discard the partial run record. The returned Go error is reserved for invalid
// inputs or coordinator configuration. A runner that ignores cancellation can
// delay Run, so runner implementations must honor the supplied context.
func (c Coordinator) Run(ctx context.Context, requested plan.RunPlan, inventory []Database) (RunResult, error) {
	if ctx == nil {
		return RunResult{}, errors.New("workflow: context must not be nil")
	}
	if c.Runner == nil {
		return RunResult{}, errors.New("workflow: database runner must not be nil")
	}

	inventoryIDs := make([]string, len(inventory))
	byID := make(map[string]Database, len(inventory))
	for i, database := range inventory {
		inventoryIDs[i] = database.ID
		byID[database.ID] = database
	}
	normalized, err := requested.Normalize(inventoryIDs)
	if err != nil {
		return RunResult{}, fmt.Errorf("workflow: normalize run plan: %w", err)
	}

	for _, id := range normalized.Selection.DatabaseIDs {
		database := byID[id]
		if err := validateRequiredChecks(database.RequiredCheckIDs); err != nil {
			return RunResult{}, fmt.Errorf("workflow: database %q: %w", id, err)
		}
	}

	ids := normalized.Selection.DatabaseIDs
	outcomes := make([]DatabaseOutcome, len(ids))
	for i, id := range ids {
		outcomes[i] = DatabaseOutcome{ID: id, State: OutcomeNotStarted}
	}

	limit := normalized.MaxParallelism
	next, active := 0, 0
	completed := make(chan execution, limit)
	stopStarting := false

	for next < len(ids) || active > 0 {
		for !stopStarting && next < len(ids) && active < limit && ctx.Err() == nil {
			index, id := next, ids[next]
			next++
			active++
			go func() {
				checks, runErr := c.Runner.RunDatabase(ctx, id)
				completed <- execution{index: index, checks: checks, err: runErr}
			}()
		}

		if active == 0 {
			break
		}

		result := <-completed
		active--
		outcome := makeOutcome(ids[result.index], byID[ids[result.index]].RequiredCheckIDs, result.checks, result.err)
		outcomes[result.index] = outcome
		if normalized.ErrorHandling == plan.FailFast && outcome.State == OutcomeFailed {
			stopStarting = true
		}
	}

	for i := next; i < len(ids); i++ {
		switch {
		case ctx.Err() != nil:
			outcomes[i].State = OutcomeCanceled
			outcomes[i].Reason = "not started: context canceled"
			outcomes[i].Error = ctx.Err().Error()
		case stopStarting:
			outcomes[i].State = OutcomeNotStarted
			outcomes[i].Reason = "not started: fail-fast after a failed database"
		}
	}

	evaluation := aggregate(outcomes, byID)
	return RunResult{Plan: normalized, Outcomes: outcomes, Evaluation: evaluation}, nil
}

type execution struct {
	index  int
	checks []CheckResult
	err    error
}

func validateRequiredChecks(ids []string) error {
	if len(ids) == 0 {
		return errors.New("at least one required check must be configured")
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !validCheckID(id) {
			return fmt.Errorf("invalid required check ID %q", id)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate required check ID %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func makeOutcome(id string, required []string, checks []CheckResult, runErr error) DatabaseOutcome {
	orderedChecks := append([]CheckResult(nil), checks...)
	sort.SliceStable(orderedChecks, func(i, j int) bool { return orderedChecks[i].ID < orderedChecks[j].ID })

	outcome := DatabaseOutcome{ID: id}
	evaluation, evaluationErr := Evaluate(orderedChecks, required)
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			outcome.State = OutcomeCanceled
			outcome.Reason = "database execution canceled"
			outcome.Error = "database execution was canceled or timed out"
			return outcome
		}
		outcome.State = OutcomeFailed
		outcome.Reason = "database execution failed"
		outcome.Error = "database execution failed; details were withheld"
		return outcome
	}

	if evaluationErr != nil {
		outcome.State = OutcomeFailed
		outcome.Error = "database runner returned invalid check results; details were withheld"
		outcome.Reason = "database runner returned invalid check results"
		return outcome
	}
	outcome.Checks = orderedChecks
	outcome.Evaluation = evaluation
	switch evaluation.Verdict {
	case VerdictPass:
		outcome.State = OutcomePassed
	case VerdictFail:
		outcome.State = OutcomeFailed
		outcome.Reason = "one or more required checks failed"
	case VerdictInconclusive:
		outcome.State = OutcomeInconclusive
		outcome.Reason = "one or more required checks are missing or inconclusive"
	}
	return outcome
}

func aggregate(outcomes []DatabaseOutcome, inventory map[string]Database) Evaluation {
	result := Evaluation{Verdict: VerdictPass}
	hasFailure, hasInconclusive := false, false
	for _, outcome := range outcomes {
		database := inventory[outcome.ID]
		checksEvaluation, err := Evaluate(outcome.Checks, database.RequiredCheckIDs)
		if err != nil {
			// makeOutcome already captures invalid results as a failed execution.
			// The empty-result evaluation is valid and safely inconclusive.
			checksEvaluation, _ = Evaluate(nil, database.RequiredCheckIDs)
		}
		for _, reason := range checksEvaluation.Reasons {
			result.Reasons = append(result.Reasons, "database "+outcome.ID+": "+reason)
		}
		switch outcome.State {
		case OutcomeFailed:
			hasFailure = true
			if outcome.Reason != "" {
				result.Reasons = append(result.Reasons, "database "+outcome.ID+": "+outcome.Reason)
			}
		case OutcomeCanceled:
			hasInconclusive = true
			if outcome.Reason != "" {
				result.Reasons = append(result.Reasons, "database "+outcome.ID+": "+outcome.Reason)
			}
		case OutcomeNotStarted:
			hasInconclusive = true
			if outcome.Reason != "" {
				result.Reasons = append(result.Reasons, "database "+outcome.ID+": "+outcome.Reason)
			}
		}
		if checksEvaluation.Verdict == VerdictFail {
			hasFailure = true
		} else if checksEvaluation.Verdict == VerdictInconclusive {
			hasInconclusive = true
		}
	}
	switch {
	case hasFailure:
		result.Verdict = VerdictFail
	case hasInconclusive:
		result.Verdict = VerdictInconclusive
	}
	sort.Strings(result.Reasons)
	return result
}
