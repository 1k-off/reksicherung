# Reksicherung — project specification

**Status:** Implementation baseline (compose/MCP architecture)  
**Purpose:** Canonical product scope and architecture for Reksicherung.  
**Naming:** Use **Reksicherung** in documentation, product references, and package/import-path naming. Go module: `github.com/1k-off/reksicherung`. The repository currently named `bcai` will be renamed later; do not make the repository name a dependency of the design.

## 1. Summary

Reksicherung verifies that backups can be restored and that restored data passes defined integrity and application-level checks. It exposes MCP servers and skills that any orchestrator (or a human operator) can drive. Airon is one optional example binding under `examples/airon/` and is not a dependency. Users provision and operate MCP servers and database services with Docker Compose; Reksicherung does not create or manage containers.

The initial target is SQL Server databases backed up with GoBackup or Restic, with S3-compatible storage as the first storage backend. Reksicherung also includes a GoBackup configuration generator based on the generic generator in `infrastructure/utils/gobackup-config-generator`, not the generator in `onsite-infrastructure`.

The project is not another backup scheduler and does not claim that the existence of a backup proves recoverability. It tests recovery by restoring into an operator-provisioned, isolated SQL Server service and recording what was done and what passed or failed.

## 2. Goals

- Make backup restore checks repeatable, schedulable, and suitable for unattended operation through any MCP-capable orchestrator or operator automation.
- Verify progressively stronger properties:
  1. The expected backup is discoverable and readable.
  2. The restore operation completes.
  3. The restored SQL Server database passes database integrity checks.
  4. AI-assisted semantic checks inspect the restored schema and verify that relevant data-bearing tables contain plausible data.
- Work with user-provisioned restore targets and MCP services, without selecting or controlling a container engine.
- Clean up only run-scoped restored database state after a normal run; optionally preserve that state for human review.
- Keep backup, storage, database, secret-provider, and orchestration responsibilities separable.
- Produce useful audit evidence, including actions, timestamps, results, errors, optional external orchestrator run ID, and LLM token usage where available.
- Keep secrets out of repository configuration, generated backup YAML, command-line arguments, and logs.
- Allow future support for other storage backends, databases, and file-level checks without redesigning the core.

## 3. Non-goals

- Coupling Reksicherung to any specific orchestrator (including Airon), embedding an orchestrator, or requiring one to run restore checks.
- Making the audit log cryptographically tamper-proof. Signatures, append-only/WORM storage, and similar anti-tampering controls are explicitly out of scope for the initial design.
- Managing container engines, creating or removing containers, or provisioning infrastructure. Users own service lifecycle and resource limits through their deployment configuration.
- Creating a general-purpose secrets-management product.
- Supporting every database, storage backend, or backup engine in the first release.
- Treating an LLM verdict as proof that a restore is valid. Checks and the primary verdict must be based on tool results and explicit rules.

## 4. Product boundaries

### Orchestrators (optional)

Reksicherung must not depend on any specific job runtime. Optional orchestrators (Airon or others) may launch MCP servers, skills, and prompts. An optional `examples/airon/` binding shows one such integration; it is not part of the Reksicherung runtime contract.

Reksicherung creates and returns its own run ID. An orchestrator may supply optional correlation metadata via tool argument `external_run_id` or MCP `_meta` key `1node.xyz/external-run-id`. AI-assisted semantic review is performed by whatever agent drives the SQL inspect MCP tools; Reksicherung owns the run record and authoritative verdict from deterministic checks plus the structured AI-review result when present.

Reksicherung must expose explicit, retryable database operations and must not assume a later orchestrator cleanup step will run after a failure. These operations must never stop or remove user-managed services.

### Reksicherung

Reksicherung owns:

- Backup configuration generation and validation.
- Catalog/inventory generation for operators and orchestrators.
- MCP servers for SQL Server, GoBackup, and Restic, as separate components.
- Shared storage interfaces and the first S3-compatible implementation.
- Secret-reference resolution through a provider interface.
- Run-scoped restore operations and database cleanup/hold-for-review behavior through configured MCP services. Container and service lifecycle remain user-owned.
- Deterministic restore and validation results plus structured evidence.
- A restore-and-check skill/runbook usable by any MCP/skills runner.

