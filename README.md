# rdns-lookup

**Look up an IP's domains, a domain's subdomains, and reverse CNAMEs — as a CLI and a local MCP server.**

When you investigate a suspicious IP or domain, the first thing you want is the surrounding relationships: what else is hosted on this address, what subdomains exist under this domain, who points a CNAME at it. `dig -x` cannot tell you — the PTR record is the one name the address owner published, not the tens of thousands of domains whose A records actually point there. `1.1.1.1` has a single PTR; this index holds **83,216** records for it. rdns-lookup queries the free 6-billion-record DNS index that [THC](https://ip.thc.org) publishes, and because it only reads a third-party index, **no packet reaches the target under investigation** — making it the safest opening move in a triage.

The relationship-breadth sibling of [asn-lookup](https://github.com/nlink-jp/asn-lookup) (attribution), [whois-lookup](https://github.com/nlink-jp/whois-lookup) (registration), [abuse-lookup](https://github.com/nlink-jp/abuse-lookup) (reputation), [doh-lookup](https://github.com/nlink-jp/doh-lookup) (current resolution), and [urlscan-lookup](https://github.com/nlink-jp/urlscan-lookup) (URL behaviour). **Zero credentials, zero external dependencies.**

> **This is not PTR.** Upstream calls it "reverse DNS", but it is an aggregate index of PTR names, domains whose A records point at the address, and names seen in Certificate Transparency logs. It answers *"what else is here"*. For the single authoritative name an owner published, use `doh-lookup`.

## Install

```bash
brew install nlink-jp/tap/rdns-lookup   # prebuilt, Developer ID signed + notarized, arm64 macOS
```

```bash
make build  # → dist/rdns-lookup
```

## Usage

```bash
# What else is hosted on this address?
rdns-lookup rdns 142.251.43.46

# An octet-boundary block works too (/8, /16, /24, /32 only)
rdns-lookup rdns 142.251.43.0/24

# Narrow a busy address by TLD or apex domain (single addresses only)
rdns-lookup rdns 142.251.43.46 --tld com,net
rdns-lookup rdns 142.251.43.46 --apex 1e100.com

# Enumerate subdomains; each record carries the date it was last seen
rdns-lookup subdomains github.com

# Who points a CNAME at this hosting target?
rdns-lookup cnames github.io

# More than the default 100 records, up to the 50000 ceiling
rdns-lookup rdns 1.1.1.1 --limit 500
rdns-lookup rdns 1.1.1.1 --all

# Machine-readable; JSONL when there are multiple targets
rdns-lookup rdns --json 142.251.43.46
rdns-lookup rdns --json 142.251.43.46 8.8.8.8

# Bulk input: arguments, a file, or stdin — paced against the rate limit
rdns-lookup rdns --input targets.txt
cut -d, -f2 alerts.csv | rdns-lookup rdns --json

# Cache
rdns-lookup cache status
rdns-lookup cache clear
```

Example:

```
$ rdns-lookup rdns 142.251.43.46 --limit 4
142.251.43.46  [rdns]  4 records of 110 upstream, TRUNCATED
  source: ip.thc.org (json face), 1 request, rate budget 249/250 left
  note: upstream holds 110 records; 4 retrieved. Raise --limit or pass --all for more.
  2rkkem2jzi.com  (142.251.43.46, AS15169 GOOGLE, Queens, US)
  5mpqvdddy7.com  (142.251.43.46, AS15169 GOOGLE, Queens, US)
  65nijzgbc3.com  (142.251.43.46, AS15169 GOOGLE, Queens, US)
  bkk02s01-in-f14.1e100.net  (142.251.43.46, AS15169 GOOGLE, Queens, US)
```

Every result states how many records upstream holds against how many were retrieved, so a partial answer is never mistaken for a complete one.

### Exit codes (rdns / subdomains / cnames)

| Code | Meaning |
|---|---|
| 0 | At least one target returned records |
| 1 | Every target returned nothing indexed (a real answer, not a failure) |
| 2 | Error — invalid input, network failure, bad config |

## MCP server

```bash
rdns-lookup mcp
```

Tools: `lookup_rdns`, `lookup_subdomains`, `lookup_cnames`, `cache_status`, `get_usage`. **Call `get_usage` first** — it returns the full reference, the result schema, and the error-recovery table. Tool errors are structured JSON (`{code, message}`); finding nothing indexed is a normal result, not an error. Every record retrieved is returned inline — the server writes no files and takes no path argument, so it works against a client with no filesystem of its own. `limit` is what bounds a response; `truncated` and `matching_records` tell you when the index holds more than you asked for.

**Arguments are checked strictly.** A call carrying an argument a tool does not declare fails with `invalid_input`, naming it — `arguments: json: unknown field "limitt"` — rather than running without it. A misspelt `limit` used to fall back to the default while the result read as the bounded set asked for. Each lookup tool takes only its own arguments, so `tld` and `apex_domain` are refused anywhere but `lookup_rdns`. Wrong-typed arguments are refused the same way, and nothing runs before the arguments decode, so a rejected call spends no upstream budget.

Register it with Claude Code:

```json
{
  "mcpServers": {
    "rdns-lookup": {
      "command": "rdns-lookup",
      "args": ["mcp"]
    }
  }
}
```

## Configuration

**Precedence: flag > environment variable > config file > built-in default.** The config file is optional; see [config.example.toml](config.example.toml).

| Setting | TOML | Env | Default |
|---|---|---|---|
| API root | `[api] base_url` | `RDNS_LOOKUP_BASE_URL` | `https://ip.thc.org/api/v1` |
| Default records | `[query] default_limit` | `RDNS_LOOKUP_DEFAULT_LIMIT` | `100` |
| `--all` ceiling | `[query] max_all` | `RDNS_LOOKUP_MAX_ALL` | `50000` |
| Deduplicate rows | `[query] dedup` | `RDNS_LOOKUP_DEDUP` | `true` |
| Cache TTL (hours) | `[cache] ttl_hours` | `RDNS_LOOKUP_CACHE_TTL_HOURS` | `24` |
| Cache directory | `[cache] dir` | `RDNS_LOOKUP_CACHE_DIR` | `~/.cache/rdns-lookup` |
| Network timeout | `[network] timeout_seconds` | `RDNS_LOOKUP_TIMEOUT_SECONDS` | `30` |
| Rate-limit floor | `[ratelimit] min_remaining` | `RDNS_LOOKUP_MIN_REMAINING` | `20` |

No credentials appear anywhere: ip.thc.org has no authentication mechanism.

## How it stays honest about what it found

An index this large makes it easy to mistake a capped answer for a complete one, so the tool works to prevent that:

- **The upstream total travels with every result.** `matching_records` sits next to the count actually retrieved, and a partial answer is flagged `TRUNCATED` with a note saying how to get more. A `matching_records` of 0 means *unknown* — upstream declines to count very large blocks — not *none*.
- **Duplicates are removed and counted.** Upstream returns the same domain twice (once with an empty `tld`, once populated) for roughly 15% of rows on a `/24`. They are dropped, the richer variant survives, and the number removed is reported.
- **Both upstream faces are used correctly.** The JSON face paginates but silently caps `limit` at 100; the CSV face returns up to 50,000 rows and cannot paginate. The engine picks between them from the record count you asked for, and the result says which answered.
- **Filters that would do nothing are refused.** Upstream accepts `--tld`/`--apex` on a block query and then ignores them, returning the unfiltered set. Combining them with a block is an error here instead.
- **Invalid targets never reach the network.** Only octet-boundary CIDR blocks exist upstream, so `/25` and `/29` are rejected locally with an explanation rather than costing a 406.

## Notes

- **Single source.** All data comes from ip.thc.org. No other provider publishes a comparable dataset for free, so if it goes away this tool loses its function. There is no fallback to configure.
- **Courtesy to a free service.** Upstream answers every request with *"Free Service!, Do not abuse"*. The 24-hour cache, the 100-record default, the `--all` ceiling, and the automatic pacing of bulk runs (which waits when the rate-limit budget drops low) are all there to honor that. The budget is 250 burst, refilling at 0.5/sec.
- **IPv6.** Accepted, but upstream has effectively no IPv6 data — expect zero records rather than an error.
- **Freshness.** `subdomains` results carry `last_seen_on`. An old date means the name was indexed once, not that it resolves today; confirm with `doh-lookup` if that matters.
- **Composing with resolution.** Piping `subdomains` output into `doh-lookup` and looking for CNAME targets that return NXDOMAIN surfaces subdomain-takeover candidates. That step *does* touch the target, so take it deliberately.

## Development

```bash
make build   # → dist/  (never `go build` directly)
make test    # go test -race -cover ./...
make e2e     # live tests against the real API (network required)
make check   # lint + test + build-all
```

## License

MIT — see [LICENSE](LICENSE).
