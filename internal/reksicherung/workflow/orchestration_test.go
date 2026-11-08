package workflow

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1k-off/reksicherung/internal/reksicherung/plan"
)

type runnerFunc func(context.Context, string) ([]CheckResult, error)

func (f runnerFunc) RunDatabase(ctx context.Context, id string) ([]CheckResult, error) {
	return f(ctx, id)
}

func TestCoordinatorRunPolicies(t *testing.T) {
	tests := []struct {
		name          string
		plan          plan.RunPlan
		run           runnerFunc
		wantCalls     []string
		wantOutcomes  []OutcomeState
		wantVerdict   Verdict
		wantOutcomeID []string
	}{
		{
			name: "continue runs later databases after a required check fails",
			plan: testPlan([]string{"db-b", "db-a"}, plan.Sequential, 1, plan.Continue),
			run: func(_ context.Context, id string) ([]CheckResult, error) {
				if id == "db-b" {
					return []CheckResult{{ID: "restore", Status: CheckFailed, Required: true}}, nil
				}
				return passedRestore(), nil
			},
			wantCalls:     []string{"db-b", "db-a"},
			wantOutcomes:  []OutcomeState{OutcomeFailed, OutcomePassed},
			wantVerdict:   VerdictFail,
			wantOutcomeID: []string{"db-b", "db-a"},
		},
		{
			name: "fail fast stops later sequential databases after execution error",
			plan: testPlan([]string{"db-a", "db-b", "db-c"}, plan.Sequential, 1, plan.FailFast),
			run: func(_ context.Context, id string) ([]CheckResult, error) {
				if id == "db-a" {
					return nil, errors.New("restore operation failed")
				}
				return passedRestore(), nil
			},
			wantCalls:     []string{"db-a"},
			wantOutcomes:  []OutcomeState{OutcomeFailed, OutcomeNotStarted, OutcomeNotStarted},
			wantVerdict:   VerdictFail,
			wantOutcomeID: []string{"db-a", "db-b", "db-c"},
		},
		{
			name: "missing required result remains inconclusive",
			plan: testPlan([]string{"db-a"}, plan.Sequential, 1, plan.Continue),
			run: func(context.Context, string) ([]CheckResult, error) {
				return nil, nil
			},
			wantCalls:     []string{"db-a"},
			wantOutcomes:  []OutcomeState{OutcomeInconclusive},
			wantVerdict:   VerdictInconclusive,
			wantOutcomeID: []string{"db-a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			result, err := (Coordinator{Runner: runnerFunc(func(ctx context.Context, id string) ([]CheckResult, error) {
				calls = append(calls, id)
				return tt.run(ctx, id)
			})}).Run(context.Background(), tt.plan, testInventory(tt.wantOutcomeID...))
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if !reflect.DeepEqual(calls, tt.wantCalls) {
				t.Errorf("runner calls = %v, want %v", calls, tt.wantCalls)
			}
			if got := outcomeStates(result.Outcomes); !reflect.DeepEqual(got, tt.wantOutcomes) {
				t.Errorf("outcome states = %v, want %v", got, tt.wantOutcomes)
			}
			if result.Evaluation.Verdict != tt.wantVerdict {
				t.Errorf("verdict = %q, want %q; reasons: %v", result.Evaluation.Verdict, tt.wantVerdict, result.Evaluation.Reasons)
			}
			if got := outcomeIDs(result.Outcomes); !reflect.DeepEqual(got, tt.wantOutcomeID) {
				t.Errorf("outcome IDs = %v, want %v", got, tt.wantOutcomeID)
			}
		})
	}
}

