# GoBackup archive discovery MCP server

`reksicherung-mcp-gobackup` discovers and stages GoBackup archives per catalog
`source_id`. S3 coordinates and credentials are resolved from OpenBao
(`secret/reksicherung/sources/<source_id>`), not from per-backup process env.

## Tools

All tools require `source_id`:

- `gobackup_list`
- `gobackup_latest`
- `gobackup_select`
- `gobackup_list_archive_candidates` — list terminal `.bak`/`.bacpac` member
  paths inside one archive (metadata only)
- `gobackup_stage_artifact` — optional `member_path` when the archive is
  ambiguous. Without `member_path`, a unique terminal member is staged
  automatically; multiple terminals return `code=ambiguous_archive` with
  `candidates` (IsError). Retry with an exact `member_path` from the list.

## Ambiguous archives

Deterministic resolution prefers a unique terminal DB artifact over nested
wrappers (`.tar`/`.zip`). If two or more `.bak`/`.bacpac` members remain, the
AI (or operator) must choose using catalog `database.name` and optional prompt
slot `archive_member` (`docs/schemas/restore-check-prompt.md`). Inventing a
path when hints are inconclusive is forbidden.

## Environment (bootstrap only)

| Variable | Meaning |
| --- | --- |
| `REKSICHERUNG_OPENBAO_ADDR` | OpenBao base URL (HTTPS except loopback) |
| `REKSICHERUNG_OPENBAO_TOKEN` | Token with read access to source KV |
| `REKSICHERUNG_ARTIFACT_STAGE_DIR` | Optional; default `/var/lib/reksicherung/artifacts` |
| `REKSICHERUNG_ARTIFACT_MAX_BYTES` | Optional size cap |

See `docs/inputs.md` and `docs/schemas/mcp-tools.schema.json`.
