# Run registry

The run registry stores non-secret lifecycle metadata in a bbolt database. It
tracks the Reksicherung run ID, optional Airon correlation ID, current phase,
verdict, run-scoped restored database name, cleanup policy, and renewable
cleanup lease. Credentials, prompts, SQL result data, and backup contents do
not belong here.

Open one registry database per active Reksicherung service process. bbolt locks
the file, so a second process cannot silently race the first writer. Transactions
and revision-checked updates protect concurrent workflow and cleanup operations
inside the service. Closing and reopening the database preserves active leases
for restart recovery.

`Reconciler` claims expired automatic-cleanup leases before dropping the
associated run-scoped database through an operator-managed SQL Server MCP /
sqlcmd path. The claim itself has a bounded lease, so an interrupted cleanup
becomes eligible for a later retry. Database drops must be idempotent. Runs
using `hold_for_review` are never selected for automatic cleanup; an
operator-facing cleanup operation must explicitly drop those databases.
Reksicherung never stops or removes SQL Server or MCP services.

The database is not an evidence log and does not make results tamper-proof. Keep
structured operation events in `internal/reksicherung/evidence`. Filesystem
permissions are restricted to the service account where the platform supports
POSIX mode bits; production deployments must also protect the containing
directory and backups with host-level access controls.