### Other repositories

Implementations described in `onsite-infrastructure`, `infrastructure`, and optional orchestrators such as Airon are references only. No changes to those repositories are part of this specification. The GoBackup generator should be adapted from `infrastructure/utils/gobackup-config-generator`.

## 5. Initial scope and delivery phases

### Phase 0 — design and contracts

- Finalize the canonical YAML concepts, inventory format, MCP tool contracts, secret-reference syntax, and evidence schema.
- Record architectural choices and unresolved questions before implementation.

### Phase 1 — first usable restore check

- First end-to-end path: GoBackup backup in S3-compatible storage, restored and checked in SQL Server.
- GoBackup generator based on the generic infrastructure implementation.
- SQL Server MCP integration as a self-contained package.
- Shared S3-compatible storage package.
- Catalog/inventory generation that connects backup configuration to restore-check runs.
- Compose-provisioned SQL Server restore target with run-scoped database cleanup and optional human-review retention.
- Deterministic restore/integrity checks and machine-readable evidence.
- Add the Restic MCP integration before the first usable release, reusing the storage and SQL Server check contracts.
- Optional orchestrator skill/config example (see `examples/airon/`) that runs a check and preserves orchestrator logs when present.
- Secret references resolved at runtime through a provider abstraction. OpenBao is the first provider and is deployed/operated by the user alongside other services.

### Phase 2 — common backup configuration model

- Add a Restic configuration generator using the same canonical YAML model as GoBackup.
- Improve validation and compatibility tests across generator and inventory outputs.

### Later, explicitly not required for the first release

- Additional secret providers and storage backends.
- Other database engines.
- File-level restore/validation workflows.
- More advanced human approval/review workflows and reporting integrations.

## 6. Core workflow

1. **Discover:** Catalog generation reads the canonical Reksicherung configuration and relevant generated backup configuration/artifacts. It emits an inventory of backup sources, repositories, databases, and supported checks.
2. **Plan:** Reksicherung creates its own run ID. Project-specific prompt instructions may define which databases to check, whether all or only selected databases are required, and whether checks must be sequential or may run in any order. An orchestrator or operator turns those instructions into an explicit run plan; Reksicherung validates and records the plan before starting any restore. An external run ID may be recorded separately if supplied.
3. **Preflight:** Reksicherung validates configuration, resolves secret references, checks required MCP tool availability, and confirms the configured target is reachable. No restore is attempted if required preflight checks fail.
4. **Prepare:** Select a SQL Server restore target that the user has provisioned and exposed through the configured MCP service. Enforce per-run database naming and request bounds; do not create or reconfigure the service.
5. **Fetch and restore:** Read the selected backup through the relevant GoBackup or Restic MCP integration and restore it into the user-provisioned SQL Server target.
6. **Validate:** Run database integrity checks. An agent inspects the restored schema and uses restricted SQL Server tools to issue bounded, read-only `SELECT` queries to assess whether relevant tables contain data and whether the observed data is plausible for the project. Record the schema context, queries, sanitized/aggregated results, and structured AI assessment.
7. **Decide:** The agent returns a structured semantic-check result with evidence. Reksicherung aggregates it with deterministic restore/integrity results to compute the authoritative `PASS`, `FAIL`, or `INCONCLUSIVE` verdict. The AI cannot override a failed required check or mark an unrun required check as passed.
8. **Finalize:** In normal mode, delete only the run-scoped restored database through the SQL Server MCP service. In human-review mode, leave the database available and report how to inspect and remove it. Reksicherung records database cleanup state; it never stops or removes user-managed services.
9. **Record:** Write structured Reksicherung evidence and preserve any corresponding orchestrator logs, linked by run ID / optional external_run_id.

All started operations should have explicit states and timestamps. Cancellation, timeout, restore failure, validation failure, and cleanup failure must be represented in the final result and evidence rather than silently discarded.

