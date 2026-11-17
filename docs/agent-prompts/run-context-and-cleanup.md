# Run context and cleanup

Drive Reksicherung through its MCP tools and the
`.agents/skills/reksicherung-restore-check` skill (manually, via Airon, or any other
orchestrator). Cleanup is database-scoped:

- Normal mode: call `sqlserver_drop_restore_database` (or
  `mysql_drop_restore_database` when `database.type=mysql`) for the same `run_id`.
- `hold_for_review`: leave the run-scoped database and **register the held run**
  (required step) with the engine that hosts it:

  ```bash
  reksicherung hold -registry /var/lib/reksicherung/runs/runs.db -run-id <run_id> \
    -database-type <sqlserver|mysql> -verdict <pass|fail|inconclusive>
  ```

  Use `database-type=mysql` for `database.type=mysql` sources. `-database-type`
  is mandatory (no default) because an unrecorded type is treated as `sqlserver`
  and `reksicherung cleanup` / `reconcile` would send a MySQL database to the
  wrong engine. Later drop it with `reksicherung cleanup -registry … -run-id …`
  (or the same MCP drop tool).

Integrity `command_completed` means the check ran, not that the database is clean.

Do not ask Reksicherung to stop, remove, or recreate Docker/Podman services.
Operators own compose lifecycle.

Optional orchestrator run-ID correlation may use MCP `_meta` key
`1node.xyz/external-run-id`. Airon-specific notes live under `examples/airon/`.
