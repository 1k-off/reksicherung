# Reksicherung input contracts (AI registry)

Every operator- or agent-supplied artifact is listed here. If an input is not in
this registry, it is not part of the supported surface.

Machine-readable schemas: [`schemas/`](schemas/) · index: [`schemas/README.md`](schemas/README.md).

| ID | Artifact | Writer | Schema | Validate | Skill |
| --- | --- | --- | --- | --- | --- |
| `I-CATALOG` | `BackupCatalog` YAML | AI/operator | [`backup-catalog.v1alpha2.schema.json`](schemas/backup-catalog.v1alpha2.schema.json) | `reksicherung validate -config <file>` | `reksicherung-add-source`, `reksicherung-bootstrap`, `reksicherung-airon-onboard` |
| `I-INVENTORY` | `BackupInventory` JSON | CLI | [`backup-inventory.v1alpha2.schema.json`](schemas/backup-inventory.v1alpha2.schema.json) | `reksicherung catalog -config <file>` | `reksicherung-restore-check` |
| `I-OPENBAO-DEFAULTS` | KV `secret/reksicherung/defaults` | AI/operator | [`openbao-defaults.schema.json`](schemas/openbao-defaults.schema.json) | skill checklist / `bao kv get` | `reksicherung-bootstrap`, `reksicherung-airon-onboard` |
| `I-OPENBAO-SOURCE` | KV `secret/reksicherung/sources/<id>` | AI/operator | [`openbao-source.schema.json`](schemas/openbao-source.schema.json) | skill checklist | `reksicherung-add-source` |
| `I-OPENBAO-SQL` | KV `secret/reksicherung/sqlserver` | AI/operator | [`openbao-sqlserver.schema.json`](schemas/openbao-sqlserver.schema.json) | skill checklist | `reksicherung-bootstrap`, `reksicherung-airon-onboard` |
| `I-COMPOSE-ENV` | compose `.env` (bootstrap only) | AI/operator | [`compose-env.schema.json`](schemas/compose-env.schema.json) | compose config / skill | `reksicherung-bootstrap`, `reksicherung-airon-onboard` |
| `I-ORCHESTRATOR-JOB` | optional job YAML (MCP + skills) | AI/operator | [`orchestrator-job.schema.json`](schemas/orchestrator-job.schema.json) | orchestrator-specific check | `reksicherung-bootstrap`, `reksicherung-airon-onboard` |
| `I-PROMPT` | restore-check request text | AI/operator | [`restore-check-prompt.md`](schemas/restore-check-prompt.md) | skill review | `reksicherung-restore-check` |
| `I-MCP-TOOLS` | MCP tool args/results | product | [`mcp-tools.schema.json`](schemas/mcp-tools.schema.json) | MCP SDK / tests | all run skills |
| `I-EVIDENCE` | evidence JSONL events | product | [`evidence-event.v1.schema.json`](schemas/evidence-event.v1.schema.json) | runtime Validate | `reksicherung-operate` |

Airon is **not** required. `examples/airon/` is one optional binding of
`I-ORCHESTRATOR-JOB`. Operators may drive MCP tools manually or via any other
automation that speaks MCP + skills.

## Rules for agents

1. Create or edit inputs only against the linked schema and fixtures.
2. Do not invent environment variables outside `I-COMPOSE-ENV`.
3. Per-database S3/Restic coordinates live in OpenBao (`I-OPENBAO-SOURCE`), never in compose `.env`.
4. SQL connection (`HOST`, `USERNAME`, `PASSWORD`; optional `PORT`) lives in OpenBao (`I-OPENBAO-SQL`), never as ADDRESS/DSN/password-ref env vars.
5. After changing `I-CATALOG` or OpenBao source KV, run `reksicherung validate` and emit inventory before scheduling a restore-check run.
6. Fail closed: missing required OpenBao fields or schema violations abort setup/run.
7. Do not hard-code Airon (or any other orchestrator) into Reksicherung core contracts.
8. For GoBackup archives with multiple `.bak`/`.bacpac` members, use MCP
   `ambiguous_archive` / `gobackup_list_archive_candidates` plus optional
   `I-PROMPT` slot `archive_member`; never invent `member_path` when hints are
   inconclusive.

## Group / version

Catalog and inventory: `reksicherung.1node.xyz/v1alpha2`.
