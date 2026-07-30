# RFP: rdns-lookup

> Generated: 2026-07-30
> Status: Draft

## 1. Problem Statement

When a CTI/IR practitioner investigates a suspicious IP or domain, the first thing they want to know is
the **surrounding relationships**: what else is hosted on this IP, what subdomains exist under this domain,
and who points a CNAME at this domain. Existing tooling cannot answer any of these. The PTR record returned
by `dig -x` or by our sibling `doh-lookup` is just "the one name the IP's owner published" — it does not
reveal the tens of thousands of domains whose A records actually point at that IP (measured: `1.1.1.1` has
a single PTR, `one.one.one.one`, but **83,216 records** in the index). Subdomain enumeration and reverse
CNAME lookup have no in-house tool at all. Commercial passive DNS services (VirusTotal, SecurityTrails,
Censys) hold comparable data but are too expensive for routine daily use.

**rdns-lookup queries the 6-billion-record DNS index that THC publishes free of charge (ip.thc.org) for
three kinds of relationship: IP → domains, domain → subdomains, and domain → domains that CNAME to it.**
It is a CLI and a local MCP server. Because it only reads a third-party index, it **sends no packets to the
target under investigation** — the exact opposite of `doh-lookup`, which actually resolves. That makes it
the safest possible opening move in a triage. The target user is the CTI/IR practitioner who wants the
relationship graph around a suspicious IP or domain without sacrificing OpSec. It is a cybersecurity-series
sibling focused on **breadth of relationships**, alongside `asn-lookup` (attribution), `whois-lookup`
(registration), `abuse-lookup` (reputation), and `doh-lookup` (current resolution).

## 2. Functional Specification

### Commands / API Surface

**CLI subcommands.** Three subcommands mapping one-to-one onto the three upstream endpoints. When a domain
is supplied there is no way to tell from the input alone whether the question is "enumerate subdomains" or
"reverse CNAME lookup", so `doh-lookup`'s input-classification approach does not work here.

- `rdns-lookup rdns <ip|cidr...>` — domains associated with an IP or IP block
  - `--limit N` — number of results (default 100)
  - `--all` — fetch everything up to the 50,000-record ceiling described below
  - `--tld com,net` — filter by TLD (**single IP only**)
  - `--apex example.com` — filter by apex domain (**single IP only**)
- `rdns-lookup subdomains <domain...>` — enumerate subdomains of a domain
  - `--limit N` / `--all`
- `rdns-lookup cnames <domain...>` — domains that CNAME to the given domain
  - `--limit N` / `--all`
- `rdns-lookup cache <status|clear>` — show or clear the cache
- `rdns-lookup mcp` — start the local MCP server (stdio)
- `rdns-lookup --version`

Common to every lookup subcommand: `--json` (JSONL when bulk), `--raw` (upstream response verbatim),
`--refresh` (bypass cache), `-c, --config`, and bulk input via multiple positional arguments, stdin, or
`--input <file>`.

**MCP tools:**

- `lookup_rdns` — `{ ip_address, limit?, all?, tld?, apex_domain?, workspace_root? }`
- `lookup_subdomains` — `{ domain, limit?, all?, workspace_root? }`
- `lookup_cnames` — `{ target_domain, limit?, all?, workspace_root? }`
- `cache_status` — cache statistics
- `get_usage` — tool reference and error-recovery table

### Input / Output

- **Validation gate, before any network I/O:**
  - `rdns`: an IP address, or a **CIDR block on an octet boundary only** (`/8`, `/16`, `/24`, `/32`).
    Upstream rejects `/25` and `/29` with HTTP 406, so reject them locally with an explicit error
    (CLI exit 2, MCP `{code:"invalid_input"}`).
  - `subdomains` / `cnames`: RFC hostname validation (≤253 total, labels 1–63 LDH, dot required, control
    characters and CRLF rejected). IDN is converted to punycode (porting `doh-lookup`'s RFC 3492
    implementation).
  - **`--tld` / `--apex` are permitted only for a single IP.** Upstream *silently ignores* these filters
    for IP blocks rather than erroring, so passing them through would produce "you thought you filtered but
    got everything". The CLI errors on the combination instead.
