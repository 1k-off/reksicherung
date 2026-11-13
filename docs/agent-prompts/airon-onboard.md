# Agent prompt: Reksicherung + Airon onboard

Follow `skills/reksicherung-airon-onboard/SKILL.md` and
`skills/reksicherung-airon-onboard/reference-pitfalls.md`.

Scaffold templates from `skills/reksicherung-airon-onboard/templates/` (fictional
example ids only — never copy real client `source_id`s into examples).

## Goal

Scaffold a GHCR-based lab package (compose + catalog + Airon job) and document
first-run commands for the operator’s OS and container runtime (`docker` /
`podman` / `nerdctl`). Prefer RUNBOOK CLI over heavy installers.

Do **not** use `reksicherung-bootstrap` unless the operator wants to build
images from source.

## Must apply (lessons from lab)

- MCP: `$RUNTIME run --rm -i` (not `compose run -i`); `env_passthrough: true`;
  literal `REKSICHERUNG_ARTIFACT_STAGE_DIR`; chmod `1777` on sql-backup.
- Airon skill tools are **one-shot text** (no MCP loop). Inject SKILL.md via
  `{{file:job/skills/…}}`; put only `fs_*` + `<server>__<tool>` in
  `prompts.tools`. `workspace.path` = package root.
- Paste `airon-mcp-stdio.yaml` into both job YAMLs (`mcp_servers: []` is a stub).
- Coverage: agent picks never-checked / oldest via `fs_read` — **no** host
  select/apply scripts. Write `coverage/runs/…` + registry with
  `database_name` ↔ `restored_database`. Final `### restore_map`.
- Outcome line `PASS:` / `FAIL:` only (`INCONCLUSIVE:` → `agent outcome missing`).
- Real UTC `last_checked_at` only (fake past dates break rotation).
- Schedule: support stack (OpenBao + SQL) is separate; cron/Task Scheduler only
  runs Airon (`scripts/run-catalog-sweep.sh` / `Run-CatalogSweep.ps1`).
- GoBackup `latest` OK + `stage_artifact` fail → mark source FAIL, update
  registry, continue — not a dead stack by default.