Run operations must be idempotent and recoverable after Reksicherung (and optional orchestrator) restarts. MCP servers are user-provisioned and managed outside Reksicherung, typically by Docker Compose; no Reksicherung process owns container lifecycle. A local CLI may validate catalogs and generate inventory, but must not manage services.

## 7. Configuration and generators

### Canonical configuration

Use a versioned, human-maintainable YAML configuration as the source of truth. It should describe, without embedding secret values:

- Backup source/provider (`gobackup` or `restic`).
- Storage/repository reference and backup selection policy.
- Database type/name and restore target settings.
- Secret references for storage, backup-tool, and database credentials.
- Restore resource limits and timeouts.
- Validation profile: built-in integrity checks and optional explicitly configured semantic checks.
- Cleanup mode: normal cleanup or `hold_for_review`.
- Evidence output/retention settings.

The catalog schema is versioned as `reksicherung.1node.xyz/v1alpha2` with `kind: BackupCatalog`. It lists SQL Server sources (GoBackup or Restic) with selection/cleanup/validation only. S3/Restic coordinates and credentials are resolved from project OpenBao at `secret/reksicherung/sources/<source_id>` (optional defaults merge). Restic sources require catalog `backupPath` (absolute path in the snapshot). The backup inventory omits endpoints and secrets. See `docs/inputs.md`, `docs/schemas/`, and `docs/decisions/0002-thin-catalog-openbao-convention.md`. The schema remains subject to evolution before a stable `v1` release. Prompt instructions can define per-project selection and ordering policy, but the normalized run plan is recorded and validated rather than being left as unstructured prose at execution time.

### GoBackup generator

- Include a GoBackup configuration generator in Reksicherung.
- Use `infrastructure/utils/gobackup-config-generator` as its implementation/reference source and adapt it as needed.
- Do not copy the `onsite-infrastructure/util/configgen` implementation as the generator source.
- Generate the simplified GoBackup YAML/configuration from canonical project configuration, with secrets represented only by references or runtime environment bindings.
- Validate generated output and make generation deterministic where practical.
- Account for GoBackup's SQL Server plugin producing `.bacpac` exports via SqlPackage: the restore workflow must import the artifact into the isolated SQL Server using a compatible SqlPackage execution environment, not treat the artifact as a native SQL Server `.bak`.

### Restic generator

- Restic MCP/restore support is in the first usable scope.
- A Restic configuration generator is a later phase.
- It should consume the same canonical YAML model as the GoBackup generator rather than introduce a separate project-level schema.

### Catalog/inventory

Catalog generation transforms canonical configuration and generated backup artifacts into an orchestrator-consumable inventory. It should:

- Identify backup provider, storage/repository, source database, available backup-selection policy, and supported check profiles.
- Validate references and report missing/ambiguous configuration before a scheduled run.
- Avoid copying resolved secret values into inventory.
- Include stable identifiers and relative source locations so results can be traced back to configuration.

## 8. Package and executable layout (proposed)

All project-owned Go import paths and package namespaces should contain `reksicherung`. The Go module path is `github.com/1k-off/reksicherung`; it is independent of the repository's current `bcai` name.

```text
cmd/
  reksicherung/                       # CLI: validate, catalog
  reksicherung-mcp-sqlserver/         # read-only SQL Server MCP (AI review)
  reksicherung-mcp-sqlserver-restore/ # restore/integrity/drop against operator SQL Server
  reksicherung-mcp-gobackup/          # GoBackup discover + stage MCP
  reksicherung-mcp-restic/            # Restic discover + stage MCP
internal/reksicherung/
  config/                             # versioned YAML model and validation
  catalog/                            # config/artifact to backup inventory
  workflow/                           # verdict aggregation
  plan/                               # validated run-plan model
  restore/artifact/                   # resolve .bak/.bacpac from archives
  restore/staging/                    # operator-managed shared staging directory
  restore/sqlserver/                  # remote restore ops (sqlcmd/sqlpackage)
  restore/gobackup/                   # GoBackup archive helpers
  restore/restic/                     # local restic client (no container engine)
  runregistry/                        # durable run + DB cleanup leases
  evidence/                           # structured check evidence
  secrets/                            # secret-provider interface and references
  secrets/openbao/                    # OpenBao KV v2 provider
  storage/                            # shared storage interface
  storage/s3/                         # S3-compatible implementation
  mcp/sqlserver/                      # SQL Server read-only tool service
  mcp/gobackup/                       # GoBackup discovery service
  mcp/restic/                         # Restic discovery service
  mcp/reksicherung/                   # high-level restore-check MCP surface
  configgen/gobackup/                 # optional GoBackup generator
skills/reksicherung-restore-check/
  SKILL.md                            # restore/check skill runbook
examples/compose/                     # operator Docker Compose reference
docs/
  decisions/
```

