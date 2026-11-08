# Airon run context and cleanup

Airon drives Reksicherung through MCP tools and the
`skills/reksicherung-restore-check` skill. Cleanup is database-scoped:

- Normal mode: call `sqlserver_drop_restore_database` for the same `run_id`.
- `hold_for_review`: leave the run-scoped database; record its derived name and
  later drop it explicitly (registry `CleanupHeld` or the same MCP tool).

Do not ask Airon or Reksicherung to stop, remove, or recreate Docker/Podman
services. Operators own compose lifecycle.

Airon run-ID correlation via MCP `_meta` and JSON log handoff remain Airon-side
follow-ups; see `docs/airon-integration-follow-up.md`.
