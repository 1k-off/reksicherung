# resticprofile configuration generator

Renders resticprofile v1 YAML from the shared backup-as-code contract in
`internal/reksicherung/configgen/input` (`I-CONFIGGEN`).

Use `input.Decode` / `input.Input.Validate`, then `ValidateForProvider` /
`Generate` for deterministic YAML plus a runtime binding manifest.

Supported subset (v1):

- Simple and enhanced schedules with forget retention and healthcheck hooks
- S3 / MinIO repositories (`repositoryURL` helper is isolated for future
  storage backends)
- Azure Blob via `azure:<container>:/<path>` with exactly one of
  `account_key_ref` → `AZURE_ACCOUNT_KEY` or `account_sas_ref` →
  `AZURE_ACCOUNT_SAS`
- File archive sources (`archive.includes` / `excludes`); POSIX or Windows
  drive-letter paths
- Optional `hooks.run_before` / `hooks.run_after` → sidecar scripts + short
  `backup.run-before` / `run-after` invocations (`powershell -File …ps1` on
  Windows hosts, `sh …sh` otherwise). Avoids nested-quote breakage in
  resticprofile ([#582](https://github.com/creativeprojects/resticprofile/issues/582)).
  Dump/cleanup remain operator-owned; no `databases[]` mapping
- Repository password via `encryption.password_ref` → `RESTIC_PASSWORD` binding

Rejected: databases, web, sftp, azure service principal refs,
`archive.compress_type`, openssl `encryption.salt` / `encryption.base64`,
missing archive or password.

This package never resolves secret references, writes files, or emits deploy
scripts.
