# Prompt: generate backup provider config

Follow `skills/reksicherung-configgen/SKILL.md` and `docs/inputs.md` (`I-CONFIGGEN`).

Start from the closest file under `examples/configgen/` (see that README table);
replace fictional OpenBao refs and hostnames. Do not invent plaintext secrets.

Slots:

- `provider`: `gobackup` or `resticprofile`
- `input_path`: path to write/read I-CONFIGGEN YAML
- `output_path`: generated provider YAML
- `bindings_path`: optional bindings JSON
- `storage`: `s3` \| `minio` \| `azure` \| `sftp` (sftp = gobackup only; azure auth mode per provider)

Use:

```bash
reksicherung configgen -provider <provider> -in <input_path> -out <output_path> -bindings <bindings_path>
```

Do not resolve or print secret values. After render, continue with
`reksicherung-add-source` if the catalog/OpenBao source is not registered yet.
