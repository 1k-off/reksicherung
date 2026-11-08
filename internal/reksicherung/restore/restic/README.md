# Restic restore package

This package provides the Restic-specific portion of a restore: it checks that
the Restic executable can run, reads the repository's snapshot list, selects a
snapshot by `latest` or full ID, and either restores a configured destination
or streams one configured `.bak` file. Its SQL Server pipeline composes the
stream with the native SQL Server restore adapter in an isolated container
and writes lifecycle events through a caller-provided evidence sink. It does
not resolve secrets or coordinate the higher-level workflow.

## Constraints

- The first supported repository syntax is an S3 repository (`s3:...`). S3
  credentials and the Restic repository password are supplied as environment
  variables to the runner, not embedded in the repository URL or command argv.
  The package accepts only a small allowlist of Restic/AWS credential and
  region-related environment variable names.
- `latest` means the snapshot with the newest recorded timestamp. When times
  tie, the lexically smallest snapshot ID is selected for deterministic results.
- Snapshot output is size-limited, parsed as strict JSON, and rejected if it
  contains unknown fields, duplicate object keys, malformed IDs/timestamps, or
  trailing data.
- Native SQL Server restores use catalog `backupPath`, an exact absolute
  `.bak` path inside a snapshot. The path must be canonical and has no glob or
  traversal syntax. Before streaming, the client inspects the selected snapshot
  with `restic ls --json` and requires exactly one matching regular-file node.
  `restic dump` then receives the path as one argv element and streams the file
  contents from stdout; a snapshot-wide restore or path guessing is not used.
- Archive bytes flow through a process pipe and the container runtime's
  streaming stdout adapter to the SQL Server adapter. They are not buffered in
  application memory; transfer is bounded to at most 1 TiB (and can be
  configured lower). Restic and SQL command diagnostics are discarded or
  replaced with sanitized errors.
- The S3 repository address uses Restic's URL form:
  `s3:https://s3.amazonaws.com/<bucket>/<prefix>` when no custom endpoint is
  configured, or `s3:<scheme>://<host>/<bucket>/<prefix>` for a custom
  endpoint. Prefix path segments are URL-escaped independently. S3 and Restic
  credentials are never included in this address or argv. The process receives
  only `RESTIC_PASSWORD`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and
  `AWS_DEFAULT_REGION`. The pipeline relies on Restic's `auto` bucket-lookup
  behavior for custom S3-compatible endpoints.
- `OpenBackupFile` needs a `StreamRunner`. It runs the selected snapshot's
  `dump` command and returns an owned reader whose close cancels the operation.
  Streaming implementations must bound reads through context and must not
  expose process stderr or raw command failures.
- Restore is scoped to the configured `TargetPath`, which must be a clean
  absolute path strictly below the configured `TargetRoot`. Callers cannot
  choose a restore target in an individual request.
- The package never passes `--overwrite` or deletion options. Restic's default
  restore behavior leaves existing files in place. The caller must provide a
  fresh per-run target directory inside the isolated restore container.
- The `Restore` request timeout covers preflight, snapshot listing, and restore.
  `OpenBackupFile` keeps its timeout active until the returned stream is closed;
  its caller must consume and close the stream. Metadata output is size-limited.
  The pipeline enforces a separate maximum archive-byte limit while streaming.
- Runner errors and stderr are not exposed by the package. Public failures use
  fixed sentinel errors or context cancellation/deadline errors.
- The writable-layer disk budget is shared by the staged `.bak` and restored
  SQL Server database. `MaxBackupBytes` must be smaller than `DiskBytes`, but
  that does not predict database expansion or temporary SQL Server overhead.
  Operators must size both limits for the largest expected restore.

## Podman runner adapter

`restore/restic/podman.Runner` implements the bounded metadata `Runner`
contract using argv and the runtime's short-lived environment-file API. For
large payloads, it implements `StreamRunner` using
`runtime/container/podman.Runtime.ExecStreamWithEnvironment`, which pipes
stdout without buffering and keeps stderr discarded.

Both adapters execute argv without a shell, honor context cancellation, and
avoid logging command output or environment values. Secret values are supplied
to Restic only through a short-lived mode-0600 environment file, never in argv
or persistent container configuration. The Restic restore image under `image/`
pins the upstream executable and checks its version at build time; the client
also invokes `restic version` before repository operations.

The caller must resolve `repositoryPasswordRef`, `storage.credentials.*Ref`,
and the SQL Server password before invoking the pipeline and pass the resulting
values through `Request.Secrets`. The pipeline does not query OpenBao or add a
secret resolver; the caller must prevent those values from being logged or
persisted. It constructs the S3 address itself, starts a fresh bounded
container, and applies catalog cleanup policy: `delete` removes it on both
success and failure, while `hold_for_review` retains it on both success and
failure.

The SQL Server base image requires `MSSQL_SA_PASSWORD` at container creation.
Podman can retain that startup environment in container metadata even after
the temporary host env file is deleted. A retained `hold_for_review` container
therefore retains the SQL password in Podman metadata for its lifetime. Use a
dedicated restricted Podman account and explicit retention/cleanup controls;
do not treat host env-file deletion as secret erasure. A runtime secret mount
and image entrypoint that avoids persistent container environment remain
necessary before this can be considered safe for unattended production use.

Podman containers otherwise use `--network none`. The Restic pipeline therefore
requires a trusted `Dependencies.EgressNetwork` name and starts the restore
container on that pre-provisioned network. The network itself is not an
endpoint allowlist: production deployment must enforce egress only to the
configured S3 endpoint and deny routes to production SQL Server networks. The
pipeline fails closed when no named network is configured; it does not claim to
implement host firewall policy.

The native restore adapter validates the backup header's database name against
catalog `database.name`, and rejects a different source database before
restoring. It supports one full backup set at position 1.

The pipeline requires a structured evidence sink and records lifecycle stages
with bounded status, duration, and sanitized failure categories. It does not
record credentials, backup paths, snapshot IDs, or raw SQL/Restic diagnostics.

`DBCC CHECKDB` command completion is recorded as **inconclusive**. The current
SQL adapter does not parse DBCC findings, so command success is not evidence
that the database passed integrity checking. No live Restic/S3/SQL Server
end-to-end verification is claimed by the package tests.
