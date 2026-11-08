# SQL Server semantic-review service

This package exposes four operations:

- `ListTables` returns a capped inventory of user tables.
- `DescribeTable` returns capped column metadata for a table.
- `Validate` counts rows matching a small set of predicates and returns only a
  capped count plus a truncation flag.
- `CheckIntegrity` runs full `DBCC CHECKDB` against the configured target, with a
  fixed timeout and fixed non-repair options.

`Options.TargetDatabase` is required and scopes all operations to one configured
restore database. Configure the `database/sql` connection pool for that same
database. Database, schema, table, and column names are quoted SQL Server
identifiers (including escaped closing brackets); predicate values and
metadata filters are bound parameters.

## Query boundary and limits

`Validate` does not accept SQL text. It constructs one parameterized `SELECT`
from the table reference, at most the configured predicate count, and the
allowlisted comparison operators. It cannot express projections, joins,
subqueries, functions, multiple statements, writes, DDL, or procedure execution.
This is a structural allowlist, not a regular-expression SQL filter, so payloads
that resemble SQL remain parameter values or quoted identifiers.

Each validation query uses `TOP (limit + 1)` inside an aggregate. The service
returns at most `limit` and marks the result truncated when the extra row
exists; it does not return business-field values. Metadata, predicates,
identifier sizes, string values, and configured timeouts have hard ceilings.
Query and metadata operations honor caller cancellation and a service timeout.
`CheckIntegrity` has a separate timeout (5 minutes by default, at most 30
minutes); the MCP executable sets it to 15 minutes. The DBCC statement is
generated from the configured target only and uses a fixed `MAXDOP = 2`.

These limits bound returned rows, not guaranteed database CPU or I/O. A
selective predicate may scan a large table before finding enough matches, and
SQL Server may spend time waiting on locks. Timeouts and the read-only login
reduce exposure but are not a query-cost governor; use restored databases with
appropriate resource limits/indexes and keep the service isolated.

## Read-only deployment assumptions

The API never issues data-changing SQL. SQL Server's `database/sql` driver
`github.com/microsoft/go-mssqldb` rejects `TxOptions{ReadOnly:true}` rather than
providing a read-only transaction, so this package intentionally runs generated
`SELECT` statements in autocommit mode instead of claiming a transaction-level
guarantee. Enforce the boundary in SQL Server: for the metadata and validation
operations, use a dedicated identity with only `CONNECT` and `SELECT` on the
target restored database/schema (or make the database read-only after restore).
`ApplicationIntent=ReadOnly` is only availability-group routing intent; it does
not replace SQL permissions.

The service does not log SQL, parameters, credentials, or underlying driver
errors. It returns stable generic database errors, so callers should also avoid
logging DSNs and raw query inputs.

`CheckIntegrity` does not use any `REPAIR_*` option. SQL Server's normal
`DBCC CHECKDB` operation checks a database without repairing its source data.
The result reports only whether the command completed without a SQL command
error; it does not return SQL Server's DBCC diagnostic messages or claim that
the database has zero consistency errors. The Go `database/sql` API does not
expose the driver's informational DBCC message stream, so a separate
SQL-Server-specific diagnostic capture mechanism would be required to report a
structured corruption count.

`DBCC CHECKDB` requires membership in `sysadmin` or the target database's
`db_owner` fixed role. Never grant `sysadmin` for this operation. If enabling
the tool, use a dedicated identity that is `db_owner` only in an isolated,
disposable restored target; `db_owner` is powerful and can modify that target
even though this service exposes no repair or arbitrary-SQL operation. If
that permission is unacceptable, do not enable this check under the restricted
`CONNECT`/`SELECT` identity. `MAXDOP` and the operation timeout limit some
resource exposure but do not cap SQL Server I/O, tempdb use, or total work
before cancellation.

## Local SQL Server integration check

The integration test is opt-in and refuses any DSN whose host is not
`localhost` or an IP loopback address. Run it only against a disposable SQL
Server exposed on loopback by Podman, with a disposable database and dummy
fixture rows. Supply the DSN through `REKSICHERUNG_SQLSERVER_TEST_DSN`; do not
put credentials in source control or pass a production DSN.

```sh
REKSICHERUNG_SQLSERVER_TEST_DSN="$LOCAL_SQLSERVER_DSN" \
  go test -tags=integration ./internal/reksicherung/mcp/sqlserver
```
