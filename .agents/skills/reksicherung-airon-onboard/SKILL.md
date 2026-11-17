---
name: reksicherung-airon-onboard
description: >-
  Scaffold and first-run a Reksicherung + Airon lab package (GHCR images, OpenBao,
  SQL Server and/or opt-in MySQL restore targets, stdio MCP). Runtime-agnostic (docker, podman, nerdctl/cri-compatible
  compose) on Windows, Linux, and macOS. Use when onboarding a client/lab for Airon
  restore-checks, generating RUNBOOK/compose/job files, or replacing fragile
  compose-run MCP with `$RUNTIME run`. Distinct from reksicherung-bootstrap
  (compose stack + any MCP host, same GHCR images, no Airon packaging).
---

# Reksicherung + Airon onboard

Create a **self-contained operator package** and bring it to first successful
`airon run` restore-check. Prefer **documented CLI commands in RUNBOOK.md**;
optional thin scripts are fine. Do **not** require image builds — use GHCR.

Related: stack without Airon — `reksicherung-bootstrap` + `docs/getting-started.md` +
`docs/mcp-clients.md` (same GHCR images). Also: `reksicherung-add-source`,
`reksicherung-restore-check`, `reksicherung-operate`.

## Inputs (ask if missing)

| Input | Notes |
|-------|--------|
| Package path | Where to write the pack (e.g. `tmp/reksicherung-airon`) |
| `COMPOSE_PROJECT` | Compose `name:` → network/volume prefix (e.g. `reksicherung-airon`) |
| Runtime | `docker` \| `podman` \| `nerdctl` (or other Compose-compatible CLI) |
| OS shell | PowerShell (Windows) vs bash/zsh (Linux/macOS) |
| Catalog | At least one `source_id`; restic needs `backupPath` |
| Database engine | `sqlserver` (default) and/or `mysql` per catalog `database.type`; MySQL uses the opt-in compose profile `mysql` |
| Secrets | S3/restic per source + SQL SA (and MySQL restricted account when used) → OpenBao (never long-lived in `.env`) |
| LLM | `OPENAI_API_KEY` or Anthropic + provider swap in `job/airon.yaml` |
| `airon` | On `PATH` or documented binary path |

## Detect runtime

```text
RUNTIME = first available of: podman, docker, nerdctl
COMPOSE = "$RUNTIME compose"   # or docker-compose / podman-compose if needed
```

Record `RUNTIME`, `COMPOSE_PROJECT`, host SQL port (default **11433** on Windows
user-mode networking; **1433** often OK on Linux/macOS bridge). MySQL (opt-in
profile `mysql`) uses host `13306` → `mysql:3306` only for admin seeding.

Fail closed if no container CLI can pull/run and compose-up.

## Package layout (create)

```text
<package>/
├── RUNBOOK.md
├── README.md
├── .gitignore                   # from templates/gitignore.example
├── catalog/catalog.yaml
├── coverage/
│   ├── POLICY.md
│   ├── registry.example.json
│   ├── registry.json            # local journal (gitignored)
│   └── runs/YYYY-MM-DD/*.md     # per-check evidence (gitignored)
├── logs/run-*.json              # Airon tool logs (workspace = package root)
├── outputs/                     # save_outputs step text
├── compose/                     # GHCR stack + OpenBao
├── scripts/                     # Init / Start / Seed / Pull + run-catalog-sweep.*
                                 # (no coverage pick/merge scripts; stack ≠ cron)
└── job/
    ├── airon.yaml
    ├── airon-catalog-sweep.yaml # workspace.path = package root for fs_*
    ├── prompts/
    │   ├── restore-one.md
    │   └── catalog-sweep-daily.md
    ├── skills/
    │   ├── reksicherung-restore-one/
    │   └── reksicherung-catalog-sweep/
    └── workspace/.gitkeep
```

Optional thin wrappers OK; RUNBOOK CLI remains authoritative.

Copy compose/OpenBao from `examples/compose/`; MCP images
`ghcr.io/1k-off/reksicherung/*`. Scaffold from
`.agents/skills/reksicherung-airon-onboard/templates/` (fictional ids only — never copy
real client `source_id`s into examples).

