# Reksicherung catalog configuration

`config.Decode` accepts a single YAML document, rejects unknown keys, and
validates the complete catalog. The first schema is versioned as
`reksicherung.io/v1alpha1`; incompatible changes require a new API version.
See [`examples/catalog.yaml`](../../../examples/catalog.yaml) for a complete
example.

Each source describes one SQL Server backup, a GoBackup or Restic adapter, an
S3-compatible repository, and the policy and bounded resources for a restore
check. Restic sources require `repositoryPasswordRef` and `backupPath`;
GoBackup sources reject both fields. `backupPath` is one exact, absolute,
canonical `.bak` file path inside the selected Restic snapshot. It cannot
contain traversal or glob syntax, and the pipeline uses it as a single argv
element to `restic dump` rather than restoring a snapshot tree. Restic
writes the selected file contents to command stdout, which is streamed to SQL
Server's fixed backup location.
`repositoryPasswordRef` is the OpenBao secret reference for the Restic
repository password, not a password value or a repository URL. Secret fields
are references only and are validated without resolving them. Literal
credential keys are rejected as unknown YAML fields.

`repositoryPasswordRef` and `backupPath` are operational configuration and are
never copied into the Airon inventory. The Restic repository address is built
from `storage.endpoint`, `bucket`, and `prefix`; resolved credentials are
provided to the restore pipeline by its caller and are not part of the URL.

Current limits are deliberate: only SQL Server and S3 are supported; backup
selection is either `latest` or an explicit provider backup ID; cleanup is
either `delete` or `hold_for_review`. The restore image and runtime limits are
explicit so a check cannot silently inherit unbounded host resources. Production
deployments should pin the restore image to a reviewed digest.

Set `validation.semanticReview: true` to offer AI-assisted semantic review in
the generated Airon inventory. `validation.expectedEmptyTables` is an optional
list of `schema.table` names; an empty table is still a warning, not a failure.
`Catalog.ValidateProduction` additionally requires each restore image to use a
sha256 digest. The local example uses a mutable tag for development and is not
valid under production validation.
