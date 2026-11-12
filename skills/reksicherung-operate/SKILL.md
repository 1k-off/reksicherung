# Reksicherung operate

Day-2 operations and triage using documented inputs only.

## Common tasks

### hold_for_review cleanup

```bash
reksicherung cleanup -registry /var/lib/reksicherung/runs/runs.db -run-id <id>
```

Requires SQL restore + OpenBao bootstrap env (same as restore MCP).

### Reconciler

`cleanup-reconciler` compose service runs `reksicherung reconcile -registry ...`.
Failed cleanup is not a PASS.

### Evidence

Retain Reksicherung evidence JSONL (`I-EVIDENCE`) and any orchestrator logs
together. Correlate by Reksicherung `run_id` and optional `external_run_id`.

### Triage FAIL

1. Read evidence events for the run (`source_id`, operation, status, error).
2. Confirm OpenBao source KV still resolves for that `source_id`.
3. Confirm inventory still lists the source.
4. Re-run restore-check for the single `source_id` after fixes.

## Fail closed

Do not restart production databases, publish SQL ports, or paste secrets into
tickets/logs.