Restore MCP services and SQL Server share operator-mounted staging/backup volumes. Reksicherung never starts or removes those services.

This is a proposed separation, not a commitment to expose all packages as public Go APIs. Keep provider-specific behavior within its package; shared packages should define narrow interfaces rather than import concrete MCP implementations.

## 9. MCP responsibilities

Each MCP server is a distinct, independently configurable component:

- **SQL Server MCP:** SQL Server-specific operations needed by the workflow, including restore-target inspection, database integrity checks, and configured validation queries. Keep it focused on SQL Server; it should not manage S3 or backup-tool configuration.
- **GoBackup MCP:** GoBackup-specific backup discovery/inspection and restore-related operations.
- **Restic MCP:** Restic-specific repository/snapshot inspection and restore-related operations.
- **Shared storage:** Common storage access used by provider integrations, beginning with S3-compatible object storage. It owns storage mechanics and credentials needed for storage access, not SQL Server semantics.

Tools should be narrow and purpose-specific. Avoid exposing unrestricted shell execution or arbitrary database administration as a general-purpose MCP tool. Tool inputs must be validated; operations that modify or delete data must be scoped to the per-run temporary environment.

Exact MCP tool names and argument schemas remain to be designed and documented with examples.

## 10. Restore target and lifecycle

- Connect only to a SQL Server restore target explicitly provisioned by the user and exposed by a configured MCP service.
- The target must be isolated from production and must not receive production database credentials.
- Users set CPU, memory, disk, lifetime, and network controls in Docker Compose or their chosen service manager. Reksicherung validates request bounds but does not create or modify services.
- **Normal mode:** drop only the run-scoped restored database after the run, including after failure where possible. Never stop or remove the SQL Server service.
- **Human-review mode:** `hold_for_review` leaves the restored database in place. Evidence reports its database identifier and cleanup instructions. Retention is explicit, not an accidental consequence of a failed cleanup.
- Record database cleanup attempts and outcomes. A cleanup failure must be visible and must not be reported as a fully clean successful run.
- Do not require encrypted temporary disks or cryptographic key destruction in the initial design.
- Reksicherung has no container-engine adapter. Compose files, SQL Server images, service lifecycle, network policy, and service resource limits belong to the user/operator.

## 11. Secrets and credentials

- Do not store secret values in checked-in YAML, generated GoBackup/Restic configs, inventory, command-line arguments, prompt text, or evidence logs.
- Configuration stores references to secrets. A separate secrets service/container is the preferred operating model; Reksicherung resolves references at run time through a `SecretProvider` interface.
- **OpenBao is the first secret provider**, using its KV v2 API. Keep the interface provider-neutral so another lightweight open-source product can be evaluated without changing workflow/config semantics.
- Do not use SOPS for this design.
- Do not treat Consul or another service-discovery product as a secret manager without separately validating its secret access controls, lifecycle, and operational suitability.
- Use least-privilege service identities and retrieve only the references needed for the current run.
- Keep secret values out of logs and tool outputs. If an error includes a credential-bearing value, redact it before recording.
- Determine how the secrets service is bootstrapped, authenticated, backed up, and operated as part of the OpenBao proof of concept. “Easy to run” is a requirement, not an assumption.

The initial reference syntax is `openbao://<mount>/<path>#<key>`. The local test setup uses an ephemeral OpenBao dev container and dummy values only. Production authentication, secret rotation, and whether OpenBao is deployed locally or shared remain operational decisions.

## 12. Checks and result semantics

The result should distinguish:

