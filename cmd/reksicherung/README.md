# Reksicherung CLI

Catalog validation, inventory, configgen, cleanup, and reconcile:

```sh
go run ./cmd/reksicherung validate -config examples/catalog.example.yaml
go run ./cmd/reksicherung catalog -config examples/catalog.example.yaml
go run ./cmd/reksicherung validate -config production.yaml -production
go run ./cmd/reksicherung configgen \
  -provider resticprofile \
  -in path/to/input.yaml \
  -out path/to/restic.yml \
  -bindings path/to/bindings.json
```

`catalog` writes compact, secret-free inventory JSON to stdout so it can be
captured by an operator or orchestrator. Validate/catalog reject unknown YAML
fields, enforce the versioned catalog schema, and limit input files to 1 MiB.
`catalog` accepts `-source-ref` to provide a stable relative configuration
reference in each inventory entry; by default it uses the configuration
file's basename. Use `-production` to require HTTPS for configured storage endpoints.

`configgen` renders GoBackup or resticprofile YAML from `I-CONFIGGEN` without
resolving secret refs; optional `-bindings` writes env→OpenBao mappings as JSON.

The CLI does not start, stop, or remove containers. Runtime services such as
SQL Server and backup-provider MCP servers are expected to be started and
managed by the user's Docker Compose deployment; any MCP client may call those
tools.