func TestCoordinatorWithholdsRunnerErrorDetails(t *testing.T) {
	const secret = "database-password-sentinel"
	result, err := (Coordinator{
		Runner: runnerFunc(func(context.Context, string) ([]CheckResult, error) {
			return nil, errors.New("connection failed with password " + secret)
		}),
	}).Run(context.Background(), testPlan([]string{"db-a"}, plan.Sequential, 1, plan.Continue), testInventory("db-a"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := result.Outcomes[0].Error; got == "" || strings.Contains(got, secret) {
		t.Errorf("outcome error = %q, want a non-empty sanitized error", got)
	}
	if result.Evaluation.Verdict != VerdictFail {
		t.Errorf("verdict = %q, want %q", result.Evaluation.Verdict, VerdictFail)
	}
}

func TestCoordinatorNormalizesAndOrdersResults(t *testing.T) {
	runner := runnerFunc(func(_ context.Context, _ string) ([]CheckResult, error) {
		return []CheckResult{
			{ID: "semantic", Status: CheckPassed},
			{ID: "restore", Status: CheckPassed, Required: true},
		}, nil
	})
	requested := testPlan(nil, plan.Any, 2, plan.Continue)
	requested.Selection = plan.Selection{All: true}
	result, err := (Coordinator{Runner: runner}).Run(context.Background(), requested, testInventory("db-z", "db-a"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := outcomeIDs(result.Outcomes), []string{"db-a", "db-z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outcome IDs = %v, want normalized order %v", got, want)
	}
	if got, want := []string{result.Outcomes[0].Checks[0].ID, result.Outcomes[0].Checks[1].ID}, []string{"restore", "semantic"}; !reflect.DeepEqual(got, want) {
		t.Errorf("check IDs = %v, want sorted %v", got, want)
	}
	if result.Evaluation.Verdict != VerdictPass {
		t.Errorf("verdict = %q, want %q", result.Evaluation.Verdict, VerdictPass)
	}
}

func TestCoordinatorBoundsParallelExecution(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{}, 3)
	var active, maximum atomic.Int32
	runner := runnerFunc(func(_ context.Context, id string) ([]CheckResult, error) {
		current := active.Add(1)
		for previous := maximum.Load(); current > previous && !maximum.CompareAndSwap(previous, current); previous = maximum.Load() {
		}
		started <- id
		<-release
		active.Add(-1)
		return passedRestore(), nil
	})
	runDone := make(chan struct {
		result RunResult
		err    error
	}, 1)
	go func() {
		result, err := (Coordinator{Runner: runner}).Run(
			context.Background(),
			testPlan([]string{"db-a", "db-b", "db-c"}, plan.Any, 2, plan.Continue),
			testInventory("db-a", "db-b", "db-c"),
		)
		runDone <- struct {
			result RunResult
			err    error
		}{result: result, err: err}
	}()

	<-started
	<-started
	select {
	case id := <-started:
		t.Fatalf("database %q started while both permitted slots were occupied", id)
	case <-time.After(25 * time.Millisecond):
	}
	release <- struct{}{}
	<-started
	release <- struct{}{}
	release <- struct{}{}

	run := <-runDone
	if run.err != nil {
		t.Fatalf("Run() error = %v", run.err)
	}
	if got := maximum.Load(); got != 2 {
		t.Errorf("maximum active calls = %d, want 2", got)
	}
	if got, want := outcomeIDs(run.result.Outcomes), []string{"db-a", "db-b", "db-c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("outcome IDs = %v, want %v", got, want)
	}
}

func TestCoordinatorCancellationIsExplicit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	result, err := (Coordinator{Runner: runnerFunc(func(context.Context, string) ([]CheckResult, error) {
		calls++
		return passedRestore(), nil
	})}).Run(
		ctx,
		testPlan([]string{"db-a", "db-b"}, plan.Sequential, 1, plan.Continue),
		testInventory("db-a", "db-b"),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 0 {
		t.Errorf("runner calls = %d, want 0 for an already-canceled context", calls)
	}
	if got, want := outcomeStates(result.Outcomes), []OutcomeState{OutcomeCanceled, OutcomeCanceled}; !reflect.DeepEqual(got, want) {
		t.Errorf("outcome states = %v, want %v", got, want)
	}
	if result.Evaluation.Verdict != VerdictInconclusive {
		t.Errorf("verdict = %q, want %q", result.Evaluation.Verdict, VerdictInconclusive)
	}
}

func TestCoordinatorRejectsInvalidSetupBeforeRunning(t *testing.T) {
	called := false
	runner := runnerFunc(func(context.Context, string) ([]CheckResult, error) {
		called = true
		return passedRestore(), nil
	})
	tests := []struct {
		name      string
		plan      plan.RunPlan
		inventory []Database
	}{
		{
			name:      "database has no required checks",
			plan:      testPlan([]string{"db-a"}, plan.Sequential, 1, plan.Continue),
			inventory: []Database{{ID: "db-a"}},
		},
		{
			name:      "unknown database selected",
			plan:      testPlan([]string{"db-missing"}, plan.Sequential, 1, plan.Continue),
			inventory: testInventory("db-a"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called = false
			if _, err := (Coordinator{Runner: runner}).Run(context.Background(), tt.plan, tt.inventory); err == nil {
				t.Fatal("Run() error = nil, want invalid setup error")
			}
			if called {
				t.Error("runner was called before invalid setup was rejected")
			}
		})
	}
}

func TestCoordinatorParallelRunnerIsSafeForConcurrentCalls(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]bool)
	runner := runnerFunc(func(_ context.Context, id string) ([]CheckResult, error) {
		mu.Lock()
		seen[id] = true
		mu.Unlock()
		return passedRestore(), nil
	})
	result, err := (Coordinator{Runner: runner}).Run(
		context.Background(),
		testPlan([]string{"db-a", "db-b", "db-c"}, plan.Any, 3, plan.Continue),
		testInventory("db-a", "db-b", "db-c"),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(seen) != 3 {
		t.Errorf("runner observed %d databases, want 3", len(seen))
	}
	if result.Evaluation.Verdict != VerdictPass {
		t.Errorf("verdict = %q, want %q", result.Evaluation.Verdict, VerdictPass)
	}
}

func testPlan(ids []string, ordering plan.Ordering, parallelism int, handling plan.ErrorHandling) plan.RunPlan {
	return plan.RunPlan{
		Selection:      plan.Selection{DatabaseIDs: ids},
		Ordering:       ordering,
		MaxParallelism: parallelism,
		ErrorHandling:  handling,
	}
}

func testInventory(ids ...string) []Database {
	databases := make([]Database, len(ids))
	for i, id := range ids {
		databases[i] = Database{ID: id, RequiredCheckIDs: []string{"restore"}}
	}
	return databases
}

func passedRestore() []CheckResult {
	return []CheckResult{{ID: "restore", Status: CheckPassed, Required: true}}
}

func outcomeIDs(outcomes []DatabaseOutcome) []string {
	ids := make([]string, len(outcomes))
	for i, outcome := range outcomes {
		ids[i] = outcome.ID
	}
	return ids
}

func outcomeStates(outcomes []DatabaseOutcome) []OutcomeState {
	states := make([]OutcomeState, len(outcomes))
	for i, outcome := range outcomes {
		states[i] = outcome.State
	}
	return states
}
