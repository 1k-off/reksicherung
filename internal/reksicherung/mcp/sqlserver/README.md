# SQL Server semantic-review service

This package exposes only three operations:

- `ListTables` returns a capped inventory of user tables.
- `DescribeTable` returns capped column metadata for a table.
- `Validate` counts rows matching a small set of predicates and returns only a
  capped count plus a truncation flag.

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
guarantee. Enforce the boundary in SQL Server: use a dedicated identity with
only `CONNECT` and `SELECT` on the target restored database/schema (or make the
database read-only after restore), and do not grant `INSERT`, `UPDATE`, `DELETE`,
DDL, `EXECUTE`, impersonation, or cross-database access. `ApplicationIntent=ReadOnly`
is only availability-group routing intent; it does not replace SQL permissions.

The service does not log SQL, parameters, credentials, or underlying driver
errors. It returns stable generic database errors, so callers should also avoid
logging DSNs and raw query inputs.

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
