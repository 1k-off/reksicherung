# Drive Reksicherung MCP without Airon

Use any MCP-capable host (Cursor, Claude Desktop, or another stdio client) after
the compose stack is up ([`examples/compose/RUNBOOK.md`](../examples/compose/RUNBOOK.md)).

Reksicherung does **not** start containers from restore-check skills. The host
launches each MCP binary (usually `docker`/`podman run --rm -i`).

**Images (GHCR):**

```text
ghcr.io/1k-off/reksicherung/mcp-gobackup:<TAG>
ghcr.io/1k-off/reksicherung/mcp-restic:<TAG>
ghcr.io/1k-off/reksicherung/mcp-sqlserver-restore:<TAG>
ghcr.io/1k-off/reksicherung/mcp-sqlserver-inspect:<TAG>
ghcr.io/1k-off/reksicherung/mcp-mysql-restore:<TAG>
ghcr.io/1k-off/reksicherung/mcp-mysql-inspect:<TAG>
ghcr.io/1k-off/reksicherung/cli:<TAG>
```

Recommended lab tag until you pin digests for production: `latest`.
Compose project in [`examples/compose`](../examples/compose/) is named
`reksicherung` → network `reksicherung_reksicherung`, volumes
`reksicherung_artifact-stage` and `reksicherung_sql-backup`.

## Host environment

Export before starting the MCP host (values from `examples/compose/.env`):

```bash
export REKSICHERUNG_OPENBAO_TOKEN=…   # required; expand into container -e
# Do not pass the whole compose .env as --env-file (SA passwords break on &/%).
```

Optional local CLI against loopback OpenBao:

```bash
export REKSICHERUNG_OPENBAO_ADDR=http://127.0.0.1:8200
export REKSICHERUNG_OPENBAO_TOKEN=…
```

Inside the compose network MCP containers use `http://openbao:8200` and
`REKSICHERUNG_OPENBAO_PLAINTEXT=1` (lab only).

## Cursor / Claude Desktop style `mcp.json`

Replace `TAG` and ensure Docker/Podman can reach the compose network. On Windows
prefer the same runtime you used for compose (`docker` or `podman`).

```json
{
  "mcpServers": {
    "reksicherung-gobackup": {
      "command": "docker",
      "args": [
        "run", "--rm", "-i",
        "--network", "reksicherung_reksicherung",
        "-e", "REKSICHERUNG_OPENBAO_ADDR=http://openbao:8200",
        "-e", "REKSICHERUNG_OPENBAO_PLAINTEXT=1",
        "-e", "REKSICHERUNG_OPENBAO_TOKEN",
        "-e", "REKSICHERUNG_ARTIFACT_STAGE_DIR=/var/lib/reksicherung/artifacts",
        "-v", "reksicherung_artifact-stage:/var/lib/reksicherung/artifacts",
        "ghcr.io/1k-off/reksicherung/mcp-gobackup:latest"
      ]
    },
    "reksicherung-restic": {
      "command": "docker",
      "args": [
        "run", "--rm", "-i",
        "--network", "reksicherung_reksicherung",
        "-e", "REKSICHERUNG_OPENBAO_ADDR=http://openbao:8200",
        "-e", "REKSICHERUNG_OPENBAO_PLAINTEXT=1",
        "-e", "REKSICHERUNG_OPENBAO_TOKEN",
        "-e", "REKSICHERUNG_ARTIFACT_STAGE_DIR=/var/lib/reksicherung/artifacts",
        "-v", "reksicherung_artifact-stage:/var/lib/reksicherung/artifacts",
        "ghcr.io/1k-off/reksicherung/mcp-restic:latest"
      ]
    },
    "reksicherung-sqlserver-restore": {
      "command": "docker",
      "args": [
        "run", "--rm", "-i",
        "--network", "reksicherung_reksicherung",
        "-e", "REKSICHERUNG_OPENBAO_ADDR=http://openbao:8200",
        "-e", "REKSICHERUNG_OPENBAO_PLAINTEXT=1",
        "-e", "REKSICHERUNG_OPENBAO_TOKEN",
        "-e", "REKSICHERUNG_ARTIFACT_STAGE_DIR=/var/lib/reksicherung/artifacts",
        "-e", "REKSICHERUNG_SQLSERVER_BACKUP_DIR=/var/opt/mssql/backup",
        "-e", "REKSICHERUNG_SQLSERVER_DATA_DIR=/var/opt/mssql/data",
        "-v", "reksicherung_artifact-stage:/var/lib/reksicherung/artifacts",
        "-v", "reksicherung_sql-backup:/var/opt/mssql/backup",
        "ghcr.io/1k-off/reksicherung/mcp-sqlserver-restore:latest"
      ]
    },
    "reksicherung-sqlserver-inspect": {
      "command": "docker",
      "args": [
        "run", "--rm", "-i",
        "--network", "reksicherung_reksicherung",
        "-e", "REKSICHERUNG_OPENBAO_ADDR=http://openbao:8200",
        "-e", "REKSICHERUNG_OPENBAO_PLAINTEXT=1",
        "-e", "REKSICHERUNG_OPENBAO_TOKEN",
        "-e", "REKSICHERUNG_SQLSERVER_DATABASE=master",
        "ghcr.io/1k-off/reksicherung/mcp-sqlserver-inspect:latest"
      ]
    },
    "reksicherung-mysql-restore": {
      "command": "docker",
      "args": [
        "run", "--rm", "-i",
        "--network", "reksicherung_reksicherung",
        "-e", "REKSICHERUNG_OPENBAO_ADDR=http://openbao:8200",
        "-e", "REKSICHERUNG_OPENBAO_PLAINTEXT=1",
        "-e", "REKSICHERUNG_OPENBAO_TOKEN",
        "-e", "REKSICHERUNG_ARTIFACT_STAGE_DIR=/var/lib/reksicherung/artifacts",
        "-v", "reksicherung_artifact-stage:/var/lib/reksicherung/artifacts",
        "ghcr.io/1k-off/reksicherung/mcp-mysql-restore:latest"
      ]
    },
    "reksicherung-mysql-inspect": {
      "command": "docker",
      "args": [
        "run", "--rm", "-i",
        "--network", "reksicherung_reksicherung",
        "-e", "REKSICHERUNG_OPENBAO_ADDR=http://openbao:8200",
        "-e", "REKSICHERUNG_OPENBAO_PLAINTEXT=1",
        "-e", "REKSICHERUNG_OPENBAO_TOKEN",
        "ghcr.io/1k-off/reksicherung/mcp-mysql-inspect:latest"
      ]
    }
  }
}
```

