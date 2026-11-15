# MySQL inspection MCP server

`reksicherung-mcp-mysql` is a standalone MCP server speaking newline-delimited
JSON-RPC over stdio. It wraps the read-only
[`internal/reksicherung/mcp/mysql`](../../internal/reksicherung/mcp/mysql)
service for semantic review of a database restored by
`reksicherung-mcp-mysql-restore`. It listens on no network port.

## Tools

- `mysql_list_tables`: list at most 200 base tables (names only).
- `mysql_describe_table`: at most 200 columns of a table from
  `information_schema`.
- `mysql_validate_table`: count rows, capped at 100. Predicates are not
  accepted (same as SQL Server inspection); only the count and a truncation flag
  are returned.
- `mysql_check_integrity`: fixed default-option `CHECK TABLE` over the base
  tables (up to 200) with a 15-minute timeout. Returns `command_completed` only. **Completion is not a clean verdict:**
  `CHECK TABLE` problem rows are discarded, and `command_completed` is false
  when tables were skipped due to the cap (no extra detail is exposed).

Every tool accepts an optional `run_id`; the server derives the run-scoped
database `reksicherung_<32 hex>` exactly as the restore MCP does. `schema` is
optional and, since MySQL schemas are databases, must match the inspected
database. Unknown properties are rejected, no tool takes SQL text, and no tool
returns row values. Each stdio frame is limited to 64 KiB; errors are reduced to
safe generic text; stdout carries only protocol traffic.

## Configure

```sh
go build -o ./bin/reksicherung-mcp-mysql ./cmd/reksicherung-mcp-mysql
```

- `REKSICHERUNG_OPENBAO_ADDR`, `REKSICHERUNG_OPENBAO_TOKEN` (required).
- `REKSICHERUNG_MYSQL_DATABASE` (optional): pin the process to one database. When
  unset the process connects to `information_schema`, cannot inspect it, and
  every call must pass `run_id`.

Host, user, and password come from OpenBao `secret/reksicherung/mysql`
(`HOST`, `USERNAME`, `PASSWORD`; optional `PORT`, default 3306), the same
secret the restore MCP uses. Credentials are never accepted from tool callers.
Prefer a separate OpenBao identity/account for inspection with `SELECT` (plus
`INSERT` only if you enable `CHECK TABLE`) on `reksicherung_%` databases.

```yaml
mcp_servers:
  - name: reksicherung_mysql
    transport: stdio
    command: /usr/local/bin/reksicherung-mcp-mysql
    env_passthrough: false
    env:
      REKSICHERUNG_OPENBAO_ADDR: ${REKSICHERUNG_OPENBAO_ADDR}
      REKSICHERUNG_OPENBAO_TOKEN: ${REKSICHERUNG_OPENBAO_TOKEN}
```

Set `runtime.log_inputs: false`; table names are tool inputs. Row limits do not
cap server CPU or I/O on large tables; use a disposable restored target.