- **PASS:** Restore completed and all required checks passed.
- **FAIL:** Restore or a required integrity/semantic check failed.
- **INCONCLUSIVE:** The check could not establish a result, for example because a required dependency was unavailable or a configured check was not run.

Minimum initial SQL Server checks:

- Successful restore into the isolated SQL Server target.
- SQL Server database integrity check (including a configured `DBCC CHECKDB` policy).
- AI-assisted semantic review: inspect schema, identify relevant data-bearing tables, and use bounded read-only `SELECT` queries to verify that data exists and looks plausible for the project.

An empty table is not automatically a failure. The AI review reports empty tables as warnings, separately from the required check verdict. Per-database configuration may optionally annotate tables that are expected to be empty; users are not required to enumerate every such table. An empty-table warning alone must not change the overall result to `FAIL`.

The project must not imply that a successful restore alone proves application correctness. Reports should state which levels were tested and which were not. The AI assessment is an explicit semantic-check result, with its evidence retained; the overall workflow aggregates it with restore and database-integrity results rather than accepting unstructured LLM prose as a verdict.

## 13. Evidence and audit record

Preserve the orchestrator JSON run log as the orchestration record, including its step information and token usage when the orchestrator reports it. Reksicherung produces structured domain evidence and links it to the external run ID and relevant step/output identifiers.

The Reksicherung record should include, at minimum:

- Schema version, Reksicherung version, run ID, external run ID if available, start/end timestamps.
- Selected source/provider, backup/repository identifier, and snapshot/backup identifier (without secret values).
- Restore environment/container identifiers and lifecycle mode.
- Ordered steps/actions, timestamps, durations, statuses, and sanitized error details.
- Validation check names, check versions/parameters that are safe to disclose, outcomes, and concise result summaries.
- For AI semantic review: model and prompt/skill version where available, schema context inspected, executed read-only queries, sanitized/aggregated results, and structured assessment. Avoid recording unnecessary row-level business data.
- Final deterministic verdict and reasons.
- Cleanup/retention result and retained-resource cleanup instructions, when applicable.
- References/paths to the related orchestrator JSON log and Reksicherung evidence artifacts.
- Token usage as reported by the orchestrator, when available. Do not infer missing usage or token cost.

Do not include credentials, secret values, full connection strings, or unnecessary production data in evidence. Evidence is an audit trail for review, not a tamper-proof proof system. No signing or anti-modification mechanism is required by the initial scope.

The exact JSON schema, storage location, retention duration, and redaction policy need to be fixed before implementation.

## 14. Security requirements

Security by design is a project-level requirement:

- Fail closed when a required secret, backup reference, or restore target cannot be resolved safely.
- Separate production/source credentials from temporary restore-target credentials.
- Scope destructive operations to resources created by the current run.
- Isolate restore environments from production networks and unrelated runs.
- Minimize permissions, network access, secret exposure time, and persisted data.
- Sanitize tool inputs and outputs; redact secrets and sensitive connection details from logs.
- SQL tools used by AI must be read-only and bounded by query, row/result size, and time limits; do not expose unrestricted shell or database administration tools.
- For local development and initial integration tests, use `mcr.microsoft.com/mssql/server:2022-latest`; define image provenance/update policy and digest pinning before production use.
- Make cleanup behavior visible and retryable without silently claiming success.
- Do not use AI-generated conclusions as the sole basis for a PASS verdict.

The initial design does not require cryptographic evidence signing or encrypted ephemeral disks.

## 15. Reliability and operability

- Every operation has a deadline or bounded timeout.
- Runs are identifiable and idempotency/retry behavior is documented before enabling automatic retries.
- Partial failures preserve enough sanitized evidence to diagnose what happened.
- Cleanup is attempted on normal completion, failure, timeout, and cancellation where the runtime permits it.
- Human-review resources are clearly visible and have an explicit operator cleanup path.
- Logs distinguish workflow failures from evidence-writing and cleanup failures.
- Generated configuration and catalog output are reproducible enough to diff and review.

## 16. Acceptance criteria for the first usable release

