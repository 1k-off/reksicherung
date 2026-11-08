# Reksicherung CLI

The initial CLI exposes catalog validation and generation:

```sh
go run ./cmd/reksicherung validate -config examples/catalog.yaml
go run ./cmd/reksicherung catalog -config examples/catalog.yaml
go run ./cmd/reksicherung validate -config production.yaml -production
```

`catalog` writes compact, secret-free inventory JSON to stdout so it can be
captured and passed as an Airon string input. Both commands reject unknown YAML
fields, enforce the versioned catalog schema, and limit input files to 1 MiB.
`catalog` accepts `-source-ref` to provide a stable relative configuration
reference in each inventory entry; by default it uses the configuration
file's basename. Use `-production` to require restore images pinned by sha256
digest; the example is intentionally configured for local development with a
mutable image tag.

`cleanup -registry <runs.db> -run-id <id>` removes a container retained by a
`hold_for_review` run. The command records its cleanup attempt before invoking
the idempotent Podman removal, so it can be retried after interruption.
Restore execution and MCP integration are still in progress; the CLI does not
yet start or restore backups.