The two `reksicherung-mysql-*` entries are only needed for `database.type: mysql`
sources (OpenBao `secret/reksicherung/mysql`; no `sql-backup` volume). Leave
`REKSICHERUNG_MYSQL_DATABASE` unset on inspect and pass the restore `run_id`.

The host must expose `REKSICHERUNG_OPENBAO_TOKEN` in its environment so Docker
forwards it via `-e REKSICHERUNG_OPENBAO_TOKEN` (value from the environment).

### Local binaries instead of GHCR

Build with `go build` / compose `build`, then point `command` at
`reksicherung-mcp-gobackup` (etc.) with the same OpenBao env. Shared staging
directories must still exist on paths the restore target can see — see
`cmd/reksicherung-mcp-sqlserver-restore/README.md`.

## Tool sequence (one source)

Follow skill [`.agents/skills/reksicherung-restore-check/SKILL.md`](../.agents/skills/reksicherung-restore-check/SKILL.md).
Summary for a GoBackup `latest` source:

1. `gobackup_latest` / `gobackup_list` with `source_id`
2. `gobackup_stage_artifact` → keep `artifact_id` + `kind`
3. On `ambiguous_archive`: `gobackup_list_archive_candidates`, pick one
   `member_path`, retry stage (fail closed if still ambiguous)
4. `sqlserver_restore_backup` with `run_id`, `artifact_id`, `source_database`
5. `sqlserver_check_restore_integrity`
6. Optional semantic review via inspect tools with the **same** `run_id`
7. Verdict `PASS` / `FAIL` / `INCONCLUSIVE`; cleanup drop or hold

Restic: list/select snapshot → `restic_stage_artifact` with catalog `backupPath`.

### MySQL sources (`database.type: mysql`)

Steps 1–3 are identical (staged `kind` is `sql`). Then:

1. `mysql_restore_backup` with `run_id`, `artifact_id`, `source_database`
2. `mysql_check_restore_integrity` — **`command_completed` ≠ clean**; it only
   means `mysqlcheck` exited without error
3. Optional `mysql_list_tables` / `mysql_describe_table` / `mysql_validate_table`
   with the **same** `run_id`; remap `expectedEmptyTables`
   `source_database.table` to the table in the run database
4. Verdict, then `mysql_drop_restore_database` (`delete`) or hold

Prompt slots: [`schemas/restore-check-prompt.md`](schemas/restore-check-prompt.md).

## Common failures

| Symptom | Fix |
| --- | --- |
| MCP `initialize: EOF` | Use `run --rm -i` (not `compose run -i` on Windows); token set; literal artifact stage dir |
| Restore fails in milliseconds | `chmod 1777` on `reksicherung_sql-backup`; OpenBao SQL password matches SA/login |
| Discovery OK, stage fails | Check OpenBao source KV / network to object storage; mark FAIL, do not invent PASS |
| Inspect sees `master` only | Pass restore `run_id` into inspect tools |
| MySQL inspect: no database / cannot inspect `information_schema` | Pass restore `run_id` (derives `reksicherung_<hash>`) |
| MySQL restore rejects dump | Dump must reference only `source_database` (single-database); see `cmd/reksicherung-mcp-mysql-restore/README.md` |

More Windows/Podman notes (also apply without Airon):
[`.agents/skills/reksicherung-airon-onboard/reference-pitfalls.md`](../.agents/skills/reksicherung-airon-onboard/reference-pitfalls.md).

## Airon

If you want a packaged Airon job instead of a desktop MCP host, use skill
`reksicherung-airon-onboard` (GHCR + RUNBOOK). Do **not** copy
`examples/airon/restore-check.yaml` for Windows/GHCR labs — that file is a
**host-binary / developer** stub; see [`examples/airon/README.md`](../examples/airon/README.md).
