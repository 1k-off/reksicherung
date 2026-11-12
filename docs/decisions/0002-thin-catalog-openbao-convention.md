# 0002: Thin catalog v1alpha2 and OpenBao per-source convention

**Status:** Accepted  

## Decision

1. Catalog/inventory group version is `reksicherung.1node.xyz/v1alpha2`.
2. `BackupCatalog` lists sources only (`id`, `provider`, `database`, `selection`,
   `cleanup`, optional `validation`, restic `backupPath`). Storage coordinates
   and credentials are **not** catalog fields.
3. Per-source S3/Restic material lives in project OpenBao at
   `secret/reksicherung/sources/<source_id>` with optional
   `secret/reksicherung/defaults` merge. Field names are fixed (see
   `docs/schemas/openbao-source-kv.md`).
4. Provider MCP processes take OpenBao bootstrap env only and require
   `source_id` on each discovery/stage tool call.
5. SQL Server connection for restore/inspect/cleanup is
   `secret/reksicherung/sqlserver` with fixed keys `HOST`, `USERNAME`,
   `PASSWORD` (optional `PORT`, default `1433`). Resolved via
   `secrets.ResolveSQLServer`; no ADDRESS/DSN/password-ref env vars.
6. **AI-first docs policy:** any new operator/agent input requires an entry in
   `docs/inputs.md`, a schema under `docs/schemas/`, fixtures, and a skill
   update. Undocumented env vars are unsupported.

## Migration from v1alpha1 (`reksicherung.io`)

- Breaking: remove `storage`, `credentials`, `repositoryPasswordRef`,
  `backupName`, and `restore` resource blocks from catalog YAML.
- Move keys into OpenBao source paths; keep one OpenBao per project.
- No automatic catalog rewriter ships with this release.

## Consequences

- Many databases / ACL key pairs no longer need compose `.env` edits.
- Agents can add sources via `skills/reksicherung-add-source` using schemas only.
- Operators must provision OpenBao KV before the first restore-check run.
