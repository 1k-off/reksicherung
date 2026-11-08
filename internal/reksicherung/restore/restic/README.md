# Restic restore package

This package performs the Restic-specific portion of a restore: it checks that
the Restic executable can run, reads the repository's snapshot list, selects a
snapshot by `latest` or full ID, and restores it into a configured destination.
It does not create or manage containers, configure S3, validate restored
databases, write evidence, or coordinate a workflow.

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
- Restore is scoped to the configured `TargetPath`, which must be a clean
  absolute path strictly below the configured `TargetRoot`. Callers cannot
  choose a restore target in an individual request.
- The package never passes `--overwrite` or deletion options. Restic's default
  restore behavior leaves existing files in place. The caller must provide a
  fresh per-run target directory inside the isolated restore container.
- The request timeout covers preflight, snapshot listing, and restore. Every
  command has a maximum stdout size; the runner must enforce the limit while
  reading, not only after buffering all process output.
- Runner errors and stderr are not exposed by the package. Public failures use
  fixed sentinel errors or context cancellation/deadline errors.

## Runner adapter requirement

`runtime/container.Runtime.Exec` currently accepts argv only and applies a
fixed four-MiB output cap; it does not offer a safe way to inject per-command
environment values or configure the requested output limit. This package
therefore defines its own narrow `Runner` contract instead of passing
credentials through argv or changing the shared runtime interface.

A production adapter must execute the command inside the already-created
restore container without invoking a shell, honor the provided context, apply
the requested output cap as the command runs, and avoid logging command output
or environment values. It must inject environment variables only into the
Restic process environment or through short-lived, permission-restricted secret
files, and must not persist them in container configuration. The adapter must
also ensure its command executable is available in the restore image; this
package checks availability by invoking `restic version` before repository
operations.

The MCP server/orchestrator must create and isolate a fresh restore container,
provide a trusted S3 repository and the minimum required Restic/AWS environment
variables, select a target directory confined to that container, and arrange
cleanup or retention independently of this package. It must not expose the
runner as a general-purpose shell or arbitrary-command tool.

The existing `Runtime.Exec` API is intentionally unchanged. Integrating this
package requires a concrete runner adapter with the contract above; no such
production adapter is included here.
