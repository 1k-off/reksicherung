# GoBackup configuration generator

Renders GoBackup YAML from the shared backup-as-code contract in
`internal/reksicherung/configgen/input` (`I-CONFIGGEN`).

Use `input.Decode` / `input.Input.Validate`, then `Generate` for deterministic
GoBackup YAML plus a runtime binding manifest.

Credential inputs must be `*_ref` values accepted by
`internal/reksicherung/secrets.ParseReference`. Generated YAML contains
GoBackup runtime placeholders such as `$REKSICHERUNG_STORAGE_ACCESS_KEY_ID`;
the returned bindings associate those environment-variable names with secret
references. This package never resolves references, reads environment
variables, or returns secret values.

Supported behavior:

- Simple and enhanced daily/weekly/monthly schedules, retention defaults,
  healthchecks, model names, storage names, and object path prefixes.
- S3, MinIO, and SFTP storage configuration.
- MSSQL, PostgreSQL, and MySQL database sources, including certificate
  verification by default and the `ignore_schema_check` argument mapping.
- File archives (POSIX or Windows drive-letter paths), compression, and OpenSSL
  encryption, healthchecks notifier switches, and GoBackup's web bind
  configuration.
- Optional `hooks.run_before` / `hooks.run_after` → model `before_script` /
  `after_script` (operator-owned dump/cleanup commands).

Not supported here: host or Docker deployment generation (including Compose,
systemd units, mounts, and env files), batch manifests/config directories,
`.env` loading or generation-time environment expansion, output-file writing,
legacy deployment/output options, arbitrary database types, and custom notifier
implementations. Free-form database `args` and the legacy `scripts` field remain
rejected; use `hooks` for pre/post commands. Configure runtime environment
variables from the returned binding manifest in the Reksicherung launcher.

This input schema rejects credential-shaped YAML fields and rejects endpoint
userinfo/query strings. It does not resolve secret references and cannot infer
whether a non-credential metadata value contains sensitive data.
