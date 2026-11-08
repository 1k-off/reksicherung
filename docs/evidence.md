# Evidence schema (v1 operational contract)

Reksicherung writes append-only JSON Lines. One file per run.

## File

- Path: `${REKSICHERUNG_EVIDENCE_DIR}/<run-id>.jsonl`
- Mode: `0600`
- Encoding: UTF-8, one JSON object per line

## Required event fields

See `internal/reksicherung/evidence.Event`. At minimum each run records:

- schema/version identifiers
- run ID and optional Airon run ID
- selected provider, backup/snapshot ID (no secrets)
- restore/import outcome
- integrity check completion flag (not a clean-DB verdict)
- AI semantic assessment summary when configured
- final `PASS` / `FAIL` / `INCONCLUSIVE`
- cleanup mode and outcome

## Retention

Operators retain evidence and the correlated Airon JSON log together. Default
recommendation: 90 days, then archive or delete per policy. Evidence is an audit
trail, not a tamper-proof WORM store.