1. A user can describe a GoBackup or Restic SQL Server backup source without placing secret values in the repository.
2. Catalog generation validates the configuration and produces an orchestrator-consumable inventory.
3. An operator or orchestrator can run the documented Reksicherung restore-check workflow using the Reksicherung MCP servers and skill (Airon is optional; see `examples/airon/`).
4. A run uses the SQL Server service already provisioned by the operator, restores the selected backup into a run-scoped database, and executes required integrity checks.
5. The workflow returns a deterministic `PASS`, `FAIL`, or `INCONCLUSIVE` result based on tool/check outcomes.
6. Normal mode attempts to drop the run-scoped database; human-review mode retains it and provides cleanup details. Neither mode changes service/container lifecycle.
7. The orchestrator JSON run log and Reksicherung evidence can be correlated by run ID and include the steps, results, cleanup outcome, and available token usage.
8. Secret values do not appear in generated configs, inventory, logs, or evidence.
9. The GoBackup generator is based on the generic infrastructure generator, and the future Restic generator is designed to share the canonical YAML model.
10. SQL Server, GoBackup, Restic, and shared storage responsibilities remain independently testable packages.

## 17. Confirmed implementation baseline

- Product and Go namespace: **Reksicherung**; module path `github.com/1k-off/reksicherung`.
- Reksicherung is orchestrator-agnostic. Optional runtimes such as Airon may drive the restore-check workflow; Reksicherung MCP services provide constrained provider and SQL operations, and the driving agent performs AI-assisted semantic review when configured. Run state and final verdict aggregation are persisted by the configured Reksicherung service, without container lifecycle control.
- First end-to-end path: GoBackup + S3-compatible storage + SQL Server. Add Restic support before the first usable release.
- Docker Compose (or another user-managed service manager) provisions MCP servers and SQL Server; Reksicherung does not manage containers or require configgen-generated backups.
- Normal cleanup drops only the run-scoped database; `hold_for_review` retains that database until an operator explicitly removes it.
- Use secret references and a provider interface. OpenBao is the first provider; test services are operator-started and use dummy secrets.
- Use mocks for unit tests and user-started disposable S3-compatible and SQL Server services for end-to-end tests. Tests must not mutate source backup objects.
- Per-project prompts may describe database scope, required coverage, and order. Normalize those instructions into a validated run plan.
- Empty tables are warnings, not automatic failures; per-database annotations for tables expected to be empty are optional.
- Retry only bounded transient/idempotent operations by default; do not automatically replay an entire restore and AI-check run.
- Keep orchestrator JSON and Reksicherung evidence correlated by run ID. No tamper-proofing or encrypted ephemeral disk requirement.
- The initial `BackupCatalog` schema and secret-free backup inventory are described in `docs/decisions/0001-catalog-config-and-inventory.md`.

## 18. Operational baselines (resolved for production packaging)

- Catalog schema is `reksicherung.1node.xyz/v1alpha2` until a separate `v1` migration ADR.
- Run registry is bbolt on an operator-mounted volume; reconciler default interval is 30s; cleanup is database-scoped only.
- OpenBao is operator-deployed with persistent storage; MCP processes use a token/AppRole valid for process lifetime (no in-process renewal yet). Reference syntax: `openbao://<mount>/<path>#<key>`.
- Images are built from `deploy/docker/`; production must pin digests. SQL Server / MCP / OpenBao isolation and network policy are operator-owned.
- First storage backend is S3-compatible object storage; Restic repositories use the same credential-reference model.
- Backup selection: `latest` or explicit ID in catalog; run plans are validated via `internal/reksicherung/plan`.
- Integrity check: fixed non-repair `DBCC CHECKDB` with package timeouts; semantic review uses bounded read-only SQL MCP tools.
- `hold_for_review` access/TTL is an operator policy (see `docs/operations.md`); evidence contract is in `docs/evidence.md`.
- E2E release gate lives under `test/e2e` (`go:build e2e`) against operator-started services and synthetic fixtures.

Optional Airon-platform notes (MCP `_meta` run-ID injection and durable log handoff) live under `examples/airon/integration-follow-up.md` and do not block compose-based production packaging of Reksicherung itself.
