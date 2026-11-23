# Shared configgen input (`I-CONFIGGEN`)

Canonical backup-as-code YAML consumed by `configgen/gobackup` and
`configgen/resticprofile`.

Use `Decode` for strict YAML input, `Input.Validate` for shared checks, and
`NewBinder` / `Binder.Placeholder` when rendering provider configs. Credential
inputs must be `*_ref` values accepted by
`internal/reksicherung/secrets.ParseReference`. Generators never resolve
references or return secret values.

Machine-readable schema: `docs/schemas/configgen-input.schema.json`
(registry id `I-CONFIGGEN`). Operator templates: `examples/configgen/`.

Optional `hooks.run_before` / `hooks.run_after` (string or list) map to
provider pre/post commands. resticprofile on Windows emits pure PowerShell
`<project>-backup.ps1` / `<project>-cleanup.ps1` sidecars
(`powershell -File C:\restic\config\…` in the profile). Embed
`openbao://…#key` in hook strings for secret substitution. Dump production
remains operator-owned — use hooks plus `archive.includes` for resticprofile
sqlbak flows; GoBackup still accepts native `databases[]`.

`archive.includes` / `excludes` accept POSIX absolute paths (`/…`) or Windows
drive-letter paths (`C:\…` / `C:/…`).

Each provider may reject fields it cannot map (`ValidateForProvider`). Storage
`type` is an extensible discriminator (`s3`, `minio`, `sftp`, `azure`); new
backends extend this package first, then each renderer.
