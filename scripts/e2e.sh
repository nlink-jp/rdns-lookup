#!/bin/sh
# e2e.sh — end-to-end smoke test driving the built rdns-lookup binary against
# the real ip.thc.org API. Network is required. This is the "run the shipped
# binary against real data" gate (complements the tagged Go live tests in
# e2e/). Run: make e2e  (or scripts/e2e.sh).
#
# ip.thc.org is a free service that asks not to be abused. This script makes
# about a dozen requests and keeps record counts small except where a large one
# is the point of the check. Do not add checks here casually.
#
# Exit 0 iff every check passes; non-zero (and a FAIL summary) otherwise.

set -u

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BIN="$ROOT/dist/rdns-lookup"

# Isolate the cache and config so a stale entry can never mask a regression and
# the user's real state is never read or written.
CACHE_DIR=$(mktemp -d 2>/dev/null || echo "/tmp/rdns-lookup-e2e.$$")
export RDNS_LOOKUP_CACHE_DIR="$CACHE_DIR"
export XDG_CONFIG_HOME="$CACHE_DIR/cfg"
WS="$CACHE_DIR/ws"
trap 'rm -rf "$CACHE_DIR"' EXIT

PASS=0
FAIL=0

if [ ! -x "$BIN" ]; then
  echo "[e2e] building $BIN ..."
  ( cd "$ROOT" && make build >/dev/null ) || { echo "[e2e] build failed"; exit 1; }
fi

# report <ok> <desc> <detail>
report() {
  if [ "$1" -eq 1 ]; then
    PASS=$((PASS + 1)); printf '  PASS  %s\n' "$2"
  else
    FAIL=$((FAIL + 1)); printf '  FAIL  %s\n' "$2"
    printf '%s\n' "$3" | sed 's/^/          /'
  fi
}

# A Google address with a moderate, stable index footprint (~110 records).
IP=142.251.43.46

echo "[e2e] rdns-lookup against live ip.thc.org"

# 1. version — brew test calls this, so it must answer and exit 0
OUT=$("$BIN" --version 2>&1); C=$?
if [ $C -eq 0 ] && echo "$OUT" | grep -q "rdns-lookup" && echo "$OUT" | grep -q "ip.thc.org"; then OK=1; else OK=0; fi
report "$OK" "--version prints the tool and its data source, exits 0" "exit=$C; $OUT"

# 2. rdns text output — provenance, counts, and enrichment all present
OUT=$("$BIN" rdns --refresh --limit 5 "$IP" 2>&1); C=$?
if [ $C -eq 0 ] \
   && echo "$OUT" | grep -q "\[rdns\]" \
   && echo "$OUT" | grep -q "source: ip.thc.org (json face)" \
   && echo "$OUT" | grep -q "rate budget" \
   && echo "$OUT" | grep -q "AS15169"; then OK=1; else OK=0; fi
report "$OK" "rdns text output carries provenance, budget, and AS enrichment" "exit=$C; $OUT"

# 3. truncation is visible in plain text, not only in JSON
OUT=$("$BIN" rdns --refresh --limit 5 "$IP" 2>&1); C=$?
if [ $C -eq 0 ] && echo "$OUT" | grep -q "TRUNCATED" && echo "$OUT" | grep -q "note:"; then OK=1; else OK=0; fi
report "$OK" "a capped result is marked TRUNCATED with an explanatory note" "exit=$C; $OUT"

# 4. subdomains — last_seen_on must reach the user (it is the freshness signal)
OUT=$("$BIN" subdomains --refresh --limit 5 github.com 2>&1); C=$?
if [ $C -eq 0 ] && echo "$OUT" | grep -q "github.com" && echo "$OUT" | grep -q "last seen 20"; then OK=1; else OK=0; fi
report "$OK" "subdomains returns names with last-seen dates" "exit=$C; $OUT"

# 5. cnames
OUT=$("$BIN" cnames --refresh --limit 5 github.io 2>&1); C=$?
if [ $C -eq 0 ] && echo "$OUT" | grep -q "\[cnames\]"; then OK=1; else OK=0; fi
report "$OK" "cnames returns reverse CNAME domains" "exit=$C; $OUT"

# 6. --all switches to the CSV face and its rotated columns are corrected:
#    every row's ip_address must be the queried address, never a domain.
OUT=$("$BIN" rdns --refresh --all --json "$IP" 2>&1); C=$?
BAD=$(printf '%s' "$OUT" | grep -c "\"ip_address\": \"$IP\"" 2>/dev/null || echo 0)
if [ $C -eq 0 ] \
   && echo "$OUT" | grep -q '"route": "csv"' \
   && [ "$BAD" -gt 10 ] \
   && ! echo "$OUT" | grep -q '"ip_address": "[a-z]'; then OK=1; else OK=0; fi
report "$OK" "--all uses the CSV face and maps its rotated columns correctly" "exit=$C; route/ip_address check failed"

