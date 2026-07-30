//go:build e2e

// Live end-to-end tests against the real ip.thc.org API. Network is required.
// Run with: make e2e  (or go test -tags e2e ./e2e/...).
package e2e

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/nlink-jp/rdns-lookup/internal/config"
	"github.com/nlink-jp/rdns-lookup/internal/engine"
	"github.com/nlink-jp/rdns-lookup/internal/thc"
)

// liveEngine builds an engine on the built-in defaults with an isolated cache,
// so repeated runs are hermetic and a stale entry can never mask a regression.
func liveEngine(t *testing.T) *engine.Engine {
	t.Helper()
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.toml"), 0)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.CacheDir = t.TempDir()
	return engine.New(cfg, "e2e")
}

// A Google address with a moderate, stable index footprint (~110 records):
// enough to exercise pagination and truncation, small enough that --all is one
// cheap CSV request.
const liveIP = "142.251.43.46"

func TestLiveRDNS(t *testing.T) {
	e := liveEngine(t)
	res, err := e.LookupRDNS(liveIP, engine.Options{Limit: 5, Refresh: true})
	if err != nil {
		t.Fatalf("rdns %s: %v", liveIP, err)
	}
	if res.Count == 0 {
		t.Fatalf("no records for %s", liveIP)
	}
	if res.Source != "ip.thc.org" || res.Route != string(thc.RouteJSON) {
		t.Errorf("provenance = %s/%s, want ip.thc.org/json", res.Source, res.Route)
	}
	// Upstream reports the real total, which must exceed a 5-record request —
	// this is what makes the truncation reporting meaningful.
	if res.MatchingRecords <= res.Count {
		t.Errorf("matching_records = %d, want more than the %d retrieved", res.MatchingRecords, res.Count)
	}
	if !res.Truncated || res.TruncationNote == "" {
		t.Error("a 5-record slice of a larger set must be flagged truncated with a note")
	}
	for _, rec := range res.Records {
		if rec.Domain == "" {
			t.Errorf("record with no domain: %+v", rec)
		}
		if rec.IPAddress != liveIP {
			t.Errorf("record ip_address = %q, want %q: %+v", rec.IPAddress, liveIP, rec)
		}
	}
	t.Logf("rdns: %d of %d records, budget %d/%d", res.Count, res.MatchingRecords,
		res.RateLimit.Remaining, res.RateLimit.Limit)
}

func TestLiveSubdomains(t *testing.T) {
	e := liveEngine(t)
	res, err := e.LookupSubdomains("github.com", engine.Options{Limit: 5, Refresh: true})
	if err != nil {
		t.Fatalf("subdomains github.com: %v", err)
	}
	if res.Count == 0 {
		t.Fatal("no subdomains for github.com")
	}
	// last_seen_on is the freshness signal; losing it would silently strip the
	// only way a user can judge whether a name is current.
	var haveLastSeen bool
	for _, rec := range res.Records {
		if rec.LastSeen != "" {
			haveLastSeen = true
			if _, err := time.Parse("2006-01-02", rec.LastSeen); err != nil {
				t.Errorf("last_seen_on %q is not a date: %v", rec.LastSeen, err)
			}
		}
	}
	if !haveLastSeen {
		t.Error("no record carried last_seen_on")
	}
	t.Logf("subdomains: %d of %d records", res.Count, res.MatchingRecords)
}

func TestLiveCNAMEs(t *testing.T) {
	e := liveEngine(t)
	res, err := e.LookupCNAMEs("github.io", engine.Options{Limit: 5, Refresh: true})
	if err != nil {
		t.Fatalf("cnames github.io: %v", err)
	}
	if res.Count == 0 {
		t.Fatal("no reverse CNAMEs for github.io")
	}
	for _, rec := range res.Records {
		if rec.Domain == "" {
			t.Errorf("record with no domain: %+v", rec)
		}
	}
	t.Logf("cnames: %d of %d records", res.Count, res.MatchingRecords)
}

