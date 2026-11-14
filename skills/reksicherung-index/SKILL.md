---
name: reksicherung-index
description: >-
  Map of Reksicherung skills for new operators and AI agents. Use when starting
  a Reksicherung engagement, unsure which skill to run, or needing the full
  path from backup config generation through recurring restore verification.
---

# Reksicherung skill index

Orchestrator-agnostic. Airon is optional (`reksicherung-airon-onboard`).

Contracts: `docs/inputs.md` · schemas: `docs/schemas/`.

## Journey (new project)

| Step | Skill | Outcome |
| --- | --- | --- |
| 1. Stack | `reksicherung-bootstrap` | Compose, OpenBao, SQL restore target, MCP binaries/images |
| 2. Backup configs | `reksicherung-configgen` | GoBackup / resticprofile YAML + secret bindings (`I-CONFIGGEN`) |
| 3. Register source | `reksicherung-add-source` | Catalog row + OpenBao `sources/<id>` (`STORAGE_TYPE` required) |
| 4. Dry-run check | `reksicherung-restore-check` | One `source_id` → PASS/FAIL/INCONCLUSIVE |
| 5. Recurring verify | `reksicherung-schedule-verify` | Inventory + schedule + coverage policy |
| 6. Day-2 | `reksicherung-operate` | Hold cleanup, reconciler, triage |

Lab pack with GHCR (no image build): `reksicherung-airon-onboard`.

## Component → skill

| Component | Skill(s) |
| --- | --- |
| `reksicherung` CLI validate/catalog | add-source, bootstrap, schedule-verify |
| `reksicherung configgen` | configgen |
| MCP gobackup / restic / sqlserver* | restore-check |
| OpenBao defaults/sqlserver | bootstrap |
| OpenBao per-source KV | add-source, configgen (bindings) |
| Evidence / hold_for_review | operate, restore-check |
| Recurring coverage | schedule-verify (+ airon-onboard templates if using Airon) |

## Hard rules (all skills)

1. Only documented inputs in `docs/inputs.md`.
2. No per-backup credentials in compose `.env`.
3. Never invent PASS; fail closed on missing OpenBao / ambiguous archives.
4. Do not manage containers from restore-check / operate triage.
5. Never commit `docs/superpowers/` artifacts.

## Prompts

| Prompt | Skill |
| --- | --- |
| `docs/agent-prompts/bootstrap.md` | bootstrap |
| `docs/agent-prompts/configgen.md` | configgen |
| `docs/agent-prompts/add-source.md` | add-source |
| `docs/agent-prompts/run-all-latest.md` / `run-context-and-cleanup.md` | restore-check |
| `docs/agent-prompts/schedule-verify.md` | schedule-verify |
| `docs/agent-prompts/airon-onboard.md` | airon-onboard |