- **Route selection is internal and invisible to the user.** The default (up to 100 records) uses one JSON
  API call; `--all` or `--limit > 100` uses the CSV API (50,000 records per request). This switch is
  mandatory because the JSON API silently caps `limit` at 100.
- **50,000 records is the effective ceiling.** The CSV API has no pagination, and following the remainder
  through JSON pagination would take hundreds of requests under the 0.5 req/sec limit. `--all` stops at
  50,000 and always reports both the upstream `matching_records` (total hits) and the number actually
  retrieved, so **truncation is always visible**. Use `--tld` / `--apex` to narrow further.
- **Normalization, centralized in the engine:**
  - **Deduplication is on by default.** Upstream sometimes returns the same `(domain, ip_address)` twice,
    once with `tld:""` and once with `tld:"net"` (measured: 85 unique out of 100 rows for a `/24`, roughly
    15% duplicates).
  - **CSV is always fetched with `hide_header=true` and read with our own column mapping.** The upstream
    CSV header is rotated by one column — it declares `ipAddress,apexDomain,subdomain,...` while the data
    is `apexDomain,subdomain,...,ipAddress` — so trusting the header mislabels every field.
  - The `last_seen_on` field on each `subdomains` row is preserved in the output; it is useful evidence of
    freshness.
- **Output metadata:** every result states the **data source (ip.thc.org), the route taken (json / csv),
  the fetch time, `matching_records` vs records retrieved, whether truncation occurred, and the remaining
  rate-limit budget** — the same philosophy as `doh-lookup` always naming the resolver it used.
- **Terminology, stated explicitly:** what upstream calls "reverse DNS" is not PTR. It is an aggregate index
  of PTR names, domains whose A records point at the IP, and names sourced from CT logs. This distinction is
  spelled out in the output header, `usage.md`, and both READMEs so it is never confused with
  `doh-lookup`'s PTR.
- **Output formats:** human-readable (default; one section per target when bulk), `--json` (JSONL when
  bulk), `--raw`.
- **Exit-code contract (lookup subcommands):** `0` at least one hit; `1` zero hits across all targets
  (a successful answer that nothing is indexed); `2` usage, validation, or network error.
- **Large MCP results go through a file.** Above a threshold (default 200 records) the result is written as
  JSONL under `workspace_root` and only the path plus a count summary is returned — the same approach as
  `urlscan-lookup`'s `get_screenshot` and `abuse-lookup`'s `get_reports`. This keeps the agent's context
  from overflowing.
- **Bulk input must implement rate-limit pacing.** Read `x-ratelimit-remaining` from every response and
  wait for replenishment when it drops below a floor (default 20). No aggressive retries.

### Configuration

`~/.config/rdns-lookup/config.toml` (sectioned TOML, optional), overridden by `RDNS_LOOKUP_*` environment
variables. Precedence is **flag > env > config > built-in default**.

```toml
[api]
# base_url = "https://ip.thc.org/api/v1"

[query]
# default_limit = 100      # when neither --limit nor --all is given
# max_all = 50000          # ceiling for --all (the upstream CSV API limit)
# dedup = true             # drop upstream duplicate rows

[cache]
# ttl_hours = 24           # a historical index; it changes slowly
# dir = "~/.cache/rdns-lookup"

[network]
# timeout_seconds = 30

[ratelimit]
# min_remaining = 20       # wait for replenishment below this
```

### External Dependencies

- **None** (Go standard library only). `net/http` + `encoding/json` + `encoding/csv` suffice.
- The only external service is **ip.thc.org**. **No credentials or API key** — the service has no
  authentication mechanism at all.

## 3. Design Decisions

