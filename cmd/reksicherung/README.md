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
file's basename. Use `-production` to require HTTPS for configured storage endpoints. The
restore service image and resource limits are owned by the operator's Compose
deployment, not by this CLI.

The CLI is limited to catalog validation and inventory generation. It does not
start, stop, or remove containers. Runtime services such as SQL Server and
backup-provider MCP servers are expected to be started and managed by the
user's Docker Compose deployment; Airon coordinates those services through
their configured MCP tools.
