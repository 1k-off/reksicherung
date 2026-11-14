---
name: reksicherung-bootstrap
description: >-
  Bring up a lab/production restore-check stack from an empty host using only
  documented inputs (docs/inputs.md). Prefer build-from-source images; for GHCR
  without building, use docs/getting-started.md or reksicherung-airon-onboard.
---

# Reksicherung bootstrap

Bring up a lab/production restore-check stack from an empty host using only
documented inputs (`docs/inputs.md`).

**Prefer copy-paste commands in**
[`examples/compose/RUNBOOK.md`](../../examples/compose/RUNBOOK.md)
(Init / Unseal / KV / SQL seed / chmod). Do not invent OpenBao steps.

Humans / GHCR without Airon: [`docs/getting-started.md`](../../docs/getting-started.md).
Airon GHCR pack: `reksicherung-airon-onboard`.

## Inputs

- `I-COMPOSE-ENV` — `examples/compose/.env` (bootstrap only)
- `I-OPENBAO-DEFAULTS`, `I-OPENBAO-SQL` — after OpenBao init
- `I-CATALOG` — at least one source (may add later via add-source skill)
- Optional `I-ORCHESTRATOR-JOB` — job YAML for an MCP/skills runner (see
  `examples/airon/` for one binding; GHCR labs use airon-onboard templates)

## Steps

1. Copy `examples/compose/.env.example` → `.env`; set SA password and OpenBao
   addr/token placeholders only. Password charset: avoid `$`, `&`, `%`.
2. Follow [`examples/compose/RUNBOOK.md`](../../examples/compose/RUNBOOK.md):
   start OpenBao → init/unseal → enable KV v2 → start SQL + reconciler →
   `chmod 1777` on `reksicherung_sql-backup` → seed `I-OPENBAO-SQL` (+ optional
   defaults) per `docs/schemas/openbao-source-kv.md`.
3. Create SQL restore login matching OpenBao `USERNAME`/`PASSWORD` (RUNBOOK §7)
   or use lab `sa` with matching OpenBao password.
4. Put least-privilege token into `.env` when leaving lab root-token bring-up.
5. `docker compose … build` for CLI/MCP images (or retag GHCR → local names).
   Host MCP must use loopback OpenBao HTTP or HTTPS for non-loopback.
6. Write thin `I-CATALOG`; run `reksicherung validate` + `reksicherung catalog`.
7. Attach MCP: [`docs/mcp-clients.md`](../../docs/mcp-clients.md) and/or optional
   orchestrator job (`I-ORCHESTRATOR-JOB`).
8. Do not enable schedules until a dry-run via `reksicherung-restore-check`.

## Next

- Optional provider YAML: `reksicherung-configgen` (skip if backups already exist)
- Register sources: `reksicherung-add-source`
- Journey map: `reksicherung-index`
- Human path: `docs/getting-started.md`

## Fail closed

- Missing required compose keys or OpenBao fields → stop.
- Per-backup S3 keys in `.env` → remove; use OpenBao sources instead.