Coverage: agent selects via `fs_read` catalog + registry — **no** host
pick/merge scripts. Record `database_name` ↔ `restored_database` in registry and
`coverage/runs/…`.

Templates: [templates/](templates/) · Pitfalls: [reference-pitfalls.md](reference-pitfalls.md).

## Hardening checklist (must apply)

- [ ] Compose: `REKSICHERUNG_ARTIFACT_STAGE_DIR: /var/lib/reksicherung/artifacts`
      **literal** (never `${...}` from host — Windows paths break staging).
- [ ] `job/airon.yaml`: MCP `command: <RUNTIME>` + `run --rm -i` on project
      network/volumes; **not** `compose run -i` (stdio EOF on Windows + external
      docker-compose provider).
- [ ] `env_passthrough: true` for every MCP server (runtime needs host env on
      Windows/macOS machines).
- [ ] Token via `${REKSICHERUNG_OPENBAO_TOKEN}` in `-e` args; do **not** pass
      whole `compose/.env` as `--env-file` to MCP (SA passwords with `&`/`%`).
- [ ] SA password charset: no `$` (compose interpolation), no `&` / `%` (Windows).
- [ ] After SQL volume create: `chmod 1777` on `sql-backup` so uid `65532` can
      stage files (else restore fails in milliseconds).
- [ ] PowerShell helpers: pass compose flags as **arrays**
      (`@('up','-d',...)`) — bare `up -d` drops `-d`.
- [ ] Restic Airon input must include `backupPath=` from catalog; short `run_id`
      (≤128, `[A-Za-z0-9._-]`).
- [ ] Paste `templates/airon-mcp-stdio.yaml` into `job/airon.yaml` and
      `job/airon-catalog-sweep.yaml` (`mcp_servers: []` is a stub — replace it).
- [ ] Airon skill tools are **one-shot text** (no MCP tool loop). Inject
      `SKILL.md` via `{{file:…}}` into prompt `system:`; put only `fs_*` +
      `server__tool` in `prompts.tools`. `workspace.path` = package root.
- [ ] Ship `scripts/run-catalog-sweep.sh` + `scripts/Run-CatalogSweep.ps1` for
      cron / Task Scheduler (Airon only; stack managed separately).
- [ ] Coverage evidence: `coverage/runs/…` + registry fields `database_name`,
      `restored_database`, `last_report`; final `### restore_map` block.
- [ ] Final outcome line `PASS:` / `FAIL:` only (not `INCONCLUSIVE:`).
- [ ] `.gitignore`: `.env`, `openbao-init.json`, seed scripts, `coverage/registry.json`,
      `coverage/runs/`, `logs/`, `outputs/`, workspace.

## Operator sequence (write into RUNBOOK)

Use `$RUNTIME` / `$COMPOSE` below; show both bash and PowerShell blocks when the
target OS is unknown.

1. **Pull** GHCR images (mcp-gobackup, mcp-restic, mcp-sqlserver-restore,
   mcp-sqlserver-inspect, cli; plus mcp-mysql-restore, mcp-mysql-inspect for
   `database.type: mysql`) + `openbao/openbao:2.1.0` +
   `mcr.microsoft.com/mssql/server:2022-latest` (and `mysql:8.4` for MySQL).
2. **Init OpenBao** — `compose up -d openbao` → `bao operator init` (save
   `openbao-init.json`) → unseal → enable `secret/` kv-v2 → write token into
   `compose/.env` as `REKSICHERUNG_OPENBAO_TOKEN`.
3. **Start stack** — openbao + sqlserver + cleanup-reconciler; then chmod
   sql-backup volume.
   **If any catalog source has `database.type: mysql`** (MySQL is opt-in, compose
   profile `mysql`; plain `up` never starts it): set `MYSQL_ROOT_PASSWORD` in
   `compose/.env`, then `<COMPOSE> --env-file .env --profile mysql up -d mysql`;
   wait healthy; create the restricted account
   (grants only on schema pattern `reksicherung\_%` — never `FILE`/`SUPER`/other
   schemas; see `examples/compose/RUNBOOK.md` §6b). SQL Server-only packs skip
   this and drop the mysql services from the package compose.
