# Restic snapshot discovery MCP server

`reksicherung-mcp-restic` lists/selects snapshots and stages one backup file per
catalog `source_id`. Repository URL, password, and S3 keys come from OpenBao
source KV — not from per-repo compose env.

## Tools

All tools require `source_id`:

- `restic_list_snapshots`
- `restic_select_snapshot`
- `restic_stage_artifact`

## Environment (bootstrap only)

| Variable | Meaning |
| --- | --- |
| `REKSICHERUNG_OPENBAO_ADDR` | OpenBao base URL |
| `REKSICHERUNG_OPENBAO_TOKEN` | Token |
| `REKSICHERUNG_ARTIFACT_STAGE_DIR` | Optional; default `/var/lib/reksicherung/artifacts` |
| `REKSICHERUNG_ARTIFACT_MAX_BYTES` | Optional size cap |

See `docs/schemas/openbao-source-kv.md`.
