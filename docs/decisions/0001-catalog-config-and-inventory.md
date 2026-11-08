# 0001: Versioned catalog and Airon inventory

**Status:** Accepted for the first SQL Server release  

## Decision

Reksicherung configuration is a strict single-document YAML catalog using
`apiVersion: reksicherung.io/v1alpha1` and `kind: BackupCatalog`. Each catalog
entry describes a SQL Server backup source, a GoBackup or Restic provider, S3
repository coordinates, OpenBao credential references, backup selection, a
bounded restore image/resources/timeout, and cleanup policy.

Catalog decoding rejects unknown fields and duplicate source IDs. Secret fields
are references only. Remote S3 endpoints must use HTTPS; HTTP is allowed only
for loopback development endpoints. The first version supports `latest` and
explicit backup-ID selection, S3-compatible storage, SQL Server, and either
`delete` or `hold_for_review` cleanup.

Airon receives a separate compact JSON inventory generated from the validated
catalog. Inventory IDs are sorted for reproducibility. The inventory excludes
secret references, storage endpoints, and restore settings; Reksicherung
resolves operational details by stable source ID.

## Rationale

- A versioned schema makes validation and future incompatible changes explicit.
- Strict field validation prevents typos and prevents accidental literal
  credential fields from being accepted.
- Separating orchestration inventory from operational configuration avoids
  putting unnecessary infrastructure and credential metadata in prompts.
- A small supported selection vocabulary keeps provider-specific backup
  identifiers out of free-form prompt logic.

## Consequences

- Additive fields can be considered within `v1alpha1`; incompatible schema
  changes require a new version and a migration strategy.
- The initial catalog does not yet contain a shared backup-generation model.
  GoBackup generation input remains provider-specific until its fields can be
  reconciled with the catalog without losing schedules, file sources, or
  generator-only options.
- Production deployments must pin restore images by digest even though
  development and integration examples use the requested `2022-latest` tag.