4. **Seed OpenBao** — `secret/reksicherung/defaults`, `.../sqlserver`
   (`HOST=sqlserver`, `USERNAME=sa`, `PASSWORD=<same as MSSQL_SA_PASSWORD>`),
   and `.../sources/<source_id>` per catalog (see
   `docs/schemas/openbao-source-kv.md`). **MySQL sources:** also seed
   `.../mysql` (`HOST=mysql`, `PORT=3306`, `USERNAME`/`PASSWORD` = the restricted
   account from step 3, not root) and keep the `mysql_restore` / `mysql_inspect`
   servers + tools from the templates; otherwise drop them.
5. **Validate catalog** (optional) — cli profile `validate` / `catalog`.
6. **Airon run** — export LLM key + `REKSICHERUNG_OPENBAO_TOKEN`; `cd job`;
   `airon run --config ./airon.yaml --no-healthchecks --input "..."`.

Primary run command style (PowerShell, from package root):

```powershell
$env:REKSICHERUNG_OPENBAO_TOKEN = (Get-Content .\compose\.env |
  Where-Object { $_ -match '^REKSICHERUNG_OPENBAO_TOKEN=' } |
  ForEach-Object { ($_ -split '=',2)[1].Trim().Trim('"') } | Select-Object -First 1)
cd job
airon run --config .\airon.yaml --no-healthchecks `
  --input "source_id=<id>`nprovider=<gobackup|restic>`nbackupPath=<if restic>`ncleanup=hold_for_review"
```

From `job/`, token file is `../compose/.env`.

## Volume / network names

Compose `name: <COMPOSE_PROJECT>` yields:

- network: `<COMPOSE_PROJECT>_reksicherung`
- volumes: `<COMPOSE_PROJECT>_artifact-stage`, `_sql-backup`, `_sql-data`, …

Substitute these into `job/airon.yaml` MCP args (see `templates/airon-mcp-stdio.yaml`).

## Fail closed

| Symptom | Action |
|---------|--------|
| `initialize: EOF` on MCP | Confirm `$RUNTIME run` (not compose run), `env_passthrough: true`, token set, no host Windows staging path |
| `SQL Server restore operation failed` in ~ms | chmod sql-backup 1777; confirm OpenBao sqlserver password matches SA |
| MySQL restore/inspect connect error | `--profile mysql` service up? OpenBao `secret/reksicherung/mysql` = restricted account (grants on `reksicherung\_%`), `HOST=mysql` |
| Restic stage invents `/path/to/...` | Require `backupPath=` in input; tighten skill/prompt |
| GoBackup discovery fails on restic source | Expected; continue with restic |
| Agent calls skill tool, no MCP | Remove skill from `prompts.tools`; inject via `{{file:}}` |
| `agent outcome missing` | Final line must be `PASS:` / `FAIL:` (not `INCONCLUSIVE:`) |
| GoBackup latest OK, stage fails | Source FAIL + registry; rotate next day — not auto “stack dead” |
| Empty MCP tool list | Paste `airon-mcp-stdio.yaml` into job YAML |
| Missing `${REKSICHERUNG_OPENBAO_TOKEN}` | Stop; load from compose/.env before airon |
| Secrets in catalog or committed `.env` | Remove; use OpenBao only |
| No DB name map in report | Require `restored_database` from MCP + `coverage/runs/…` |

## Done when

- [ ] Package files exist and RUNBOOK matches detected `RUNTIME`/OS
- [ ] OpenBao unsealed; KV seeded; SQL Server up (and, for MySQL sources: `mysql`
      profile service healthy, restricted account created, `secret/reksicherung/mysql` seeded)
- [ ] `airon check --config job/airon.yaml` OK (aside from optional notify URL)
- [ ] One restore-check reaches restore (or documented PASS/FAIL with real MCP errors)
- [ ] Coverage path writes `coverage/runs/…` + registry map when used
- [ ] No secrets committed

## Do not

- Build images unless the operator is a developer iterating on this repo
  (see bootstrap “Developers only”). Prefer GHCR for all lab/operator bring-up.
- Use `compose run -i` for Airon MCP on Windows.
- Put per-source S3/restic keys in compose `.env`.
- Invent catalog ids, backup paths, restored DB names, or PASS verdicts.
- Ship host scripts that pick coverage `source_id`s or merge registry.
