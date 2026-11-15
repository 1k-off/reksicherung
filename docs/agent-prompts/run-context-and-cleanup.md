# Run context and cleanup

Drive Reksicherung through its MCP tools and the
`.agents/skills/reksicherung-restore-check` skill (manually, via Airon, or any other
orchestrator). Cleanup is database-scoped:

- Normal mode: call `sqlserver_drop_restore_database` (or
  `mysql_drop_restore_database` when `database.type=mysql`) for the same `run_id`.
- `hold_for_review`: leave the run-scoped database; record its derived name and
  later drop it explicitly (registry held cleanup or the same MCP tool).

Integrity `command_completed` means the check ran, not that the database is clean.

Do not ask Reksicherung to stop, remove, or recreate Docker/Podman services.
Operators own compose lifecycle.

Optional orchestrator run-ID correlation may use MCP `_meta` key
`1node.xyz/external-run-id`. Airon-specific notes live under `examples/airon/`.
