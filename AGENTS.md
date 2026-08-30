# AGENTS.md — rdns-lookup

## What this is

A CLI and local MCP server that queries the free DNS index at [ip.thc.org](https://ip.thc.org) for the relationships around an IP or a domain: the domains associated with an address or octet-boundary block (`rdns`), the subdomains of a domain (`subdomains`), and the domains that CNAME to a domain (`cnames`). THC publishes roughly 6 billion records for free; the monthly bulk dump is 47GB compressed / 72GB uncompressed parquet, which is why this is an API client with a cache rather than a local dataset.

**The name is a compromise and the semantics matter.** Upstream calls its main endpoint "reverse DNS", but it is not PTR — it aggregates PTR names, domains whose A records point at the address, and Certificate Transparency names. `1.1.1.1` has one PTR record and 83,216 index records. Every user-facing surface (usage text, both READMEs, `internal/mcp/usage.md`, the MCP `instructions`) states this explicitly, because a user who assumes PTR will misread every result. The sibling `doh-lookup` is the tool for the authoritative single name.

Because only a third-party index is read, **no packet reaches the target under investigation** — the exact inverse of `doh-lookup`, and the reason this belongs early in a triage. Input is validated before any network I/O. Zero credentials (upstream has no authentication mechanism at all) and zero external dependencies. The relationship-breadth member of the cybersecurity-series lookup family alongside `asn-lookup`, `whois-lookup`, `abuse-lookup`, `tor-exit-lookup`, `icloud-relay-lookup`, `mac-lookup`, `doh-lookup`, and `urlscan-lookup`.

## Build & test

```bash
make build   # → dist/rdns-lookup  (NEVER `go build` directly — it drops the binary in the repo root)
make test    # go test -race -cover ./...   (fully offline)
make e2e     # live tests against the real API (network required)
make check   # lint + test + build-all
make verify-release  # gate: .notarized marker + freshness (run before upload)
```

Go 1.25.0, standard library only — `go.mod` has no `require` block. Shared code (the release scripts, `parseTOML`, `writeAtomic`, the `internal/mcp` skeleton, `internal/idn`) is vendored from sibling projects rather than imported, matching the series.

## Tests

Offline suite: an injected `thc.Doer` / `engine.Fetcher` for unit tests, plus an `httptest.Server` reached through `RDNS_LOOKUP_BASE_URL` for `internal/app` integration tests that exercise the whole flags → config → engine → thc → output path. Every test `t.Setenv`s `XDG_CONFIG_HOME` and the cache dir so the suite never touches real state, and points `RDNS_LOOKUP_BASE_URL` at an unreachable address where no request should happen at all.

Coverage: 72–96% per package, all ten packages tested. `internal/mcp/usage_test.go` holds three meta-tests that pin `usage.md` to the code — every tool name, every tool argument, and every error code must appear in the manual — because the manual is what an agent reads before its first call.

Live suite (`make e2e`, network required, excluded from `go test ./...` by the `e2e` build tag): 11 tagged Go tests in `e2e/live_test.go` plus 16 binary-level checks in `scripts/e2e.sh`. Both isolate the cache and `XDG_CONFIG_HOME` in a tempdir so a stale entry cannot mask a regression. Together they spend roughly 30 requests of upstream's 250-burst budget; **keep it that way** — small `--limit` values everywhere except the checks where a large result is the point.

`TestLiveCSVAndJSONFacesAgree` is the one to care about. It queries the same address through both upstream faces and compares every enrichment field record by record. A mock can only prove we parse bytes we wrote ourselves; this proves the positional CSV mapping matches reality, and it is what will catch upstream changing its column order. `TestLiveUpstreamStillRejectsNonOctetBoundary` plays the same role for our local `/25` restriction, asking upstream directly so a change on their side surfaces as a test failure rather than as docs drifting from reality.

## Layout

```
main.go                      Entry point; 23 lines, delegates to internal/app
internal/query/              Input validation gate — IP / octet-boundary CIDR / domain
internal/idn/                RFC 3492 punycode (vendored from doh-lookup)
internal/thc/                Upstream client: 3 JSON endpoints + 3 CSV endpoints
internal/cache/              Fixed-TTL JSON-file cache, atomic writes
internal/config/             Sectioned-TOML subset + RDNS_LOOKUP_* env
internal/engine/             Shared core: route selection, cache, dedup, truncation, pacing
internal/app/                CLI shell: subcommand dispatch, flags, text/JSON rendering
internal/mcp/                Zero-dep stdio JSON-RPC 2.0 server + embedded usage.md
```

## Key design decisions

- **Three subcommands, not one with auto-detection.** `doh-lookup` classifies its input and picks a lookup, but that cannot work here: given a domain, there is no way to tell whether the question is "enumerate subdomains" or "reverse CNAME lookup". Splitting by question removes the ambiguity. Reversing this would require a `--mode` flag on every invocation.
- **The JSON/CSV asymmetry is hidden in the engine.** The JSON face paginates but **silently caps `limit` at 100** (asking for 5000 returns 100, with no error); the CSV face returns up to 50,000 rows in one request and **cannot paginate**. `engine.fetch` picks the face from the requested count. Callers think only about record counts; the face that answered is reported as provenance. Leaking this choice into the CLI would make every caller re-derive it.
- **50,000 is a hard ceiling and truncation is always explicit.** Walking past the CSV limit through JSON pagination would take hundreds of requests at 0.5 req/sec. So `--all` stops there, and every result carries `matching_records` next to the retrieved count plus a `TRUNCATED` marker and a note. Silently returning a capped set is the failure mode this design exists to prevent.
- **Deduplication on by default.** Upstream emits the same `(domain, ip_address)` twice — once with an empty `tld`, once populated — for roughly 15% of rows on a `/24`. The richer variant survives so no field is lost, and the removed count is reported so the number is never mysterious.
- **Filters are refused on block queries.** Upstream accepts `tld`/`apex_domain` with a block and then ignores them, returning the unfiltered set. Passing them through would mean a caller believes they filtered when they did not, so `engine.LookupRDNS` errors instead.
- **Courtesy to a free service is in the code, not just the docs.** Upstream returns `"Free Service!, Do not abuse"` on every response. Hence the 24-hour cache TTL, the 100-record default, the `--all` ceiling, no retries, and `engine.pace` waiting for replenishment when `x-ratelimit-remaining` drops below `MinRemaining`. Do not "optimize" these away.
- **Single-source dependence is accepted, not abstracted.** No other provider publishes a comparable free dataset, so there is nothing to swap in; a provider abstraction would be dead weight. The dependence is stated in both READMEs and `usage.md` instead. Same trade-off as `urlscan-lookup` with urlscan.io.
- **The engine is the only clock reader** (`Engine.Now`) and `Engine.Sleep` is injected, which is what makes caching and rate-limit pacing deterministic in tests.
- **Fixed-TTL cache, not per-entry expiry.** Unlike `doh-lookup`, which honors each record's own DNS TTL, this upstream carries no freshness of its own, so the store records `fetched_at_unix` and the TTL is applied at read time — changing `ttl_hours` in config takes effect on entries already on disk.

## Gotchas

- **The upstream CSV header is rotated by one column.** It declares `ipAddress,apexDomain,subdomain,tld,country,city,asn,organization` while a row actually arrives as `apexDomain,subdomain,tld,country,city,asn,organization,ipAddress` — the IP is **last**, not first. Trusting the header mislabels every single field. Every CSV request therefore pins `hide_header=true` and rows are mapped positionally through `thc.csvColumns`. `TestParseCSVCorrectsRotatedHeader` guards this with real captured bytes; **never** change that mapping without re-verifying against the JSON face for the same query.
- **`matching_records: 0` means unknown, not none.** Upstream says so in a comment for very large blocks, and the CSV face reports no total at all. Code must read `Count` for what is held and treat 0 as "could not count".
- **Upstream's documented subdomains key is wrong.** The docs say `subdomains`; every real response uses `domains`. The observed name is what `internal/thc` decodes.
- **CIDR blocks must sit on an octet boundary.** `/8`, `/16`, `/24`, `/32` work; `/25` and `/29` return HTTP 406 `{"status":"error","error":"invalid ip"}`. `query.ClassifyAddress` rejects them locally with an explanation. A `/32` is canonicalized to the bare address so it shares a cache entry and counts as a single address for filter purposes.
- **Errors can arrive with HTTP 200.** An `{"status":"error","error":...}` envelope appears on 200 as well as 406, and the CSV face returns that JSON envelope despite the `text/csv` Accept. `internal/thc` checks both.
- **A zero `RateLimit.Remaining` is not the same as unknown.** `RateLimit{Limit:-1,Remaining:-1}` means upstream said nothing; `engine.fetch` must seed the result with that, or `pace` reads the zero value as "exhausted" and sleeps ~42s before the very first request of every lookup. This was a real bug caught by `TestPaceSkipsWhenBudgetHealthy`.
- **IPv6 returns 200 with zero records.** Not an error, just no data upstream. Do not turn it into one.
- **Exit-code contract:** `0` records found / `1` nothing indexed anywhere (a real answer) / `2` operational error. A partial failure across bulk targets still prints the successful results and then returns `2`.
- **Status:** RFP complete (`docs/{en,ja}/`), Phase 1 (core CLI) and Phase 2 (MCP) implemented and tested, offline and live suites green (`make test`, `make e2e`). Phase 3 remains: not yet pushed to GitHub, not added as a cybersecurity-series submodule, and no release.

## Roadmap (post-scaffold)

- Subdomain-takeover detection: resolve each `subdomains` result and flag CNAME targets that return NXDOMAIN. Deliberately left out of v1 — the composition currently belongs in the user's pipeline, and building it in would mean this tool starts touching targets.
- Certificate Transparency daily dumps (`cs2.ip.thc.org`, ~400MB gz/day) are explicitly out of scope: bulk data, not an API, and a different problem domain.