# 7. JSON output carries the honesty fields
OUT=$("$BIN" rdns --refresh --limit 5 --json "$IP" 2>&1); C=$?
if [ $C -eq 0 ] \
   && echo "$OUT" | grep -q '"source": "ip.thc.org"' \
   && echo "$OUT" | grep -q '"matching_records"' \
   && echo "$OUT" | grep -q '"truncated"' \
   && echo "$OUT" | grep -q '"duplicates_removed"'; then OK=1; else OK=0; fi
report "$OK" "JSON output includes source, upstream total, truncation, dupes" "exit=$C; $OUT"

# 8. nothing indexed -> exit 1 (a real answer, not a failure)
OUT=$("$BIN" rdns --refresh --limit 5 203.0.113.9 2>&1); C=$?
if [ $C -eq 1 ] && echo "$OUT" | grep -q "nothing indexed"; then OK=1; else OK=0; fi
report "$OK" "an unindexed address exits 1 and says nothing was indexed" "exit=$C; $OUT"

# 9. a non-octet-boundary block is refused locally, before any request
OUT=$("$BIN" rdns 142.251.43.0/25 2>&1); C=$?
if [ $C -eq 2 ] && echo "$OUT" | grep -q "octet-boundary"; then OK=1; else OK=0; fi
report "$OK" "a /25 block exits 2 with the octet-boundary explanation" "exit=$C; $OUT"

# 10. a filter on a block is refused rather than silently ignored upstream
OUT=$("$BIN" rdns --tld com 142.251.43.0/24 2>&1); C=$?
if [ $C -eq 2 ] && echo "$OUT" | grep -q "ignores"; then OK=1; else OK=0; fi
report "$OK" "--tld with a block exits 2 and explains upstream ignores it" "exit=$C; $OUT"

# 11. bulk via stdin -> two results, paced against the budget
OUT=$(printf '%s\n# a comment\n%s\n' "$IP" 8.8.8.8 | "$BIN" rdns --refresh --limit 3 --json 2>&1); C=$?
N=$(printf '%s' "$OUT" | grep -c '"source":"ip.thc.org"')
if [ $C -eq 0 ] && [ "$N" -eq 2 ]; then OK=1; else OK=0; fi
report "$OK" "bulk stdin returns two JSONL results and skips comments" "exit=$C; lines=$N"

# 12. the cache serves a repeat instead of re-requesting
"$BIN" rdns --refresh --limit 5 "$IP" >/dev/null 2>&1
OUT=$("$BIN" rdns --limit 5 "$IP" 2>&1); C=$?
if [ $C -eq 0 ] && echo "$OUT" | grep -q "cached"; then OK=1; else OK=0; fi
report "$OK" "a repeated lookup is served from cache" "exit=$C; $OUT"

# 13. cache status reports the real settings
OUT=$("$BIN" cache status 2>&1); C=$?
if [ $C -eq 0 ] && echo "$OUT" | grep -q "ttl:" && echo "$OUT" | grep -q "ip.thc.org"; then OK=1; else OK=0; fi
report "$OK" "cache status reports the TTL and data source" "exit=$C; $OUT"

# 14. MCP stdio round-trip: initialize, every tool advertised, a real lookup
REQ='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"lookup_rdns","arguments":{"ip_address":"'$IP'","limit":3,"refresh":true}}}'
OUT=$(printf '%s\n' "$REQ" | "$BIN" mcp 2>/dev/null)
if echo "$OUT" | grep -q '"name":"rdns-lookup"' \
   && echo "$OUT" | grep -q '"lookup_subdomains"' \
   && echo "$OUT" | grep -q '"lookup_cnames"' \
   && echo "$OUT" | grep -q 'ip.thc.org'; then OK=1; else OK=0; fi
report "$OK" "MCP initialize + tools/list + lookup_rdns round-trip" "$OUT"

# 15. MCP spills a large result to a file instead of flooding the caller
mkdir -p "$WS"
REQ='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"lookup_rdns","arguments":{"ip_address":"'$IP'","all":true,"refresh":true,"workspace_root":"'$WS'"}}}'
OUT=$(printf '%s\n' "$REQ" | RDNS_LOOKUP_MCP_INLINE_MAX=10 "$BIN" mcp 2>/dev/null)
FILES=$(ls -1 "$WS"/*.jsonl 2>/dev/null | wc -l | tr -d ' ')
LINES=$(cat "$WS"/*.jsonl 2>/dev/null | wc -l | tr -d ' ')
if echo "$OUT" | grep -q 'records_file' && [ "$FILES" -eq 1 ] && [ "$LINES" -gt 10 ]; then OK=1; else OK=0; fi
report "$OK" "MCP writes a large result to a JSONL file under workspace_root" "files=$FILES lines=$LINES; $OUT"

# 16. MCP reports a bad target as a structured tool error
REQ='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"lookup_rdns","arguments":{"ip_address":"142.251.43.0/25"}}}'
OUT=$(printf '%s\n' "$REQ" | "$BIN" mcp 2>/dev/null)
if echo "$OUT" | grep -q '"isError":true' && echo "$OUT" | grep -q 'invalid_input'; then OK=1; else OK=0; fi
report "$OK" "MCP returns a structured invalid_input error for a /25 block" "$OUT"

echo "[e2e] $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
