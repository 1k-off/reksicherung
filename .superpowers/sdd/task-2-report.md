# Task 2 report: OpenBao MySQL secrets + catalog/inventory enum

Status: DONE
Commit: 1f01ec2 `feat: add MySQL OpenBao material and catalog database.type mysql` (branch `feat/mysql-restore-check`)

## TDD
- RED: secrets build failed (undefined MySQLPath/ResolveMySQL/ErrIncompleteMySQL); config tests failed ("database.type must be sqlserver"); catalog test failed for mysql type.
- GREEN: `go test ./internal/reksicherung/... ./cmd/... -count=1` all packages pass.

## Changes
- `secrets/mysql.go`: `MySQLPath="reksicherung/mysql"`, `ErrIncompleteMySQL`, `MySQLMaterial{Host,Port,Username,Password}`, `Clear()`, `DSN(database) (string, error)` -> `user:pass@tcp(host:port)/db` (IPv6 bracketed), `ResolveMySQL` (default port 3306, same validation as SQL Server; reuses the shared resolve helpers and maps their sentinel to `ErrIncompleteMySQL`).
- `secrets/mysql_test.go`: default port, custom port + IPv6 + special-char password, missing fields / nil provider, invalid port, unsafe DSN inputs.
- `config.go`: accepts `sqlserver` or `mysql`. `catalog.go`: check `mysql-integrity` for mysql, `sqlserver-integrity` otherwise.
- Tests added in config_test.go (mysql ok, postgres rejected, mysql fixture) and catalog_test.go (integrity check follows type).
- Schemas: catalog and inventory `database.type` enum `[sqlserver, mysql]`; inventory checks enum adds `mysql-integrity`; new `openbao-mysql.schema.json`; fixtures `mysql.connection.json`, `catalog.mysql-source.yaml` (existing two-sources fixture untouched).
- Docs: inputs.md (`I-OPENBAO-MYSQL`), schemas/README.md, openbao-source-kv.md (path table + MySQL section), glossary.md.

## Concerns / notes
- `DSN` returns `(string, error)` (mirrors `MSSQLDSN`), brief wrote just `DSN(database string)`. Adjust the signature if Task 3 needs a single return.
- DSN is hand-built (no go-sql-driver dependency added yet); no TLS/params appended. Task 3 may append params or switch to `mysql.Config.FormatDSN` when the driver is added.
- `gofmt -l` lists all files because of CRLF working copies (pre-existing, autocrlf), not a real formatting issue.
- Docs mention `reksicherung-bootstrap` skill for I-OPENBAO-MYSQL; skills themselves not changed.

## Review fix: DSN database escaping
- Finding: `MySQLMaterial.DSN` inserted the database name unescaped; names with `%`, space or `#` could yield an invalid go-sql-driver DSN.
- Fix: `secrets/mysql.go` now uses `url.PathEscape(database)` (go-sql-driver path-unescapes the dbname segment).
- Test: added `TestMySQLDSNEscapesDatabaseName` (`db%41`, `my db`, `db#1`, `a%b c#d`) asserting escaped output and round-trip via `url.PathUnescape`.
- Result: `go test ./internal/reksicherung/secrets/ -count=1` -> ok (2.130s).
