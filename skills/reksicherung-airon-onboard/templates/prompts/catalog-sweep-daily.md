# Daily catalog coverage sweep

Use job file `airon-catalog-sweep.yaml` (not `airon.yaml`).

The model reads `catalog/catalog.yaml` + `coverage/registry.json` via `fs_*`,
picks never-checked / oldest sources, restore-checks them, writes
`coverage/runs/…` evidence (`database_name` ↔ `restored_database`) and updates
the registry. No host selection scripts.

```
daily_batch=2
oldest_window=10
cleanup=delete
```

## Airon invocation

```powershell
# package root
$env:REKSICHERUNG_OPENBAO_TOKEN = (Get-Content .\compose\.env |
  Where-Object { $_ -match '^REKSICHERUNG_OPENBAO_TOKEN=' } |
  ForEach-Object { ($_ -split '=',2)[1].Trim().Trim('"') } | Select-Object -First 1)
cd job
airon run --config .\airon-catalog-sweep.yaml --no-healthchecks `
  --input "cleanup=delete`ndaily_batch=2`noldest_window=10"
cd ..
```

```bash
cd job
airon run --config ./airon-catalog-sweep.yaml --no-healthchecks \
  --input "cleanup=delete
daily_batch=2
oldest_window=10"
```

Optional: `source_id=acme-prod-appdb-daily` to force one fictional id (still load
provider/path from catalog — never invent).

Expect final `### restore_map` and files under `coverage/runs/`. Airon tool log:
`logs/run-*.json`. Step text: `outputs/catalog_sweep.md`.
