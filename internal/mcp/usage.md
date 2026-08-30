# rdns-lookup MCP server

**Queries the free DNS index published by THC at ip.thc.org for the relationships around an IP or a domain: what else is hosted on an address, what subdomains a domain has, and who points a CNAME at a domain.** Only a third-party index is read, so no packet reaches the target under investigation — this is the safest opening move in a triage, before anything that resolves. No credentials are required; the service has no authentication mechanism.

**Its rdns data is not PTR.** Upstream calls it "reverse DNS", but it aggregates PTR names, domains whose A records point at the address, and names observed in Certificate Transparency logs. That is why `1.1.1.1` yields tens of thousands of records where a PTR lookup yields one. For the single authoritative name an address owner published, use the `doh-lookup` sibling instead.

## Tools

### `get_usage`

No arguments. Returns this manual.

### `lookup_rdns`

Domains associated with an IP address or IP block.

- `ip_address` (string, required) — an IP address, or a CIDR block **on an octet boundary** (`/8`, `/16`, `/24`, `/32`). Other prefix lengths are rejected before any request, because upstream answers them with HTTP 406.
- `limit` (integer, optional) — records to retrieve. Default 100, which costs exactly one upstream request.
- `all` (boolean, optional) — retrieve up to the 50,000-record ceiling. Costs more of the shared rate-limit budget.
- `tld` (array of string, optional) — TLD filter, e.g. `["com"]`. **Single addresses only.** Upstream silently ignores it for blocks, so passing it with a block is an `invalid_input` error rather than a filter that quietly does nothing.
- `apex_domain` (string, optional) — apex-domain filter, e.g. `example.com`. Single addresses only, for the same reason.
- `refresh` (boolean, optional) — bypass the local cache and re-query.

### `lookup_subdomains`

Subdomains of a domain. Each record carries `last_seen_on`, which is the freshness signal — an old date means the name was indexed once, not that it resolves today.

- `domain` (string, required) — domain name, IDN accepted.
- `limit`, `all`, `refresh` — as above.

### `lookup_cnames`

Domains that point a CNAME at the given target — the reverse of an ordinary CNAME lookup. Useful for finding who depends on a hosting target.

- `target_domain` (string, required) — the CNAME target to search for, e.g. `github.io`.
- `limit`, `all`, `refresh` — as above.

### `cache_status`

No arguments. Reports the cache directory, entry count, TTL, and the default and ceiling record limits.

## Result schema (lookup tools)

```json
{
  "kind": "rdns",
  "query": "142.251.43.46",
  "records": [
    {
      "domain": "bkk02s01-in-f14.1e100.net",
      "apex_domain": "1e100.net",
      "tld": "net",
      "ip_address": "142.251.43.46",
      "asn": "15169",
      "organization": "GOOGLE",
      "country": "US",
      "city": "Queens"
    }
  ],
  "count": 1,
  "matching_records": 110,
  "truncated": true,
  "truncation_note": "upstream holds 110 records; 100 retrieved. Raise --limit or pass --all for more.",
  "duplicates_removed": 0,
  "source": "ip.thc.org",
  "route": "json",
  "upstream_requests": 1,
  "cached": false,
  "fetched_at": "2026-07-30T05:20:00Z",
  "rate_limit": { "limit": 250, "remaining": 249, "rate_per_second": 0.5 }
}
```

- `records` — the normalized rows. Which fields are populated depends on the tool: `lookup_rdns` fills the enrichment above, `lookup_subdomains` fills `domain` and `last_seen_on`, and `lookup_cnames` fills `domain` alone.
- `count` — records returned, after deduplication.
- `matching_records` — upstream's **total** hit count. **0 means unknown, not none:** upstream declines to count very large blocks, and the CSV route reports no total at all. Read `count` for what you have.
- `truncated` / `truncation_note` — **check this before concluding a set is complete.** It is set whenever upstream holds more than was retrieved, or whenever completeness could not be proven. The note says how to get more.
- `duplicates_removed` — rows upstream returned more than once, dropped here. Around 15% on a `/24` is normal, and not a sign of anything wrong.
- `route` — which upstream face answered: `json` (paginates, up to 100 rows per request) or `csv` (up to 50,000 rows in one request, no pagination). Chosen automatically from the record count you asked for.
- `rate_limit` — the shared upstream budget. `remaining` of `-1` means upstream said nothing. Bulk work paces itself against this automatically.

### Sizing a result

Every record retrieved comes back inline; no file is written and no path comes back.

`limit` is therefore the knob that bounds a response: it is the number of records fetched from upstream, and every one of them is returned. One lookup can pull tens of thousands of rows with `all`, so ask for what you can hold.

`truncated` and `matching_records` are a different signal: the index holds more than you retrieved. Raise `limit` (or set `all`) to reach the rest.

## Error recovery

Tool errors are structured JSON: `{"code": ..., "message": ...}`.

| code | meaning | recovery |
|---|---|---|
| `invalid_input` | The target failed validation, or a filter was combined with a block. | Read the message: it names the specific problem. For a CIDR block, use `/8`, `/16`, `/24`, or `/32`. For `tld`/`apex_domain`, query a single address, or filter the returned records yourself. |
| `rate_limited` | The upstream budget is spent. | Wait for replenishment — roughly 0.5 requests per second, 250 burst — then retry. Do not loop immediately. |
| `network_error` | The request failed, or upstream returned an unexpected status. | Retry once; if it persists, ip.thc.org is likely down. There is no alternative source for this data. |

Finding nothing indexed is **not** an error: the tool returns a normal result with `count` of 0. That is a real answer about the target.

## Notes

- **No credentials.** ip.thc.org has no authentication mechanism; the rate limit is the only access control.
- **Caching.** Results are cached locally for 24 hours by default. The index is historical and changes slowly, so a repeated lookup of the same indicator costs nothing. Pass `refresh` to force a re-query.
- **Courtesy.** The service is free and asks not to be abused. That is why the default limit is 100, why `--all` stops at the ceiling, and why bulk runs pace themselves. Please do not defeat these.
- **Single source.** All data comes from ip.thc.org. No other provider publishes a comparable dataset for free, so if it goes away this server has no fallback.
- **IPv6.** Accepted, but upstream has effectively no IPv6 data: expect a `count` of 0 rather than an error.
- **OpSec.** Nothing here touches the target. Combining `lookup_subdomains` with a resolving tool (`doh-lookup`) does, so decide deliberately before taking that step.