// The load-bearing live test. Upstream's CSV header is rotated by one column
// relative to its data, so the CSV face is read positionally from a hardcoded
// schema. A mock can only prove we match bytes we wrote ourselves; this proves
// the two faces agree on real data, field by field, for the same query. If
// upstream ever changes its column order, this is what catches it.
func TestLiveCSVAndJSONFacesAgree(t *testing.T) {
	e := liveEngine(t)

	viaJSON, err := e.LookupRDNS(liveIP, engine.Options{Limit: 50, Refresh: true})
	if err != nil {
		t.Fatalf("json face: %v", err)
	}
	viaCSV, err := e.LookupRDNS(liveIP, engine.Options{All: true, Refresh: true})
	if err != nil {
		t.Fatalf("csv face: %v", err)
	}
	if viaJSON.Route != string(thc.RouteJSON) || viaCSV.Route != string(thc.RouteCSV) {
		t.Fatalf("routes = %s/%s, want json/csv", viaJSON.Route, viaCSV.Route)
	}
	if viaCSV.Count == 0 || viaJSON.Count == 0 {
		t.Fatal("both faces must return records")
	}

	byDomain := map[string]thc.Record{}
	for _, rec := range viaCSV.Records {
		byDomain[rec.Domain] = rec
	}

	compared := 0
	for _, j := range viaJSON.Records {
		c, ok := byDomain[j.Domain]
		if !ok {
			// The CSV set is the larger one, so a JSON domain missing from it
			// would mean the CSV rows were parsed into the wrong fields.
			t.Errorf("domain %q present via json but absent via csv", j.Domain)
			continue
		}
		compared++
		if c.IPAddress != j.IPAddress {
			t.Errorf("%s: ip_address json=%q csv=%q — CSV columns are mismapped", j.Domain, j.IPAddress, c.IPAddress)
		}
		if c.ApexDomain != j.ApexDomain {
			t.Errorf("%s: apex_domain json=%q csv=%q", j.Domain, j.ApexDomain, c.ApexDomain)
		}
		if c.ASN != j.ASN {
			t.Errorf("%s: asn json=%q csv=%q", j.Domain, j.ASN, c.ASN)
		}
		if c.Org != j.Org {
			t.Errorf("%s: organization json=%q csv=%q", j.Domain, j.Org, c.Org)
		}
		if c.Country != j.Country || c.City != j.City {
			t.Errorf("%s: location json=%q/%q csv=%q/%q", j.Domain, j.Country, j.City, c.Country, c.City)
		}
	}
	if compared == 0 {
		t.Fatal("compared no records; the faces share no domain, which cannot be right")
	}
	t.Logf("faces agree on %d records (json %d, csv %d, %d duplicates removed)",
		compared, viaJSON.Count, viaCSV.Count, viaCSV.Duplicates)
}

// Upstream returns the same (domain, ip) twice for a noticeable share of rows.
// This confirms the duplicates are real and that removing them does not leave
// the count above the upstream total.
func TestLiveDeduplication(t *testing.T) {
	e := liveEngine(t)
	res, err := e.LookupRDNS(liveIP, engine.Options{All: true, Refresh: true})
	if err != nil {
		t.Fatalf("rdns --all: %v", err)
	}
	if res.Count+res.Duplicates > res.MatchingRecords && res.MatchingRecords > 0 {
		t.Errorf("retrieved %d+%d exceeds the upstream total %d",
			res.Count, res.Duplicates, res.MatchingRecords)
	}
	seen := map[string]bool{}
	for _, rec := range res.Records {
		k := rec.Domain + "|" + rec.IPAddress
		if seen[k] {
			t.Errorf("duplicate survived deduplication: %s", k)
		}
		seen[k] = true
	}
	t.Logf("--all: %d records, %d duplicates removed, upstream total %d",
		res.Count, res.Duplicates, res.MatchingRecords)
}

// A filter narrows a single address. Upstream also populates tld only when the
// filter is used, which is the quirk behind the duplicate rows.
func TestLiveTLDFilter(t *testing.T) {
	e := liveEngine(t)
	res, err := e.LookupRDNS(liveIP, engine.Options{Limit: 10, TLDs: []string{"net"}, Refresh: true})
	if err != nil {
		t.Fatalf("rdns --tld net: %v", err)
	}
	if res.Count == 0 {
		t.Fatal("no .net records; expected at least the 1e100.net name")
	}
	for _, rec := range res.Records {
		if rec.TLD != "net" {
			t.Errorf("record %q has tld %q, want net — the filter was ignored", rec.Domain, rec.TLD)
		}
	}
	t.Logf("--tld net: %d of %d records", res.Count, res.MatchingRecords)
}

