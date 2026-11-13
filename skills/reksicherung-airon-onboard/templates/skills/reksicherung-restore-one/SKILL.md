---
name: reksicherung-restore-one
description: >-
  Procedure for one BackupCatalog source_id restore-check. Inject into the
  Airon prompt via {{file:}}; do not list as prompts.tools (no MCP tool loop).
---

# Restore one database

Process **exactly one** `source_id` per Airon run. Do not start a second source.
Operate only on the operator-provisioned SQL Server and MCP services — do not
create, start, or stop containers.

## Inputs (operator `--input`)

| Slot | Required | Notes |
|------|----------|--------|
| `source_id=` | yes | Catalog id |
| `provider=` | recommended | `gobackup` \| `restic` |
| `database.name=` | recommended | For BAK metadata / ambiguity |
| `backupPath=` | **yes if restic** | Exact catalog path; never invent |
| `cleanup=` | no | `hold_for_review` (lab default) or `delete` |
| `archive_member=` | no | GoBackup disambiguation hints |

## Sequence

1. **Discover** (use `provider=` when present)
   - `gobackup`: `gobackup__gobackup_latest` / `gobackup__gobackup_list`
   - `restic` (or gobackup discovery fails): `restic__restic_list_snapshots`,
     then `restic__restic_select_snapshot`
   - Do not treat a failed gobackup discovery on a restic source as a hard fail.
2. **Stage**
   - GoBackup: `gobackup__gobackup_stage_artifact` → keep `artifact_id` + `kind`
   - Restic: `restic__restic_stage_artifact` with **exact** catalog `backupPath`
     as `backup_path`. If missing → `FAIL:` and stop. Never invent paths.
   - On `code=ambiguous_archive`: `gobackup__gobackup_list_archive_candidates`,
     pick `member_path` using `database.name` + hints, retry. Still ambiguous →
     `FAIL:` — do not invent a path.
3. **Restore** — `restore__sqlserver_restore_backup` with short `run_id`
   (≤128, `[A-Za-z0-9._-]`), `artifact_id`, `source_database` (= catalog
   `database.name`). From the tool JSON record:
   - `database_name` = `source_database`
   - `restored_database` = result `database` (`reksicherung_<32hex>`)
   Never invent the restored name.
4. **Integrity** — `restore__sqlserver_check_restore_integrity` with same `run_id`.
5. **Semantic review** (when enabled) — inspect tools with the **same** `run_id`
   (targets `reksicherung_*`, not `master`). If inspect cannot see the DB →
   `FAIL: inconclusive — …`.
6. **Verdict** — final line `PASS:` or `FAIL:` only (never `INCONCLUSIVE:` —
   Airon `pass_fail` ignores it). Include:
   `database_name=<name> restored_database=<reksicherung_…|none> run_id=<id>`
7. **Cleanup** — `cleanup=delete` → `restore__sqlserver_drop_restore_database`;
   `hold_for_review` → report held `restored_database` and skip drop.

## Hard rules

- No docker/podman/compose triage; report MCP errors and stop.
- No invented credentials, paths, catalog ids, or `restored_database`.
- One DB only — for monthly coverage use `reksicherung-catalog-sweep`
  (agent picks via `fs_*` on catalog + registry).
