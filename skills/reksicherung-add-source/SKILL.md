# Reksicherung add-source

Add one database/backup source without editing compose `.env` storage keys.

## Inputs

- Existing `I-CATALOG`
- New `I-OPENBAO-SOURCE` at `secret/reksicherung/sources/<id>`
- Schemas: `backup-catalog.v1alpha2`, `openbao-source`

## Steps

1. Choose `id` matching `^[a-z0-9][a-z0-9-]{0,126}$`.
2. Append catalog entry (provider, database, selection, cleanup; restic needs
   `backupPath`). Do **not** add `storage` or credential fields.
3. `bao kv put secret/reksicherung/sources/<id> ...` with required fields for
   the provider (`docs/schemas/openbao-source-kv.md`).
4. `reksicherung validate -config <catalog>`
5. `reksicherung catalog -config <catalog>` → refresh inventory for the next run.
6. Optional: dry-run restore-check for that `source_id` only.

## Fail closed

- Invalid fixture style (`fixtures/invalid/catalog.with-storage.yaml`) must be
  rejected by validate.
- Missing OpenBao keys for the provider → MCP tools fail; do not invent PASS.