// Nothing indexed is a successful answer with its own sentinel, not a failure.
// 203.0.113.0/24 is TEST-NET-3 (RFC 5737), reserved for documentation, so it
// should never carry indexed names.
func TestLiveNoRecordsIsAnAnswer(t *testing.T) {
	e := liveEngine(t)
	res, err := e.LookupRDNS("203.0.113.9", engine.Options{Limit: 5, Refresh: true})
	if !errors.Is(err, engine.ErrNoRecords) {
		t.Fatalf("err = %v, want ErrNoRecords (TEST-NET-3 should be unindexed)", err)
	}
	if res == nil {
		t.Fatal("the result must still be populated so provenance survives")
	}
	if res.Count != 0 || res.Source != "ip.thc.org" {
		t.Errorf("result = %+v", res)
	}
}

// Upstream has effectively no IPv6 data: an empty answer, not an error. If this
// ever starts returning records, the IPv6 caveat in the docs is stale.
func TestLiveIPv6IsEmptyNotAnError(t *testing.T) {
	e := liveEngine(t)
	_, err := e.LookupRDNS("2404:6800:4004:80e::200e", engine.Options{Limit: 5, Refresh: true})
	if err != nil && !errors.Is(err, engine.ErrNoRecords) {
		t.Fatalf("err = %v, want nil or ErrNoRecords", err)
	}
	if err == nil {
		t.Log("upstream now returns IPv6 records — the docs' IPv6 caveat may be stale")
	}
}

// Our local octet-boundary restriction restates an upstream limit. This asks
// upstream directly, bypassing local validation, so that a change on their side
// (accepting /25, or rejecting /24) surfaces here rather than as a silent
// difference between the docs and reality.
func TestLiveUpstreamStillRejectsNonOctetBoundary(t *testing.T) {
	c := &thc.Client{
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		UserAgent: "rdns-lookup/e2e (+https://github.com/nlink-jp/rdns-lookup)",
	}
	_, err := c.Fetch(thc.Request{Kind: thc.KindRDNS, Query: "142.251.43.0/25", Limit: 1})
	if !thc.IsInvalidTarget(err) {
		t.Errorf("upstream accepted a /25 (err = %v); the local restriction in query.ClassifyAddress may now be too strict", err)
	}
}

// Every response should carry the rate-limit budget, since bulk pacing depends
// on it. An unknown budget disables pacing, which is the polite-behaviour
// safeguard.
func TestLiveRateLimitBudgetReported(t *testing.T) {
	e := liveEngine(t)
	res, err := e.LookupRDNS(liveIP, engine.Options{Limit: 1, Refresh: true})
	if err != nil {
		t.Fatalf("rdns: %v", err)
	}
	if res.RateLimit.Unknown() {
		t.Error("upstream reported no rate-limit budget; bulk pacing would be disabled")
	}
	if res.RateLimit.Limit <= 0 || res.RateLimit.Rate <= 0 {
		t.Errorf("rate limit = %+v, want a positive limit and refill rate", res.RateLimit)
	}
	t.Logf("budget: %d/%d remaining, refilling at %.2f/sec",
		res.RateLimit.Remaining, res.RateLimit.Limit, res.RateLimit.Rate)
}

// A repeated lookup must be served from cache. This is the courtesy that keeps
// repeated triage of one indicator from becoming repeated requests.
func TestLiveCacheServesRepeat(t *testing.T) {
	e := liveEngine(t)
	first, err := e.LookupRDNS(liveIP, engine.Options{Limit: 5})
	if err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	if first.Cached {
		t.Fatal("the first lookup into a fresh cache cannot be a hit")
	}
	second, err := e.LookupRDNS(liveIP, engine.Options{Limit: 5})
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if !second.Cached {
		t.Error("the second identical lookup should be served from cache")
	}
	if second.Count != first.Count {
		t.Errorf("cached count = %d, want %d", second.Count, first.Count)
	}
}
