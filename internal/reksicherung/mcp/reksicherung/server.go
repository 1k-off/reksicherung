// Package reksicherung exposes the high-level restore-check operation over
// MCP. It intentionally does not implement restore behavior; an injected
// service owns durable execution and the authoritative verdict.
package reksicherung

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"github.com/1k-off/reksicherung/internal/reksicherung/plan"
	"github.com/1k-off/reksicherung/internal/reksicherung/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxRequestBytes    = 16 * 1024
	maxSelectedDBs     = plan.MaxSelectedDatabases
	maxResultChecks    = 8
	maxResponseBytes   = 48 * 1024
	maxIdentifierBytes = 128
)

// AironRunIDMetaKey identifies Airon's generic run-correlation metadata in the
// MCP request's reserved `_meta` field.
const AironRunIDMetaKey = "com.1k-off/airon-run-id"

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Service starts or resumes a durable restore-check run and returns its
// persisted result. Implementations must own lifecycle persistence,
// reconciliation/cleanup after interruption, and computation of the
// authoritative verdict. They must honor context cancellation and must not
// return secret values or sensitive data in identifiers.
type Service interface {
	RestoreCheck(context.Context, Request) (ServiceResult, error)
}

// Request is the validated request passed to the restore-check service.
// AironRunID is correlation metadata only; it does not control execution.
type Request struct {
	Plan       plan.RunPlan `json:"plan" jsonschema:"Explicit database selection and execution policy."`
	AironRunID string       `json:"airon_run_id,omitempty" jsonschema:"Optional Airon run ID for correlation only."`
}

// ServiceResult is the bounded, structured result returned by the service.
// AironRunID must be the correlation ID persisted with this run; it must equal
// the ID supplied in Request, including when both are empty. Free-form
// summaries, errors, warnings, and evidence are deliberately not exposed by
// this MCP tool to prevent accidental disclosure.
type ServiceResult struct {
	RunID      string           `json:"run_id"`
	AironRunID string           `json:"airon_run_id,omitempty"`
	Databases  []DatabaseResult `json:"databases"`
	Verdict    workflow.Verdict `json:"verdict"`
}

// DatabaseResult reports the status and check outcomes for one selected
// database. Database IDs and check IDs must be safe stable identifiers.
type DatabaseResult struct {
	DatabaseID string                `json:"database_id"`
	Status     workflow.OutcomeState `json:"status"`
	Checks     []CheckResult         `json:"checks"`
}

// CheckResult is a secret-free projection of one workflow check outcome.
type CheckResult struct {
	ID       string               `json:"id"`
	Status   workflow.CheckStatus `json:"status"`
	Required bool                 `json:"required"`
}

// Result is the MCP tool's secret-free structured response.
type Result struct {
	RunID      string           `json:"run_id"`
	AironRunID string           `json:"airon_run_id,omitempty"`
	Databases  []DatabaseResult `json:"databases"`
	Verdict    workflow.Verdict `json:"verdict"`
}

type requestWire struct {
	Plan       json.RawMessage `json:"plan"`
	AironRunID string          `json:"airon_run_id,omitempty"`
}

type planWire struct {
	Selection      selectionWire      `json:"selection"`
	Ordering       plan.Ordering      `json:"ordering"`
	MaxParallelism int                `json:"max_parallelism"`
	ErrorHandling  plan.ErrorHandling `json:"error_handling"`
}

type selectionWire struct {
	All         *bool    `json:"all"`
	DatabaseIDs []string `json:"database_ids,omitempty"`
}

