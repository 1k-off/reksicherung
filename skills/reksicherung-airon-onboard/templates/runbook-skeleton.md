# Runbook: Reksicherung + Airon

Package root: `<PACKAGE_PATH>`  
Runtime: `<RUNTIME>` (`<COMPOSE>` = `<RUNTIME> compose`)  
Compose project: `<COMPOSE_PROJECT>`  
Host SQL port: `<SQL_HOST_PORT>` (container always `sqlserver:1433`)

Stack is download/restore-check only. Mutations only against `reksicherung_*` DBs.

```
[operator]
    │
    ▼
 airon CLI  ──stdio──►  <RUNTIME> run *‑mcp (GHCR images)
    │
    ▼
 logs/run-*.json          ← Airon tool-call log (package root)
 outputs/<step>.md        ← final model text
 coverage/runs/…/*.md     ← per-source evidence + DB name map
 coverage/registry.json   ← journal (database_name ↔ restored_database)
```

## Layout

```text
<PACKAGE_PATH>/
├── RUNBOOK.md
├── catalog/catalog.yaml
├── coverage/
│   ├── POLICY.md
│   ├── registry.example.json
│   ├── registry.json
│   └── runs/YYYY-MM-DD/*.md
├── logs/run-*.json
├── outputs/
├── compose/
├── scripts/                 # Init / Start / Seed / Pull only
└── job/
    ├── airon.yaml
    ├── airon-catalog-sweep.yaml
    ├── prompts/
    └── skills/
        ├── reksicherung-restore-one/
        └── reksicherung-catalog-sweep/
```

Secrets: OpenBao KV, not catalog. Example ids in prompts are fictional
(`acme-…`, `contoso-…`).

## Prerequisites

- `<RUNTIME>` + compose provider
- `airon` on PATH (or document binary)
- LLM API key
- Disk for large bacpac/bak
- Network: GHCR pull; MCP → object storage

Images: `ghcr.io/1k-off/reksicherung/{mcp-gobackup,mcp-restic,mcp-sqlserver-restore,mcp-sqlserver-inspect,cli}:<TAG>`

## Setup

### Pull

```bash
cd <PACKAGE_PATH>/compose
# pull each image with <RUNTIME> pull …
```

### Init OpenBao

```bash
cd <PACKAGE_PATH>/compose
cp -n .env.example .env   # set MSSQL_SA_PASSWORD (no $ & %)
<COMPOSE> --env-file .env up -d openbao
# wait until process listens on 8200
<COMPOSE> --env-file .env exec -T openbao bao operator init \
  -key-shares=1 -key-threshold=1 -format=json > openbao-init.json
# unseal with unseal_keys_b64[0]; enable KV v2 at secret/
# write root_token into .env as REKSICHERUNG_OPENBAO_TOKEN=
```

Keep `openbao-init.json` secret; gitignore it.

### Start SQL + reconciler

```bash
<COMPOSE> --env-file .env up -d openbao sqlserver cleanup-reconciler
<RUNTIME> run --rm -v <COMPOSE_PROJECT>_sql-backup:/var/opt/mssql/backup alpine \
  chmod 1777 /var/opt/mssql/backup
```

### Seed OpenBao

Write `secret/reksicherung/defaults`, `secret/reksicherung/sqlserver`
(`HOST=sqlserver`, `USERNAME=sa`, `PASSWORD=<MSSQL_SA_PASSWORD>`), and per-source
KV. Schema: `docs/schemas/openbao-source-kv.md`.

### Optional catalog validate

```bash
<COMPOSE> --env-file .env --profile cli run --rm cli \
  validate -config /catalog/catalog.yaml
```

## Run one restore-check (Airon)

```bash
export OPENAI_API_KEY=…
export REKSICHERUNG_OPENBAO_TOKEN=…   # from compose/.env
cd <PACKAGE_PATH>/job
airon run --config ./airon.yaml --no-healthchecks \
  --input "source_id=…"$'\n'"provider=…"$'\n'"backupPath=…"$'\n'"cleanup=hold_for_review"
```

```powershell
$env:OPENAI_API_KEY = '…'
$env:REKSICHERUNG_OPENBAO_TOKEN = (Get-Content ..\compose\.env |  # if cwd=job
  Where-Object { $_ -match '^REKSICHERUNG_OPENBAO_TOKEN=' } |
  ForEach-Object { ($_ -split '=',2)[1].Trim().Trim('"') } | Select-Object -First 1)
airon run --config .\airon.yaml --no-healthchecks `
  --input "source_id=…`nprovider=…`nbackupPath=…`ncleanup=hold_for_review"
```

Restic: `backupPath` required from catalog. Gobackup: `source_id` (+ optional
`archive_member`). Final line must include
`database_name=… restored_database=reksicherung_… run_id=…`.

MCP is started per tool as `<RUNTIME> run --rm -i …` (see `job/airon.yaml`).
Do **not** list skill names in `prompts.tools` — inject SKILL.md via `{{file:}}`.

## Daily catalog coverage (example: ≥60 of ~100 / month → 2/day)

Policy: `coverage/POLICY.md`. Journal: `coverage/registry.json`.

The agent selects the batch (`fs_read` catalog + registry → never-checked /
oldest → restore-check → `coverage/runs/…` + registry). No host pick scripts.

```bash
cd job
airon run --config ./airon-catalog-sweep.yaml --no-healthchecks \
  --input "cleanup=delete
daily_batch=2
oldest_window=10"
```

```powershell
cd job
airon run --config .\airon-catalog-sweep.yaml --no-healthchecks `
  --input "cleanup=delete`ndaily_batch=2`noldest_window=10"
```

`workspace.path` must be package root. Expect `### restore_map` and
`coverage/runs/<date>/<source_id>.md` with `database_name` ↔ `restored_database`.

## Schedule (Task Scheduler / cron)

Assumption: **OpenBao + SQL Server (+ reconciler) run separately** (compose /
systemd / ops). Cron / Task Scheduler only starts **Airon** with the catalog-
sweep prompt — it does not start or unseal the support stack.

Preconditions (ops): stack healthy, OpenBao unsealed, token in `compose/.env`,
container CLI + `airon` on `PATH`, `OPENAI_API_KEY` for the scheduled user.

### Linux — cron

```bash
chmod +x <PACKAGE_PATH>/scripts/run-catalog-sweep.sh
```

```cron
OPENAI_API_KEY=sk-…
PATH=/usr/local/bin:/usr/bin
0 6 * * * <PACKAGE_PATH>/scripts/run-catalog-sweep.sh >><PACKAGE_PATH>/logs/cron-sweep.log 2>&1
```

### Windows — Task Scheduler

```powershell
# powershell.exe -File <PACKAGE_PATH>\scripts\Run-CatalogSweep.ps1
# Daily 06:00; OPENAI_API_KEY in User env or task XML
```

Outputs: `logs/run-*.json`, `outputs/catalog_sweep.md`, `coverage/runs/…`,
`coverage/registry.json`.

## Day-2

- After reboot: unseal OpenBao; `up -d` stack; re-chmod sql-backup if volume recreated.
- Triage: `logs/run-*.json` tool errors; `coverage/runs/…` for DB name map;
  see `reference-pitfalls.md`.
- Month-end: count distinct `last_checked_at` in month vs `monthly_min`.

## Tear down

```bash
cd compose
<COMPOSE> --env-file .env --profile mcp-stdio --profile cli down
# add -v only when wiping OpenBao + SQL data intentionally
```
