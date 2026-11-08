# Reksicherung restore-check skill

Run a backup restoreability check through Reksicherung MCP tools. Do not create,
start, or stop containers. Operate only on the operator-provisioned SQL Server
and MCP services.

## Inputs

- Catalog inventory (from `reksicherung catalog`) or an explicit backup source ID
- Cleanup mode: `delete` (default) or `hold_for_review`
- Optional Airon run ID for correlation metadata when available

## Tool sequence

1. **Discover** — use GoBackup or Restic MCP list/select tools to identify the
   backup object or snapshot + absolute file path.
2. **Stage** — call the provider `*_stage_*` tool. Keep the returned
   `artifact_id` and `kind` (`bak` or `bacpac`).
3. **Restore** — call `sqlserver_restore_backup` with `run_id`, `artifact_id`,
   and `source_database`. This restores/imports into a database derived from
   `run_id` only.
4. **Integrity** — call `sqlserver_check_restore_integrity`. Treat
   `command_completed=true` as DBCC completion, not as proof the database is
   clean of consistency errors.
5. **Semantic review** — use the read-only SQL Server MCP tools to inspect
   schema and run bounded `SELECT` queries. Empty tables are warnings, not
   automatic failures. Return a structured assessment; do not invent PASS.
6. **Verdict** — aggregate deterministic restore/integrity outcomes with the
   structured AI assessment into `PASS`, `FAIL`, or `INCONCLUSIVE`. A failed
   required check cannot be overridden by prose.
7. **Cleanup** — if cleanup mode is `delete`, call
   `sqlserver_drop_restore_database`. If `hold_for_review`, leave the database
   and report its derived name and drop instructions.

## Hard rules

- Never request unrestricted shell, Docker/Podman, or arbitrary admin SQL.
- Never echo secret values, connection strings, or raw row-level business data
  into evidence summaries.
- If a required step fails or is skipped, final verdict is `FAIL` or
  `INCONCLUSIVE`, never an optimistic PASS.
- Preserve the Airon JSON run log and Reksicherung evidence, correlated by run ID.