- **Go, zero dependencies.** The series standard (identical to asn / abuse / tor / whois / icloud-relay /
  doh / mac). It ships as a single signed binary and lets the CLI and the MCP server share one executable.
- **API calls plus a cache; no local copy of the bulk data.** Upstream publishes the full dataset monthly,
  but the current release is 47GB compressed / 72GB uncompressed parquet across 6 billion records, and even
  a single-record lookup would not return in a practical time. The API needs no authentication and answers
  immediately, so a cache in front of it is sufficient.
- **Three separate subcommands.** Because a domain input is ambiguous between subdomain enumeration and
  reverse CNAME lookup, `doh-lookup`'s auto-classification is not viable. Splitting by question removes the
  ambiguity.
- **The JSON/CSV split is hidden inside the engine.** Users think only about record counts; the engine
  decides which endpoint to call. The upstream asymmetry (JSON capped at 100, CSV at 50,000) stays an
  implementation detail.
- **Single-source dependence is accepted explicitly.** No other provider publishes a dataset of this scale
  for free, so if ip.thc.org stops, the tool stops working. We will not build an abstraction layer for
  alternative sources — there is nothing to swap in — and instead state the single-source dependence
  plainly in README and AGENTS.md. This is the same trade-off `urlscan-lookup` makes with urlscan.io.
- **Courtesy toward a free service is built into the design.** Upstream returns
  `"comment":"Free Service!, Do not abuse"` on every response. The 24-hour default cache, mandatory
  rate-limit pacing for bulk runs, the ban on aggressive retries, and the 50,000-record ceiling on `--all`
  are all implementations of that stance.
- **The engine is shared by the CLI and MCP** so their behaviour cannot diverge. The HTTP client is an
  injected interface, mocked in tests.
- **Relationship to sibling tools:** as the counterpart to `doh-lookup` (current resolution, actually
  queries the target), this tool covers **relationship lookup that never touches the target**. Enrichment
  of the returned domains and IPs is delegated to `asn-lookup` (attribution), `whois-lookup` (registration),
  and `abuse-lookup` (reputation) — UNIX philosophy. Piping `subdomains` output into `doh-lookup` and
  looking for CNAME targets that return NXDOMAIN surfaces subdomain-takeover candidates, but **that
  composition is left to the user's pipeline rather than built in**.
- **Deliberately out of scope:**
  - **Daily CT log dumps** (`cs2.ip.thc.org`, roughly 400MB gz per day) — bulk data rather than an API, and
    a different problem domain. Excluded from this scope.
  - **Local querying of the monthly bulk parquet/CSV** — rejected for the reason above.
  - **IPv6** — upstream has effectively no data (it returns 200 but always zero records). Input is accepted
    and the empty result is explained in the output.
  - **Enrichment of IPs and domains** (AS, reputation, geo, registration) — delegated to sibling tools.
  - **Automatic subdomain-takeover detection** — a v2 candidate.
  - Exhaustive retrieval beyond 50,000 records — impractical under the rate limit.

## 4. Development Plan

### Phase 1: Core (CLI) — independently reviewable

- `internal/query`: input classification (IP / CIDR / domain) plus the validation gate (octet-boundary CIDR
  only, RFC hostname checks); IDN punycode ported from `doh-lookup`
- `internal/config`: sectioned TOML + `RDNS_LOOKUP_*` env + flags, applying the precedence rule
- `internal/cache`: fixed-TTL cache following `whois-lookup` (`fetched_at_unix` with `Get(key, now, ttl)`),
  atomic writes
- `internal/thc`: the upstream client — three JSON endpoints and three CSV endpoints, injected HTTP
  interface, `hide_header=true` pinned with our own column mapping, `x-ratelimit-*` parsing, structured
  interpretation of 406 errors
- `internal/engine`: validate → route selection (json / csv) → cache → fetch → normalize (dedup,
  `matching_records` and truncation detection) → rate-limit pacing
