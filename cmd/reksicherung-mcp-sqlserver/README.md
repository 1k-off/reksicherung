# SQL Server inspection MCP server

`reksicherung-mcp-sqlserver` is a standalone MCP server that speaks newline-
delimited JSON-RPC over stdio. It uses the existing read-only
[`internal/reksicherung/mcp/sqlserver`](../../internal/reksicherung/mcp/sqlserver)
service and connects to one configured database. It does not listen on a
network port.

## Tools

The server registers only these tools:

- `sqlserver_list_tables`: list at most 200 user tables, optionally in one
  schema.
- `sqlserver_describe_table`: return at most 200 column definitions for a
  selected table.
- `sqlserver_validate_table`: count matching rows up to 100, using at most 8
  AND-combined predicates from the service's fixed operator allowlist.

Tool inputs reject unknown JSON properties and the service validates
identifiers, operators, values, and bounds. The database is not a tool
argument: every call is scoped to `REKSICHERUNG_SQLSERVER_DATABASE`. No tool
accepts SQL text or returns row-level values. Validation returns only an
aggregate count and truncation flag. A 10-second service timeout and fixed
metadata/sample/predicate limits are applied by the executable.
Each incoming stdio protocol frame is limited to 64 KiB before JSON decoding.

Database errors and configuration errors are reduced to safe generic text.
The MCP SDK activity logger is disabled; startup and process errors are
written to stderr. Stdout is reserved exclusively for MCP protocol traffic.
Do not add logs that print environment values, tool inputs, SQL parameters, or
database results.

The service limits returned results, but cannot guarantee a limit on SQL Server
CPU or I/O for a selective predicate. Use a disposable restored target with
appropriate SQL Server resource controls.

## Build and configure

Build with the module's Go 1.23-compatible toolchain. The executable uses the
official, maintained `github.com/modelcontextprotocol/go-sdk/mcp` package at
v1.2.0; that release declares Go 1.23.0 in its module file:

```sh
go build -o ./bin/reksicherung-mcp-sqlserver ./cmd/reksicherung-mcp-sqlserver
```

The process requires these environment variables:

- `REKSICHERUNG_SQLSERVER_DSN`: a `go-mssqldb` URL DSN. It must use the
  `sqlserver` scheme and include exactly one `database` query parameter.
- `REKSICHERUNG_SQLSERVER_DATABASE`: the exact same database name as the DSN.

Example syntax (replace placeholders using a secret manager or protected
process environment; never check a real DSN into source control):

```sh
REKSICHERUNG_SQLSERVER_DSN='sqlserver://<login>:<url-escaped-password>@127.0.0.1:1433?database=RestoredDb&encrypt=disable'
REKSICHERUNG_SQLSERVER_DATABASE='RestoredDb'
```

The SQL Server endpoint should be reachable only from the isolated restore
environment or the MCP process host. Use TLS (`encrypt=true`) outside a
loopback-only development setup. The database name in both settings must match;
the process rejects mismatched targets and never accepts a database selector
from a tool caller.

## Airon stdio configuration

Airon expands `${NAME}` placeholders in its configuration before starting MCP
children. Supply the two values in Airon's protected environment and use
`env_passthrough: false` so the child receives only the explicitly configured
environment. Do not place literal credentials in the YAML.

```yaml
runtime:
  log_inputs: false

mcp_servers:
  - name: reksicherung_sqlserver
    transport: stdio
    command: /usr/local/bin/reksicherung-mcp-sqlserver
    env_passthrough: false
    env:
      REKSICHERUNG_SQLSERVER_DSN: ${REKSICHERUNG_SQLSERVER_DSN}
      REKSICHERUNG_SQLSERVER_DATABASE: ${REKSICHERUNG_SQLSERVER_DATABASE}

prompts:
  - id: inspect_restored_database
    system: |
      Inspect only the restored database through the Reksicherung SQL Server
      tools. Use table listing and schema metadata first, then bounded
      aggregate validation where useful. Never request or infer row values.
    content: |
      Review the restored database schema for the requested validation scope.
    tools:
      - reksicherung_sqlserver__sqlserver_list_tables
      - reksicherung_sqlserver__sqlserver_describe_table
      - reksicherung_sqlserver__sqlserver_validate_table
```

Set `runtime.log_inputs: false` because predicate values are tool inputs and may
otherwise be persisted by Airon. Tool results contain only metadata and
aggregates, not record values.

## SQL Server permissions

Use a dedicated login/user for the restored target. Grant it only `CONNECT` and
`SELECT` on the schema(s) being checked. For example, after provisioning the
login out of band:

```sql
USE [RestoredDb];
CREATE USER [reksicherung_inspector] FOR LOGIN [reksicherung_inspector];
GRANT CONNECT TO [reksicherung_inspector];
GRANT SELECT ON SCHEMA::[dbo] TO [reksicherung_inspector];
```

Do not grant `db_owner`, `db_datawriter`, broad server roles, `EXECUTE`,
`IMPERSONATE`, DDL, write permissions, or cross-database access. Grant
additional schema-level `SELECT` only when those schemas are intentionally in
the review scope. SQL Server metadata visibility follows the permissions of
the user, so table/schema metadata is limited accordingly. `ApplicationIntent`
or a read-only transaction is not a replacement for SQL Server permissions.
