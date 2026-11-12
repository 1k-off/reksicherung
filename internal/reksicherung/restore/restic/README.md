# Restic restore package

Lists snapshots and streams one selected file from a Restic repository through
a local `restic` executable. It does not create containers, resolve secrets, or
drive the higher-level restore-check workflow.

## Constraints

- First supported repository syntax is S3 (`s3:...`). Credentials and the
  repository password are environment variables, never argv or URL userinfo.
- `latest` selects the newest snapshot timestamp; ties break on lexical ID.
- `OpenBackupFile` requires a `StreamRunner`, uses `restic dump` for one exact
  absolute path, and returns an owned stream the caller must close.
- Optional `TargetRoot`/`TargetPath` apply only to full `Restore` into a local
  path; MCP staging uses `OpenBackupFile` without those fields.
- Runner errors and stderr are not exposed; public failures use sentinel or
  context errors.

MCP staging lives in `cmd/reksicherung-mcp-restic`. SQL Server restore/import
lives in `cmd/reksicherung-mcp-sqlserver-restore`.
