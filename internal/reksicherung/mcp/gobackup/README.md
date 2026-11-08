# GoBackup object discovery

This provider-neutral service discovers archive objects through
`storage.Store`; it does not add an MCP transport or depend on GoBackup's
runtime.

## Prefix and backup IDs

Configure a non-empty, safe object-key prefix ending in `/`, for example
`production/sqlserver/`. The prefix is scoped to the object store already
configured by the supplied `storage.Store`; this package does not select a
bucket or endpoint. Prefixes and selected IDs are relative object keys:
absolute paths, `.`/`..` path segments, empty segments, backslashes, invalid
UTF-8, and control characters are rejected.

`List` scans every page under that prefix and returns metadata only. A backup's
`ID` is the exact object key with the configured prefix removed, so an object
at `production/sqlserver/daily/db.bacpac` has ID `daily/db.bacpac`. Pass that
ID to `Select` for metadata lookup or `Open` to stream the exact object.
Directory-marker keys ending in `/` are ignored. `Latest` is the object with
the greatest `LastModified`; equal timestamps are resolved by ascending ID
lexical order. This is object-store ordering, not an interpretation of
timestamps encoded in GoBackup filenames.

Listing is bounded by page size, total pages, and total listed-object count
(including directory markers). Repeated continuation tokens, duplicate keys,
unsafe keys, and limit exhaustion are errors; the service never returns a
partial inventory as complete. `Open` does not buffer the archive in memory and
limits data delivered to the caller to `MaxObjectBytes` (1 GiB by default).

## Scope and limitations

The service treats each safe object as one candidate archive. It does not
parse GoBackup manifests, correlate multipart/split artifacts, or assemble
parts; a split backup may therefore appear as multiple independent objects and
must not be assumed restorable from one selected ID. It also does not invoke a
GoBackup-native restore operation. `Open` only supplies the selected object
stream to a separate restore implementation.
