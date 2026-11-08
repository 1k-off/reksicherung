# GoBackup configuration generator

This package adapts the backup-as-code model of
`infrastructure/utils/gobackup-config-generator` into the Reksicherung module.
Use `Decode` for strict YAML input, `Input.Validate` for checks, and `Generate`
for deterministic GoBackup YAML plus a runtime binding manifest.

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
- MSSQL, PostgreSQL, and MySQL database sources, including MSSQL certificate
  trust defaults and the `ignore_schema_check` argument mapping.
- File archives, compression, OpenSSL encryption, before/after scripts,
  healthchecks notifier switches, and GoBackup's web bind configuration.

Not supported here: host or Docker deployment generation (including Compose,
systemd units, mounts, and env files), batch manifests/config directories,
`.env` loading or generation-time environment expansion, output-file writing,
legacy deployment/output options, arbitrary database types, and custom notifier
implementations. Configure runtime environment variables from the returned
binding manifest in the Reksicherung launcher.

Do not put credentials in scripts, endpoint URLs, or other ordinary string
settings. This input schema rejects credential-shaped YAML fields and rejects
endpoint userinfo/query strings, but it cannot infer whether arbitrary script
text or non-credential metadata contains sensitive data.
