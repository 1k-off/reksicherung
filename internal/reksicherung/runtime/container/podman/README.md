# Podman container runtime

This adapter creates containers with a deterministic Reksicherung name and
labels, `--network none`, CPU and memory limits, and a bounded process count. It
does not publish ports, mount host paths, override the image entrypoint, or use a
shell. Container removal is idempotent to support restart reconciliation.

Container environment values are written to a temporary mode-0600 environment
file rather than passed as command-line arguments. The file is removed as soon
as Podman has created the container. The runtime necessarily places these values
in the container configuration because services such as SQL Server consume
environment variables at startup; callers must use per-run credentials and
must not persist or log resolved secret values. Protect the Podman service and
its metadata store as part of the host security boundary.

The SQL Server integration test is opt-in because it starts the real image:

```sh
go test -tags=integration ./internal/reksicherung/runtime/container/podman
```

It starts `mcr.microsoft.com/mssql/server:2022-latest`, executes a local
`SELECT 1` using `podman exec` without publishing the SQL port, and removes the
container. A 1024-process cgroup limit is used because SQL Server creates many
worker threads based on host CPU topology even when its CPU quota is lower.
