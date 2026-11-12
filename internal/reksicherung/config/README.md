# Catalog configuration

Package `config` decodes `BackupCatalog` documents with
`apiVersion: reksicherung.1node.xyz/v1alpha2`.

Storage credentials are **not** catalog fields. They are resolved from OpenBao
by source id (`internal/reksicherung/secrets.ResolveSource`).

Schema artifacts: `docs/schemas/backup-catalog.v1alpha2.schema.json`.
