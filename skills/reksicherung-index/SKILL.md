---
name: reksicherung-index
description: >-
  Map of Reksicherung skills for new operators and AI agents. Use when starting
  a Reksicherung engagement, unsure which skill to run, or needing the full
  path from backup config generation through recurring restore verification.
---

# Reksicherung skill index

Orchestrator-agnostic. Airon is optional (`reksicherung-airon-onboard`).

**Humans first:** [`docs/getting-started.md`](../../docs/getting-started.md) ·
glossary [`docs/glossary.md`](../../docs/glossary.md) ·
compose [`examples/compose/RUNBOOK.md`](../../examples/compose/RUNBOOK.md) ·
MCP host [`docs/mcp-clients.md`](../../docs/mcp-clients.md).

Contracts: `docs/inputs.md` · schemas: `docs/schemas/`.

## Journey (new project)

| Step | Skill / doc | Outcome | Required? |
| --- | --- | --- | --- |
| 0. Path | `docs/getting-started.md` | Choose GHCR+MCP / Airon / source build | Yes |
| 1. Stack | `reksicherung-bootstrap` **or** compose `RUNBOOK` **or** `reksicherung-airon-onboard` | OpenBao, SQL, MCP images | Yes |
| 2. Backup configs | `reksicherung-configgen` + `examples/configgen/` | GoBackup / resticprofile YAML + bindings | **Only if** you need new backup YAML |
| 3. Register source | `reksicherung-add-source` | Catalog row + OpenBao `sources/<id>` (`STORAGE_TYPE` required) | Yes |
| 4. Dry-run check | `reksicherung-restore-check` (+ `docs/mcp-clients.md` if no Airon) | One `source_id` → PASS/FAIL/INCONCLUSIVE | Yes |
| 5. Recurring verify | `reksicherung-schedule-verify` | Inventory + schedule + coverage policy | After dry-run |
| 6. Day-2 | `reksicherung-operate` | Hold cleanup, reconciler, triage | Ongoing |

Lab pack with GHCR (no image build): `reksicherung-airon-onboard`.

## Component → skill

| Component | Skill(s) |
| --- | --- |
| `reksicherung` CLI validate/catalog | add-source, bootstrap, schedule-verify |
| `reksicherung configgen` | configgen (optional) |
| MCP gobackup / restic / sqlserver* | restore-check, mcp-clients |
| OpenBao defaults/sqlserver | bootstrap, compose RUNBOOK |
| OpenBao per-source KV | add-source, configgen (bindings) |
| Evidence / hold_for_review | operate, restore-check |
| Recurring coverage | schedule-verify (+ airon-onboard templates if using Airon) |

## Hard rules (all skills)

1. Only documented inputs in `docs/inputs.md`.
2. No per-backup credentials in compose `.env`.
3. Never invent PASS; fail closed on missing OpenBao / ambiguous archives.
4. Do not manage containers from restore-check / operate triage.
5. Never commit `docs/superpowers/` artifacts.
6. Prefer `docs/getting-started.md` + compose `RUNBOOK` over inventing OpenBao/SQL commands.

## Prompts

| Prompt | Skill |
| --- | --- |
| `docs/agent-prompts/bootstrap.md` | bootstrap |
| `docs/agent-prompts/configgen.md` | configgen |
| `docs/agent-prompts/add-source.md` | add-source |
| `docs/agent-prompts/run-all-latest.md` / `run-context-and-cleanup.md` | restore-check |
| `docs/agent-prompts/schedule-verify.md` | schedule-verify |
| `docs/agent-prompts/airon-onboard.md` | airon-onboard |
