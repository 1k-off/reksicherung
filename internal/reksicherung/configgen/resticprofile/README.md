# resticprofile configuration generator

Renders resticprofile v1 YAML from the shared backup-as-code contract in
`internal/reksicherung/configgen/input` (`I-CONFIGGEN`).

Use `input.Decode` / `input.Input.Validate`, then `ValidateForProvider` /
`Generate` for deterministic YAML plus a runtime binding manifest.

Supported subset (v1):

- Simple and enhanced schedules with forget retention and healthcheck hooks
- S3 / MinIO repositories (`repositoryURL` helper is isolated for future
  storage backends)
- File archive sources (`archive.includes` / `excludes`)
- Repository password via `encryption.password_ref` → `RESTIC_PASSWORD` binding

Rejected: databases, web, sftp, `archive.compress_type`, openssl
`encryption.salt` / `encryption.base64`, missing archive or password.

This package never resolves secret references, writes files, or emits deploy
scripts.
