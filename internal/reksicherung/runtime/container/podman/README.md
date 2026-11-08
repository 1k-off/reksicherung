# Podman container runtime

This adapter creates containers with a deterministic Reksicherung name and
labels, CPU and memory limits, and a bounded process count. By default it uses
`--network none`; a trusted `container.Spec.Network` can select a validated
named network, but host networking is rejected. Named networks are not
destination allowlists. A deployment using Restic must pre-provision and
firewall its network so it can reach the configured S3 endpoint but cannot
reach production SQL Server networks. The runtime does not create that policy.
It does not publish ports, mount host paths, override the image entrypoint, or
use a shell. Container removal is idempotent to support restart reconciliation.

Container environment values are written to a temporary mode-0600 environment
file rather than passed as command-line arguments. The file is removed as soon
as Podman has created the container. The runtime necessarily places these values
in the container configuration because services such as SQL Server consume
environment variables at startup; callers must use per-run credentials and
must not persist or log resolved secret values. Protect the Podman service and
its metadata store as part of the host security boundary.

Small command output uses bounded `Exec` methods. `ExecStreamWithEnvironment`
is the separate large-output path: stdout is piped to the caller without
buffering, stderr is discarded, and closing the reader cancels the Podman exec.
Its caller must enforce a payload byte limit and close the stream.

The SQL Server integration test is opt-in because it starts the real image:

```sh
go test -tags=integration ./internal/reksicherung/runtime/container/podman
```

It starts `mcr.microsoft.com/mssql/server:2022-latest`, executes a local
`SELECT 1` using `podman exec` without publishing the SQL port, and removes the
container. A 1024-process cgroup limit is used because SQL Server creates many
worker threads based on host CPU topology even when its CPU quota is lower.