// UnmarshalJSON keeps unknown fields and oversized requests out of both the
// service and MCP error output. The nested plan is decoded strictly as well.
func (r *Request) UnmarshalJSON(data []byte) error {
	if len(data) > maxRequestBytes {
		return errors.New("invalid restore-check request")
	}
	var raw requestWire
	if err := decodeStrict(data, &raw); err != nil {
		return errors.New("invalid restore-check request")
	}
	if len(raw.Plan) == 0 {
		return errors.New("invalid restore-check request")
	}
	var requested planWire
	if err := decodeStrict(raw.Plan, &requested); err != nil {
		return errors.New("invalid restore-check request")
	}
	if requested.Selection.All == nil ||
		len(requested.Selection.DatabaseIDs) > maxSelectedDBs ||
		(raw.AironRunID != "" && !validIdentifier(raw.AironRunID)) {
		return errors.New("invalid restore-check request")
	}
	if *requested.Selection.All && len(requested.Selection.DatabaseIDs) != 0 ||
		!*requested.Selection.All && len(requested.Selection.DatabaseIDs) == 0 {
		return errors.New("invalid restore-check request")
	}

	normalizedPlan := plan.RunPlan{
		Selection: plan.Selection{
			All:         *requested.Selection.All,
			DatabaseIDs: append([]string(nil), requested.Selection.DatabaseIDs...),
		},
		Ordering:       requested.Ordering,
		MaxParallelism: requested.MaxParallelism,
		ErrorHandling:  requested.ErrorHandling,
	}
	if normalizedPlan.Selection.All {
		// Normalize checks policy invariants without claiming knowledge of the
		// service's inventory. A single synthetic inventory item is sufficient
		// to validate the all-target form; the service resolves real targets.
		if _, err := normalizedPlan.Normalize([]string{"validation-target"}); err != nil {
			return errors.New("invalid restore-check request")
		}
	} else {
		if _, err := normalizedPlan.Normalize(normalizedPlan.Selection.DatabaseIDs); err != nil {
			return errors.New("invalid restore-check request")
		}
	}
	*r = Request{Plan: normalizedPlan, AironRunID: raw.AironRunID}
	return nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

// NewServer registers the single high-level restore_check operation. The MCP
// transport that serves this server is responsible for authentication and TLS.
func NewServer(service Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "reksicherung",
		Version: "0.1.0",
	}, nil)

	mcp.AddTool[json.RawMessage, Result](server, &mcp.Tool{
		Name:        "restore_check",
		Description: "Run a durable Reksicherung restore check for the explicitly selected database scope and execution policy. Returns only run IDs, per-database check statuses, and the authoritative verdict.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"plan":{
					"type":"object",
					"properties":{
						"selection":{
							"type":"object",
							"properties":{
								"all":{"type":"boolean"},
								"database_ids":{
									"type":"array",
									"items":{"type":"string","pattern":"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$","maxLength":128},
									"maxItems":64
								}
							},
							"required":["all"],
							"oneOf":[
								{"properties":{"all":{"const":true},"database_ids":{"maxItems":0}}},
								{"properties":{"all":{"const":false},"database_ids":{"minItems":1}},"required":["database_ids"]}
							],
							"additionalProperties":false
						},
						"ordering":{"type":"string","enum":["sequential","any"]},
						"max_parallelism":{"type":"integer","minimum":1,"maximum":64},
						"error_handling":{"type":"string","enum":["continue","fail_fast"]}
					},
					"required":["selection","ordering","max_parallelism","error_handling"],
					"additionalProperties":false
				},
				"airon_run_id":{"type":"string","pattern":"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$","maxLength":128}
			},
			"required":["plan"],
			"additionalProperties":false
		}`),
	}, func(ctx context.Context, call *mcp.CallToolRequest, encoded json.RawMessage) (*mcp.CallToolResult, Result, error) {
		var request Request
		if err := json.Unmarshal(encoded, &request); err != nil {
			return nil, Result{}, errors.New("invalid restore-check request")
		}
		aironRunID, err := aironRunIDFromMeta(call)
		if err != nil || (aironRunID != "" && request.AironRunID != "" && request.AironRunID != aironRunID) {
			return nil, Result{}, errors.New("invalid restore-check request")
		}
		if aironRunID != "" {
			request.AironRunID = aironRunID
		}
		if service == nil {
			return nil, Result{}, errors.New("restore check service unavailable")
		}
		serviceResult, err := service.RestoreCheck(ctx, request)
		if err != nil {
			return nil, Result{}, errors.New("restore check failed")
		}
		if len(serviceResult.Databases) == 0 || len(serviceResult.Databases) > maxSelectedDBs {
			return nil, Result{}, errors.New("restore check failed")
		}
		if serviceResult.AironRunID != request.AironRunID {
			return nil, Result{}, errors.New("restore check failed")
		}
		databases := make([]DatabaseResult, len(serviceResult.Databases))
		for i, database := range serviceResult.Databases {
			if len(database.Checks) > maxResultChecks {
				return nil, Result{}, errors.New("restore check failed")
			}
			checks := make([]CheckResult, len(database.Checks))
			copy(checks, database.Checks)
			databases[i] = DatabaseResult{
				DatabaseID: database.DatabaseID,
				Status:     database.Status,
				Checks:     checks,
			}
		}
		result := Result{
			RunID:      serviceResult.RunID,
			AironRunID: serviceResult.AironRunID,
			Databases:  databases,
			Verdict:    serviceResult.Verdict,
		}
		if err := validateResult(request.Plan, result); err != nil {
			return nil, Result{}, errors.New("restore check failed")
		}
		encodedResult, err := json.Marshal(result)
		if err != nil || len(encodedResult) > maxResponseBytes {
			return nil, Result{}, errors.New("restore check failed")
		}
		return nil, result, nil
	})
	return server
}

func aironRunIDFromMeta(request *mcp.CallToolRequest) (string, error) {
	if request == nil || request.Params.Meta == nil {
		return "", nil
	}
	value, exists := request.Params.Meta[AironRunIDMetaKey]
	if !exists {
		return "", nil
	}
	runID, ok := value.(string)
	if !ok || !validIdentifier(runID) {
		return "", errors.New("invalid Airon run ID metadata")
	}
	return runID, nil
}

func validateResult(requested plan.RunPlan, result Result) error {
	if !validIdentifier(result.RunID) ||
		(result.Verdict != workflow.VerdictPass &&
			result.Verdict != workflow.VerdictFail &&
			result.Verdict != workflow.VerdictInconclusive) ||
		len(result.Databases) == 0 || len(result.Databases) > maxSelectedDBs {
		return errors.New("invalid service result")
	}
	selected := make(map[string]struct{}, len(requested.Selection.DatabaseIDs))
	for _, id := range requested.Selection.DatabaseIDs {
		selected[id] = struct{}{}
	}
	seenDatabases := make(map[string]struct{}, len(result.Databases))
	for _, database := range result.Databases {
		if !validIdentifier(database.DatabaseID) {
			return errors.New("invalid service result")
		}
		if _, exists := seenDatabases[database.DatabaseID]; exists {
			return errors.New("invalid service result")
		}
		seenDatabases[database.DatabaseID] = struct{}{}
		if !requested.Selection.All {
			if _, exists := selected[database.DatabaseID]; !exists {
				return errors.New("invalid service result")
			}
		}
		switch database.Status {
		case workflow.OutcomePassed, workflow.OutcomeFailed, workflow.OutcomeInconclusive,
			workflow.OutcomeCanceled, workflow.OutcomeNotStarted:
		default:
			return errors.New("invalid service result")
		}
		if len(database.Checks) > maxResultChecks {
			return errors.New("invalid service result")
		}
		seenChecks := make(map[string]struct{}, len(database.Checks))
		for _, check := range database.Checks {
			if !validIdentifier(check.ID) {
				return errors.New("invalid service result")
			}
			if _, exists := seenChecks[check.ID]; exists {
				return errors.New("invalid service result")
			}
			seenChecks[check.ID] = struct{}{}
			switch check.Status {
			case workflow.CheckPassed, workflow.CheckFailed, workflow.CheckInconclusive, workflow.CheckWarning:
			default:
				return errors.New("invalid service result")
			}
		}
	}
	if !requested.Selection.All && len(seenDatabases) != len(selected) {
		return errors.New("invalid service result")
	}
	return nil
}

func validIdentifier(value string) bool {
	return len(value) <= maxIdentifierBytes && identifierPattern.MatchString(value)
}
