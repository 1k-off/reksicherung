# Airon integration follow-up

This is a copy-ready implementation task for an Airon maintainer or coding
agent. It records integration constraints identified while building
Reksicherung; it does not authorize changes to Airon from the Reksicherung
repository.

## Prompt

You are working in the Airon repository only. Airon must remain an independent,
generic AI-powered job runner; do not add Reksicherung-specific package names,
dependencies, or restore logic.

### Context

Reksicherung is a separate system that performs backup restoreability checks.
Airon starts and runs MCP servers configured by the operator. Reksicherung
provides a high-level restore-check MCP operation and persists its own run ID,
lifecycle, authoritative deterministic verdict, and evidence. Airon separately
generates an Airon run ID and stores JSON logs containing steps, tool calls,
tool results, errors, and token usage.

The current integration has two correlation/observability gaps:

1. Airon's generated run ID is returned to the caller, but is not automatically
   available to MCP tool handlers. Passing it as a prompt variable is not a
   reliable contract because a model can omit or alter it.
2. Airon's JSON log is written in Airon's workspace. A separate long-lived MCP
   service cannot reliably obtain the corresponding log by reading a local
   path, and log contents may include sensitive prompt or tool data.

Airon prompt execution can stop on a failed step, so an MCP cleanup prompt is
not a reliable `finally` mechanism. The external service must own durable
cleanup and reconciliation; do not claim Airon guarantees final cleanup.

There is also a workflow-boundary mismatch for AI-assisted restore validation.
Airon issues MCP calls only while executing an LLM prompt, and the MCP server
does not receive control to ask Airon to run another prompt. Therefore a single
synchronous high-level restore-check MCP call cannot restore a database, pause
to have Airon inspect it through SQL tools, and then resume cleanup in the same
call. Airon can order prompt steps, but it does not currently provide a
declarative non-LLM tool invocation or an unconditional finalizer. Do not hide
this limitation behind prompt wording or claim that a cleanup prompt is
reliable.

### Required work

1. Inspect the current public `Run` API, MCP client wrappers, and JSON run-log
   writer. Preserve existing behavior and API compatibility where practical.
2. Add a reliable, generic correlation mechanism so every MCP tool call made
   during one Airon run carries Airon's generated run ID as request metadata.
   Prefer protocol metadata such as MCP request `_meta` if supported by the
   current Go SDK, rather than adding an undeclared tool argument or depending
   on prompt instructions. The MCP server must be able to read it without
   changing its tool schema. Document exact semantics and any transport
   limitations.
3. Add a generic, opt-in run-log handoff mechanism so a caller can receive the
   completed Airon run log and token usage for both successful and failed
   runs. Do not make a filesystem path on Airon's host the only handoff
   contract. Use an observer/sink/callback or another transport-neutral API;
   bound payload size, propagate sink failures explicitly, and do not silently
   report a successful handoff when it failed.
4. Keep log export opt-in and document that logs can contain prompts, MCP
   arguments/results, and potentially sensitive information. Respect existing
   redaction/logging settings; do not leak secret environment values through
   the new metadata or handoff mechanism.
5. Do not implement `finally` as another AI prompt. Any callback used for
   result/log delivery must be deterministic and run outside model-generated
   tool-call sequencing. Continue to treat external resource cleanup as the
   external service's durable responsibility.
6. Add tests using a fake MCP server and a failing/succeeding run to establish:
   - every call in the same run receives the same generated run ID;
   - distinct Airon runs receive distinct IDs;
   - tool schemas/arguments are unchanged by correlation metadata;
   - run-log handoff receives the final structured log and aggregate token
     usage on both success and failure;
   - disabled handoff causes no export;
   - handoff errors are surfaced and do not expose log contents in diagnostics;
   - payload limits and cancellation are respected.
7. Update Airon's user documentation and public API comments. Include a minimal
   example showing how an external system can correlate MCP calls and consume
   the optional run result without coupling Airon to that system.
8. Resolve the workflow-boundary mismatch with a generic Airon capability, not
   a Reksicherung-specific dependency. Inspect and document a viable contract
   for a job that needs deterministic preparation, a model-driven MCP
   inspection phase, and deterministic result handoff. Preserve the external
   service as owner of durable resources and cleanup. If Airon cannot safely
   provide a single-call nested model phase, define a generic multi-step or
   resumable job contract and state explicitly that the caller must coordinate
   phases; do not imply that one blocking MCP handler can invoke Airon midway.
   Add tests proving the chosen phase ordering and behavior on prompt failure,
   cancellation, and process restart. The external service's lease/reconciler
   must remain sufficient to clean up even when Airon never executes a later
   phase.

### Constraints and acceptance

- Airon remains an independent generic library/application; no ReKSicherung
  imports, types, or hard-coded tool names.
- Do not put secrets into argv, tool arguments, or unredacted diagnostics.
- Do not claim that JSON logs are tamper-proof.
- Keep the existing JSON log schema backward-compatible unless a versioned
  migration is necessary.
- Run the full Airon unit, race, vet, and build checks, plus the new MCP
  correlation and log-handoff tests.
- Report limitations that prevent automatic handoff or correlation for any
  supported MCP transport.
