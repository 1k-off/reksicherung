---
name: reksicherung-bootstrap
description: >-
  Bring up a lab/production restore-check stack from an empty host using only
  documented inputs (docs/inputs.md) and published GHCR images. Do not build
  images unless the operator is a developer iterating on this repo.
---

# Reksicherung bootstrap

Bring up a lab/production restore-check stack from an empty host using only
documented inputs (`docs/inputs.md`) and **published container images**.

**Default images (lab tag `latest`; pin digests for production):**

```text
ghcr.io/1k-off/reksicherung/{mcp-gobackup,mcp-restic,mcp-sqlserver-restore,mcp-sqlserver-inspect,cli}:<TAG>
openbao/openbao:2.1.0
mcr.microsoft.com/mssql/server:2022-latest
```

**Prefer copy-paste commands in**
[`examples/compose/RUNBOOK.md`](../../../examples/compose/RUNBOOK.md)
(Init / Unseal / KV / SQL seed / chmod). Do not invent OpenBao steps.

Do **not** run `docker compose build` for operator bootstrap. Building from
`deploy/docker/` is **developers only** (see below).

Humans without Airon: also [`docs/getting-started.md`](../../../docs/getting-started.md) ·
[`docs/mcp-clients.md`](../../../docs/mcp-clients.md).  
Airon lab pack: `reksicherung-airon-onboard` (same GHCR images, Airon job layout).

## Inputs

- `I-COMPOSE-ENV` — `examples/compose/.env` (bootstrap only)
- `I-OPENBAO-DEFAULTS`, `I-OPENBAO-SQL` — after OpenBao init
- `I-CATALOG` — at least one source (may add later via add-source skill)
- Optional `I-ORCHESTRATOR-JOB` — job YAML for an MCP/skills runner (see
  `examples/airon/` / airon-onboard templates)
- Optional `REKSICHERUNG_IMAGE_TAG` in `.env` (default `latest`)

## Steps

1. Copy `examples/compose/.env.example` → `.env`; set SA password and OpenBao
   addr/token placeholders only. Password charset: avoid `$`, `&`, `%`.
   Optionally set `REKSICHERUNG_IMAGE_TAG` (lab: `latest`).
2. **Pull** GHCR images (do not build):

```bash
TAG="${REKSICHERUNG_IMAGE_TAG:-latest}"
for i in mcp-gobackup mcp-restic mcp-sqlserver-restore mcp-sqlserver-inspect cli; do
  docker pull "ghcr.io/1k-off/reksicherung/${i}:${TAG}"
done
```

3. Follow [`examples/compose/RUNBOOK.md`](../../../examples/compose/RUNBOOK.md):
   start OpenBao → init/unseal → enable KV v2 → start SQL + reconciler →
   `chmod 1777` on `reksicherung_sql-backup` → seed `I-OPENBAO-SQL` (+ optional
   defaults) per `docs/schemas/openbao-source-kv.md`.
4. Create SQL restore login matching OpenBao `USERNAME`/`PASSWORD` (RUNBOOK §7)
   or use lab `sa` with matching OpenBao password.
5. Put least-privilege token into `.env` when leaving lab root-token bring-up.
6. Write thin `I-CATALOG`; validate with GHCR CLI (or local binary if already
   installed) — see RUNBOOK §9. Prefer:

```bash
docker run --rm -v "$PWD:/work:ro" \
  "ghcr.io/1k-off/reksicherung/cli:${TAG:-latest}" \
  validate -config /work/path/to/catalog.yaml
```

7. Attach MCP via [`docs/mcp-clients.md`](../../../docs/mcp-clients.md) (GHCR
   `docker run --rm -i`) and/or optional orchestrator job (`I-ORCHESTRATOR-JOB`).
8. Do not enable schedules until a dry-run via `reksicherung-restore-check`.

## Developers only — build from source

Use only when changing Reksicherung code or Dockerfiles:

```bash
# From repo root; tags images as ghcr.io/1k-off/reksicherung/*:$TAG
export REKSICHERUNG_IMAGE_TAG=local
docker compose -f examples/compose/docker-compose.yml --env-file examples/compose/.env build
```

Operators and AI agents doing client/lab bring-up must **skip** this section.

## Next

- Optional provider YAML: `reksicherung-configgen` (skip if backups already exist)
- Register sources: `reksicherung-add-source`
- Journey map: `reksicherung-index`
- Human path: `docs/getting-started.md`

## Fail closed

- Missing required compose keys or OpenBao fields → stop.
- Per-backup S3 keys in `.env` → remove; use OpenBao sources instead.
- Image pull failure → stop; do not fall back to inventing a local build unless
  the operator explicitly asked for developer mode.
