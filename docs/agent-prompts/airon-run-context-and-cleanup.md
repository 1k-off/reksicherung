# Implementation task: Airon run context and cleanup finalizers

## Scope

Improve Airon so MCP integrations can reliably correlate external operations with an Airon run and register cleanup/finalizer work that executes when a run ends, including when a prompt/provider/tool phase fails, times out, or is canceled.

This is a standalone task prompt. The implementing agent should inspect the current Airon code and tests before choosing APIs. Do not assume the repository layout or behavior described here is still current.

## Constraints

- Work only in the Airon repository and its normal feature branch.
- Preserve backward compatibility for existing `airon.Run` callers, MCP servers, configs, and JSON logs where practical.
- Do not expose credentials or secret values through run context, logs, MCP arguments, or errors.
- Do not claim cleanup can be guaranteed after process termination or machine failure; provide durable/reconciliation guidance where in-process finalizers cannot run.
- Keep domain-specific lifecycle logic out of Airon. Airon should provide generic run correlation and finalizer mechanisms.

## Required capabilities

1. **Run correlation**
   - Make the active Airon run ID available to every MCP invocation without relying on the LLM to copy it into tool arguments.
   - Prefer MCP request metadata or an explicit, reserved context envelope that does not mutate a tool's declared business arguments.
   - Preserve caller-provided correlation metadata separately from Airon's own run ID.
   - Include the run ID in structured tool-call/run-log records and make it accessible to MCP server implementations through a documented API.
   - Add tests proving the identifier is stable for one run, differs across runs, and is present on every tool call including retries.

2. **Always-run finalizers**
   - Add a generic API for registering cleanup/finalizer functions with run scope.
   - Execute finalizers once after normal completion, prompt failure, provider error, tool-loop exhaustion, timeout, and cancellation.
   - Run finalizers with a bounded cleanup context that is not already canceled by the parent run, while honoring a configurable maximum cleanup duration.
   - Preserve the primary run error and separately report all cleanup errors in the public result and JSON log.
   - Define deterministic ordering (prefer reverse registration order) and avoid concurrent duplicate finalizer execution.
   - Ensure app/MCP connection close and domain finalizers have clear, non-overlapping responsibilities.
   - Do not implement finalizer execution as a later LLM prompt; it must be runtime-controlled.

3. **Observability and compatibility**
   - Record finalizer name, start/end timestamps, duration, outcome, and sanitized error details.
   - Keep existing JSON logs readable; version the log schema if a breaking shape change is unavoidable.
   - Document that finalizers are best effort and cannot run after forced process termination. Provide integration guidance for durable leases, expirations, or external reconcilers for resources that must survive process crashes.
   - Update examples to demonstrate run-ID access and registering a cleanup action.

## Testing and acceptance

- Add unit tests covering success, each failure/cancellation path, cleanup timeout, multiple finalizers, error aggregation, ordering, and exactly-once invocation.
- Add MCP integration tests verifying run-ID propagation without changing tool arguments.
- Verify sensitive values are excluded from logs and surfaced errors.
- Run the repository's full test suite, race tests for affected packages, and `go vet ./...` (or the repository's documented equivalents).
- Update public API documentation and migration notes.

## Deliverables

- Implementation and focused tests.
- Updated docs/examples and any required config/log schema version notes.
- A concise report of design choices, commands run, test results, and limitations.
