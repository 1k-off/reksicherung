# Reksicherung restore-check skill

Run a backup restoreability check through Reksicherung MCP tools. Do not create,
start, or stop containers. Operate only on the operator-provisioned database
(SQL Server or MySQL) and MCP services. Any orchestrator (or a human operator)
may drive this skill.

Attach MCP first: [`docs/mcp-clients.md`](../../../docs/mcp-clients.md) (or Airon).
Onboarding: [`docs/getting-started.md`](../../../docs/getting-started.md).

Inputs: `docs/inputs.md` (`I-INVENTORY`, `I-PROMPT`). Schemas under `docs/schemas/`.

## Inputs

- Catalog inventory JSON (`reksicherung catalog`) or an explicit list of
  `source_id` values from `BackupCatalog`
- Cleanup mode: `delete` (default) or `hold_for_review`
- Optional `external_run_id` for correlation (tool arg or MCP `_meta`
  `1node.xyz/external-run-id`)
- Optional `archive_member` / disambiguation hints in the prompt (see
  `docs/schemas/restore-check-prompt.md`)

## Branch on `database.type`

Read `database.type` from the inventory entry (default `sqlserver` when absent)
and use **only** that family's tools. Discover/stage (steps 1–2) and the
verdict rules are shared.

| `database.type` | Staged `kind` | Restore / integrity / drop | Inspect (semantic review) |
| --- | --- | --- | --- |
| `sqlserver` | `bak`, `bacpac` | `sqlserver_restore_backup`, `sqlserver_check_restore_integrity`, `sqlserver_drop_restore_database` | `sqlserver_list_tables`, `sqlserver_describe_table`, `sqlserver_validate_table`, `sqlserver_check_integrity` |
| `mysql` | `sql` (plain or gzip logical dump) | `mysql_restore_backup`, `mysql_check_restore_integrity`, `mysql_drop_restore_database` | `mysql_list_tables`, `mysql_describe_table`, `mysql_validate_table`, `mysql_check_integrity` |

Never call a `sqlserver_*` tool for a `mysql` source or vice versa. If the
staged `kind` does not match the type (e.g. `bak` for `mysql`) — verdict `FAIL`.

## Tool sequence (per source_id)

1. **Discover** — GoBackup or Restic list/select tools with required `source_id`.
2. **Stage** — provider `*_stage_*` tool with the same `source_id`. Keep
   `artifact_id` and `kind` (`bak` / `bacpac` for sqlserver, `sql` for mysql).
   - **GoBackup ambiguity**: if `gobackup_stage_artifact` returns
     `code=ambiguous_archive` (or call `gobackup_list_archive_candidates`),
     choose exactly one `member_path` using `database.name` plus any prompt
     `archive_member` hints. Retry `gobackup_stage_artifact` with that
     `member_path`. If more than one candidate still matches equally, or no
     candidate clearly matches — **do not invent** — verdict `FAIL` or
     `INCONCLUSIVE`.
3. **Restore** — `sqlserver_restore_backup` / `mysql_restore_backup` with
   `run_id`, `artifact_id`, and `source_database` (= catalog `database.name`).
   MySQL dumps may reference only `source_database`; other-database references
   fail closed. If a MySQL restore fails part-way, drop the run database.
4. **Integrity** — `sqlserver_check_restore_integrity` /
   `mysql_check_restore_integrity`. **`command_completed: true` means only that
   the check command exited without error; it is NOT a clean-integrity verdict**
   (same for `*_check_integrity` inspect tools). Do not report "clean" or PASS
   on this signal alone; failure to complete is `FAIL`.
5. **Semantic review** — read-only inspect tools of the matching family when
   inventory enables `semanticReview`. Pass the same `run_id` used for restore
   so inspect targets the run-scoped DB (`*_list_tables` / `describe` /
   `validate` with `run_id`). Do not assume
   `REKSICHERUNG_SQLSERVER_DATABASE=master` is the restore DB. If inspect
   cannot see the restore DB after passing `run_id` — verdict `INCONCLUSIVE`
   (not PASS).
   - **MySQL `expectedEmptyTables`**: entries are `source_database.table`.
     The restored database is `reksicherung_<hash>`, so remap
     `source_database.table` → the table name in the run DB (call
     `mysql_validate_table` with `run_id` and the table name; `schema`, if
     given, must be the run database). Only the part before the dot is
     replaced; never inspect the source database name.
6. **Verdict** — `PASS` / `FAIL` / `INCONCLUSIVE`; never invent PASS.
7. **Cleanup** — drop (`sqlserver_drop_restore_database` /
   `mysql_drop_restore_database`) if `delete`. Complete semantic review before
   drop when `cleanup=delete`.
   - `hold_for_review`: report the held DB name **and register the run** so
     later cleanup routes to the right engine (do not skip — an unrecorded
     type defaults to `sqlserver` and mis-routes MySQL drops):
     `reksicherung hold -registry <runs.db> -run-id <run_id> -database-type <sqlserver|mysql> -verdict <pass|fail|inconclusive>`
     (`mysql` when `database.type=mysql`).

## Next

- Recurring coverage: `reksicherung-schedule-verify`
- Triage / hold cleanup: `reksicherung-operate`
- Journey map: `reksicherung-index`

## Hard rules

- Always pass `source_id` from inventory; never invent storage credentials.
- Never request unrestricted shell, Docker/Podman, or arbitrary admin SQL.
- Never run `docker` / `podman` / `compose` for triage when MCP tools fail —
  report the MCP error and stop; the operator owns the stack.
- Never echo secret values into evidence summaries.
- If a required step fails or is skipped, final verdict is `FAIL` or
  `INCONCLUSIVE`.
- Archive member selection is fail-closed: ambiguous AI judgment is not PASS.
- `command_completed` (SQL Server or MySQL) ≠ clean integrity; say "check
  completed" in evidence, never "clean".
