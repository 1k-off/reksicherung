# Backup inventory

`catalog.FromConfig` emits `BackupInventory` (`reksicherung.1node.xyz/v1alpha2`)
as secret-free JSON for operators or any orchestrator. It contains source IDs,
providers, database names, selection hints, and check lists — never endpoints
or credentials.
