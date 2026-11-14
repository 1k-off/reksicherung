# Reksicherung glossary

Short terms used in operator docs and skills. Full contracts: [`inputs.md`](inputs.md).

| Term | Meaning |
| --- | --- |
| **source_id** | Stable id of one backup source. Equals `BackupCatalog.backups[].id` and OpenBao path `secret/reksicherung/sources/<id>`. |
| **Thin catalog (`I-CATALOG`)** | YAML listing sources (provider, database, selection, cleanup, validation). No storage endpoints or credentials. |
| **Inventory (`I-INVENTORY`)** | Secret-free JSON emitted by `reksicherung catalog`. What orchestrators/agents consume for a run. |
| **OpenBao** | Operator-managed secret store (KV v2). Holds defaults, per-source storage creds, and SQL restore connection. |
| **defaults** | `secret/reksicherung/defaults` — shared storage fields merged under each source. |
| **sqlserver KV** | `secret/reksicherung/sqlserver` — `HOST`, `USERNAME`, `PASSWORD` (+ optional `PORT`) for restore/inspect/cleanup. |
| **STORAGE_TYPE** | Required after merge: `s3` or `azure`. |
| **provider** | Backup engine for a source: `gobackup` or `restic`. |
| **configgen (`I-CONFIGGEN`)** | Optional backup-as-code YAML → GoBackup / resticprofile files. Skip if backups already exist. |
| **staging / artifact_id** | Provider MCP downloads a `.bak`/`.bacpac` into a shared volume; restore MCP restores by opaque `artifact_id`. |
| **backupPath** | Catalog field for restic: absolute path of the `.bak` inside each snapshot. |
| **archive_member / member_path** | When a GoBackup archive has several `.bak`/`.bacpac` files, pick one path; never invent if ambiguous. |
| **run_id** | Reksicherung’s id for one restore-check. Names the run-scoped DB (`reksicherung_<hash>`). |
| **external_run_id** | Optional orchestrator correlation id (tool arg or MCP `_meta` `1node.xyz/external-run-id`). |
| **cleanup `delete`** | Drop the run-scoped DB after the check (default for unattended). |
| **cleanup `hold_for_review`** | Keep the restored DB until an operator runs `reksicherung cleanup`. |
| **PASS / FAIL / INCONCLUSIVE** | Authoritative verdicts. Never invent PASS when OpenBao/MCP/archive selection is incomplete. |
| **semanticReview** | Optional AI read-only SQL inspection after integrity check. Cannot override a failed required check. |
| **reconciler** | Compose service that retries expired automatic DB cleanup leases. Never stops SQL Server. |
| **MCP** | Constrained tool servers (gobackup, restic, sqlserver-restore, sqlserver-inspect). Reksicherung does not start containers from skills. |
| **Airon** | Optional job/orchestrator binding under `examples/airon/` and skill `reksicherung-airon-onboard`. Not required. |
| **I-*** | Documented input ids in [`inputs.md`](inputs.md) (e.g. `I-CATALOG`, `I-COMPOSE-ENV`). Unsupported surface if missing from that registry. |
