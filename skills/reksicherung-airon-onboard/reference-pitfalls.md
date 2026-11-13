# Pitfalls (Reksicherung + Airon lab)

Lessons from Windows/Podman lab bring-up. Apply when generating RUNBOOK and
configs.

## MCP stdio

### Prefer `$RUNTIME run`, not `compose run -i`

On Windows, `podman compose` often shells out to `docker-compose.exe`. Stdio
attach breaks → Airon: `connect mcp server … initialize: EOF`.

Use:

```text
$RUNTIME run --rm -i --network <project>_reksicherung \
  -e REKSICHERUNG_OPENBAO_ADDR=http://openbao:8200 \
  -e REKSICHERUNG_OPENBAO_PLAINTEXT=1 \
  -e REKSICHERUNG_OPENBAO_TOKEN=… \
  -e REKSICHERUNG_ARTIFACT_STAGE_DIR=/var/lib/reksicherung/artifacts \
  -v <project>_artifact-stage:/var/lib/reksicherung/artifacts \
  ghcr.io/1k-off/reksicherung/mcp-<name>:latest
```

### `env_passthrough: true`

Airon with `env_passthrough: false` strips most host env. Podman/Docker Desktop
machine connections then fail → same EOF. Keep passthrough **true** for
container-CLI MCP servers.

### Do not `--env-file compose/.env` into MCP

`.env` holds `MSSQL_SA_PASSWORD`. Characters `&` / `%` break Windows env-file
parsing. Pass only needed `-e` keys; expand token from host env in `airon.yaml`.

## Compose / paths

### Literal artifact stage dir

```yaml
REKSICHERUNG_ARTIFACT_STAGE_DIR: /var/lib/reksicherung/artifacts
```

If compose interpolates a host `REKSICHERUNG_ARTIFACT_STAGE_DIR=C:\Users\…`,
MCP exits with staging errors → Airon EOF.

### Host SQL port

Podman user-mode networking on Windows: map `127.0.0.1:11433:1433`. Inside the
compose network always use `sqlserver:1433` (`HOST=sqlserver` in OpenBao).

## Passwords

### SA / generated secrets

Avoid in values written to compose `.env` or unquoted shells:

| Char | Why |
|------|-----|
| `$` | Compose `${VAR}` interpolation |
| `&` `%` | Windows cmd/PowerShell / env-file |

Quote `.env` values if special characters are unavoidable; strip quotes when
seeding OpenBao.

### OpenBao sqlserver must match container SA

Seed `PASSWORD` from the same material as `MSSQL_SA_PASSWORD` **after** first
SQL data-volume init. Changing `.env` later does not change the existing SA
password inside the volume.

## Volumes

### `sql-backup` permissions

Volume is often root-owned after SQL first start. Restore MCP runs as
`65532:65532` and must create temp `.bacpac` there.

```bash
$RUNTIME run --rm -v <project>_sql-backup:/var/opt/mssql/backup alpine \
  chmod 1777 /var/opt/mssql/backup
```

Symptom: `SQL Server restore operation failed` in a few milliseconds after a
successful stage.

## PowerShell

### `-d` swallowed by parameter binding

```powershell
# BAD — -d disappears
Invoke-Compose up -d openbao

# GOOD
Invoke-Compose -ComposeArgs @('up', '-d', 'openbao')
# or
& podman compose … up -d openbao
```

### Token path depends on cwd

From package root: `compose/.env`. From `job/`: `../compose/.env`.

## Airon / skill behavior

### Skill tools have no MCP loop

Airon `skills:` register a **one-shot** LLM call (system = SKILL.md body, user =
`task`). They cannot call `fs_*` or MCP. If the prompt lists the skill in
`tools:`, the model often “delegates” and then FAIL without staging/restore.

Fix: inject procedure with `{{file:job/skills/…/SKILL.md}}` in the prompt
`system:`; put only `fs_*` + `<server>__<tool>` in `prompts.tools`. Set
`workspace.path` to the package root so `{{file:}}` and catalog/coverage work.

### Final line must be `PASS:` or `FAIL:`

With `runtime.outcome: pass_fail`, Airon only scans for lines starting with
`PASS` / `FAIL`. A sole `INCONCLUSIVE:` → `agent outcome missing`. Map
inconclusive cases to `FAIL: inconclusive — …`.

### Record `database_name` ↔ `restored_database`

Restore MCP returns `database: reksicherung_<32hex>` derived from `run_id`.
Always copy that field into the final report and into
`coverage/registry.json` / `coverage/runs/<date>/<source_id>.md`. Never invent
the restored name. Airon tool logs live at package-root `logs/run-*.json`
(when `workspace.path` is `..`); step text at `outputs/<step>.md`.

### Fake `last_checked_at` breaks rotation

Inventing past timestamps (e.g. `2023-…`) re-queues the source as oldest.
Use real current UTC only.

### Restic needs `backupPath=`

Without it the model invents `/path/to/backup/file.bak` → stage fails. Put the
catalog path in `--input`.

### Short `run_id`

`DatabaseNameForRun` rejects ids longer than 128 or with illegal characters.
Use e.g. `rc-cms-1`, not `source_id + full snapshot sha`.

### Provider mismatch

`gobackup_*` on a restic `source_id` fails discovery — continue with restic; do
not treat as hard stack failure.

### GoBackup `latest` OK but `stage_artifact` fails

Opaque `GoBackup artifact staging failed` (and often the same for
`list_archive_candidates`) can happen while `gobackup_latest` / list still
works — S3 list/stat is fine, download/resolve is not.

Treat as **source FAIL**, write evidence + registry (`restored_database: null`),
and let the next sweep rotate to other never-checked ids. Do **not** assume the
whole stack is down. Prefer a known-good restic source for first lab PASS if
needed. Deeper triage (S3 Get, archive contents, size limits) is ops/product —
outside the Airon cron job.

### Paste `mcp_servers` into job YAML

Templates `airon.yaml` / `airon-catalog-sweep.yaml` ship `mcp_servers: []`.
Always paste `templates/airon-mcp-stdio.yaml` and substitute `RUNTIME`,
`COMPOSE_PROJECT`, `IMAGE_TAG`. Empty MCP list → no restore tools.

## Init stuck on “Attaching to …”

`compose up` without `-d` (or `-d` lost in PowerShell) attaches to logs.
Ctrl+C / detach, then use detached up and continue init.
