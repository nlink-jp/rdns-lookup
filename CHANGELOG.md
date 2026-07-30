# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-30

### Added

- Initial release. `rdns-lookup` queries the free DNS index at ip.thc.org for the relationships
  around an IP or a domain, as a CLI and a local MCP server. It reads only a third-party index,
  so no packet reaches the target under investigation.
- Three lookup commands, one per upstream relationship. `rdns <ip|cidr...>` returns the domains
  associated with an address or octet-boundary block; `subdomains <domain...>` enumerates a
  domain's subdomains; `cnames <domain...>` returns the domains that CNAME to a target. They are
  separate commands rather than one auto-detecting command because a domain input cannot
  distinguish subdomain enumeration from reverse CNAME lookup.
- Shared lookup flags: `--limit`, `--all`, `--tld`, `--apex` (the last two for `rdns` on a single
  address), `--json`, `--raw`, `--refresh`, `--timeout`, `--input`, and `-c/--config`. Bulk input
  via multiple arguments, a file, or stdin, with output as JSONL when several targets are given.
- Automatic selection between upstream's two faces. The JSON face paginates but silently caps
  `limit` at 100; the CSV face returns up to 50,000 rows and cannot paginate. The engine chooses
  from the requested record count and reports which face answered.
- Explicit truncation reporting. Every result carries upstream's `matching_records` next to the
  count actually retrieved, marks a partial answer `TRUNCATED`, and explains how to get more.
  A `matching_records` of 0 means unknown — upstream declines to count very large blocks — rather
  than none.
- Deduplication of upstream's repeated rows, on by default. The same `(domain, ip_address)` can
  arrive twice, once with an empty `tld` and once populated (roughly 15% of rows on a `/24`); the
  richer variant survives and the removed count is reported.
- Correction of upstream's rotated CSV header. It declares `ipAddress` first while the value
  arrives last, so every CSV request pins `hide_header=true` and columns are mapped positionally.
  Reading the declared header would mislabel every field.
- Validation before any network I/O: octet-boundary CIDR blocks only (`/8`, `/16`, `/24`, `/32` —
  upstream answers others with HTTP 406), RFC hostname checks with IDN punycode conversion, and a
  control-character gate. `--tld`/`--apex` combined with a block is rejected, because upstream
  accepts those filters for blocks and then silently ignores them.
- Rate-limit pacing for bulk runs, reading `x-ratelimit-remaining` from each response and waiting
  for replenishment below a configurable floor. Together with the 24-hour result cache, the
  100-record default, and the `--all` ceiling, this honors a free service that asks not to be
  abused.
- Local MCP server (`rdns-lookup mcp`, stdio JSON-RPC 2.0, no SDK) exposing `lookup_rdns`,
  `lookup_subdomains`, `lookup_cnames`, `cache_status`, and `get_usage`, with structured
  `{code, message}` tool errors. Results above 200 records are written as JSONL under
  `workspace_root` and only the path plus a count summary is returned, keeping an agent's context
  intact.
- `cache status` and `cache clear`, with a fixed-TTL cache under `$XDG_CACHE_HOME/rdns-lookup`
  whose TTL is applied at read time, so changing it in config affects entries already on disk.
- Configuration via an optional sectioned TOML file and `RDNS_LOOKUP_*` environment variables,
  with precedence flag > env > file > default. No credentials anywhere: ip.thc.org has no
  authentication mechanism.
- Exit-code contract for the lookup commands: `0` records found, `1` nothing indexed for any
  target (a successful answer, not a failure), `2` usage, validation, or network error.

[0.1.0]: https://github.com/nlink-jp/rdns-lookup/releases/tag/v0.1.0
