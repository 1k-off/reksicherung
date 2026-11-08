# Isolated restore container limits and artifact transfer

The container runtime requires every restore `Spec` to set CPU, memory, writable
layer disk, and lifetime limits. It rejects values outside 0.25–64 CPUs,
64 MiB–1 TiB of memory, 1 GiB–1 TiB of writable layer, and 1 minute–24 hours.
The lifetime starts before Podman setup and bounds creation and quota
verification. `RetainForReview` disables the in-process expiry timer after
startup so a human-review run is not removed while awaiting inspection; callers
must persist its lifecycle and explicitly remove it when review is complete.
Automatic cleanup after process restart is the run registry/reconciler's
responsibility.

Podman containers are created with `--network none`, no published ports or
caller-supplied mounts, and `--image-volume ignore`. Ignoring image-declared
volumes is intentional: SQL Server images may declare `/var/opt/mssql` as a
volume, and an anonymous volume would otherwise sit outside the container
writable-layer quota.

## Disk quota support

The runtime always passes the configured limit as Podman's
`--storage-opt size=<bytes>`. It does not drop the option or fall back to an
unlimited layer. This is not portable across all Podman storage drivers and
host filesystems: the configured driver/filesystem must implement writable
layer size quotas (commonly overlay on a filesystem configured for project
quotas). Podman create errors are returned to the caller, so a restore does
not proceed when Podman rejects the requested limit. Since Podman may accept
the option without enforcing it, `Start` also runs an 8 MiB behavioral quota
probe in a temporary, networkless container using the restore image. It verifies
a 4 MiB write succeeds and a 9 MiB write fails; if the larger write succeeds,
startup fails closed with `ErrDiskQuotaUnsupported`. Ambiguous failures also
fail closed: only an explicit `Disk quota exceeded` result is accepted as
evidence of enforcement. The probe is bounded by the restore CPU/memory limits
and is removed with `podman run --rm`.

The SQL Server integration test additionally verifies the configured 1 GiB
limit by attempting a 1025 MiB write and expects it to fail. Run that test when
validating a host's storage configuration. Unsupported quota configurations
are not treated as successful runtime support.

The limit applies to the container writable layer, including paths otherwise
declared as image volumes. It does not include the immutable image layers or
Podman's own image store.

## Artifact transfer

`Runtime.Transfer` accepts an owned `io.ReadCloser`, a basename, and an explicit byte
limit. It streams directly through Podman stdin into a randomly named temporary
file under `/var/opt/mssql/backup`, then renames it to the requested name only
after the stream completes. No host-path temporary file or bind mount is used;
the resulting file resides in Podman's managed container writable layer, which
uses the host's configured container storage and is subject to the disk quota.
The destination is fixed to that directory; path separators, option-like names,
and the internal temporary-name prefix are rejected. A failed or over-limit
stream triggers best-effort removal of the partial container file. The hard
transfer ceiling is 1 TiB, and the caller may choose any positive lower limit.

The transfer command uses argv directly (`tee`, `mv`, and `rm`), not a shell.
Its context is passed to Podman, and the runtime closes the owned source on
cancellation so a pending read can unblock. Source implementations must honor
the `io.ReadCloser` contract and make `Close` interrupt a pending `Read`.