- `internal/app`: the `rdns` / `subdomains` / `cnames` / `cache` subcommands, `--limit`/`--all`/`--tld`/
  `--apex`/`--json`/`--raw`/`--refresh`, bulk input (multiple arguments plus stdin/`--input`), output
  metadata, exit-code contract
- A full table-driven test suite against a mocked HTTP client, plus `httptest.Server` integration tests
  covering the whole CLI path

### Phase 2: Features (MCP) — independently reviewable

- `internal/mcp`: zero-dependency stdio JSON-RPC 2.0 with the tools `lookup_rdns` / `lookup_subdomains` /
  `lookup_cnames` / `cache_status` / `get_usage`, and structured `{code, message}` errors
- `internal/workspace`: JSONL output for above-threshold results (`workspace_root`, containment via
  `os.OpenRoot`)
- `usage.md` plus the two meta-tests that pin tool names and error codes to the documentation

### Phase 3: Release

- README.md / README.ja.md / CHANGELOG.md / AGENTS.md / config.example.toml / LICENSE
- Makefile and scripts (codesign / notarize / brew), build-all (linux amd64/arm64, darwin arm64,
  windows amd64), darwin signing and notarization, homebrew-tap formula
- Live E2E (`//go:build e2e` plus `scripts/e2e.sh`)
- Submodule integration → org profile and web-site catalog sync → `check-org.sh`

## 5. Required API Scopes / Permissions

**None.** The ip.thc.org API has no authentication mechanism, so no API key, OAuth scope, or IAM role is
required. The rate limit is the only effective access control (per-IP, 250 burst plus 0.5/sec replenishment).

## 6. Series Placement

Series: **cybersecurity-series**
Reason: it is a CTI/IR support tool that collects the relationships around a suspicious IP or domain without
touching the target, belonging to the same "CLI plus MCP, zero credentials, zero dependencies investigative
lookup" family as `asn-lookup`, `abuse-lookup`, `tor-exit-lookup`, `whois-lookup`, `icloud-relay-lookup`,
`doh-lookup`, and `mac-lookup`.

## 7. External Platform Constraints

Upstream behaviour was verified empirically on 2026-07-30. The following includes behaviour that is not
documented.

- **Rate limit:** `x-ratelimit-limit: 250` (burst) plus `x-ratelimit-rate: 0.50` (req/sec replenishment),
  with the remaining budget in `x-ratelimit-remaining`. Per-IP. Every response body carries
  `"comment":"Free Service!, Do not abuse"`.
- **The JSON API silently caps `limit` at 100.** Both 1000 and 50000 return exactly 100 records. Pagination
  uses the opaque `next_page_state` token, passed back as `page_state`.
- **The CSV API's `limit` goes to 50,000** and has no pagination mechanism.
- **The CSV header row is rotated by one column.** It declares
  `ipAddress,apexDomain,subdomain,tld,country,city,asn,organization` while the data is
  `apexDomain,subdomain,tld,country,city,asn,organization,ipAddress` (`ipAddress` last). Always pass
  `hide_header=true` and use our own mapping.
- **Duplicate records occur.** The same `(domain, ip_address)` can appear twice, once with `tld:""` and once
  with `tld:"net"` — 85 unique out of 100 rows for a `/24`, roughly 15%.
- **CIDR blocks must fall on octet boundaries.** `/8`, `/16`, `/24`, and `/32` work; `/25` and `/29` return
  HTTP 406 `{"status":"error","error":"invalid ip"}`.
- **Adding `tld` / `apex_domain` filters to a CIDR query is silently ignored rather than rejected.** The
  documentation only says the filters "cannot be used" for blocks; it does not make the request fail.
- **The documented response schema does not match the implementation.** For subdomains the docs say the key
  is `subdomains`, but it is actually `domains`. Match the observed behaviour if decoding strictly.
- **IPv6 has effectively no data.** Requests return 200 with `domains:[]`, `matching_records:0`, and the
  comment `"Could not fetch result count, matching_records will be zero, this can happen for /8 blocks"`.
