# Shared configgen input (`I-CONFIGGEN`)

Canonical backup-as-code YAML consumed by `configgen/gobackup` and
`configgen/resticprofile`.

Use `Decode` for strict YAML input, `Input.Validate` for shared checks, and
`NewBinder` / `Binder.Placeholder` when rendering provider configs. Credential
inputs must be `*_ref` values accepted by
`internal/reksicherung/secrets.ParseReference`. Generators never resolve
references or return secret values.

Machine-readable schema: `docs/schemas/configgen-input.schema.json`
(registry id `I-CONFIGGEN`).

Each provider may reject fields it cannot map (`ValidateForProvider`). Storage
`type` is an extensible discriminator (`s3`, `minio`, `sftp` today); new
backends extend this package first, then each renderer.