- An absence of data is 200 with an empty array, not an error. Invalid input is 406 with
  `{"status":"error","error":"..."}`.
- `matching_records` carries the total hit count, which is usable for truncation detection and volume
  estimation.
- **Single-source dependence:** no alternative provider publishing a dataset of this scale for free has been
  identified. If upstream shuts down, the tool loses its function.

---

## Discussion Log

- **Origin:** the user found ip.thc.org and proposed adding it to the lookup series (CLI + MCP). Judging
  that querying the 60GB-plus bulk dataset locally would be impractical (minutes even for a single-record
  lookup), they proposed **API calls plus a cache**. Investigation confirmed the current bulk release is
  47GB compressed / 72GB uncompressed parquet across 6 billion records, supporting that judgement.
- **Hands-on API investigation:** the docs are a SPA, so WebFetch returned nothing usable and the pages had
  to be rendered in a browser. Beyond the three pages the user cited (reverse-dns / subdomain / cname), this
  revealed **three additional CSV download APIs over the same data**, with a `limit` ceiling of 50,000.
- **Decision to build a new project rather than extend `doh-lookup`:** what THC calls "reverse DNS" is not
  PTR but an aggregate index of every domain observed to be associated with the IP. Measured, `1.1.1.1` has
  one PTR record against 83,216 index records. The questions answered differ, as does the nature of the data
  source (historical index versus current authoritative answer), so this becomes an independent sibling.
- **Naming:** `rdns-lookup` was compared against `thc-lookup` (the vendor-named approach of
  `urlscan-lookup`). The single-source dependence is real, but `rdns-lookup` makes the function obvious in
  the catalog and was chosen, with the agreement that the "if upstream stops, the tool stops" caveat is
  stated in README and AGENTS.md.
- **CT logs agreed out of scope:** the daily dumps at `cs2.ip.thc.org` (roughly 400MB gz per day) are bulk
  data rather than an API and belong to a different problem domain.
- **Nine undocumented pitfalls found empirically** (recorded in section 7). Four drove design directly:
  (a) the JSON `limit` cap of 100, (b) the rotated CSV header, (c) roughly 15% duplicate records, and
  (d) the octet-boundary CIDR restriction together with the silently ignored filters. The probe results cost
  rate-limit budget to obtain and were saved to memory (`reference_ip_thc_org_api`).
- **Four functional decisions, all taking the recommended option:**
  - **Three separate subcommands** (`rdns` / `subdomains` / `cnames`), since a domain input cannot
    distinguish subdomain enumeration from reverse CNAME lookup and `doh-lookup`'s auto-classification
    therefore does not hold.
  - **Default 100 records with `--all` for everything**, because `1.1.1.1` yields 83,216 hits and an
    implicit full fetch would burn through the rate limit.
  - **Large MCP results go through a file above a threshold** (`urlscan-lookup`'s `workspace_root` pattern).
  - **Bulk input in v1 with mandatory pacing** — automatic pacing driven by `x-ratelimit-remaining`, so the
    tool stays courteous toward a free service.
- **Handling the 50,000 ceiling:** the CSV API has no pagination, and chasing 83,216 records through JSON
  pagination would need hundreds of requests (over ten minutes) at 0.5 req/sec, which is impractical.
  `--all` therefore stops at 50,000 and reports `matching_records` alongside the retrieved count to make
  truncation explicit; `--tld` / `--apex` are the way to narrow.
- **Cache TTL:** a historical index changes slowly, so the default is 24 hours, using the same hour-based
  setting as `whois-lookup`. Shortening it was considered because upstream `last_seen_on` values are recent
  (2026-07-17 on a `github.com` subdomain), but **the upstream dataset itself is not updated that quickly**,
  so 24 hours was confirmed as the default.
- **RFP confirmed:** with the TTL decision above, all seven items are settled. Proceeding to Phase 2
  (scaffolding).
