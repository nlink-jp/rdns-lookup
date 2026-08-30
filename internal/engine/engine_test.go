package engine

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/rdns-lookup/internal/cache"
	"github.com/nlink-jp/rdns-lookup/internal/config"
	"github.com/nlink-jp/rdns-lookup/internal/query"
	"github.com/nlink-jp/rdns-lookup/internal/thc"
)

// fakeFetcher records the requests it saw and replays scripted pages.
type fakeFetcher struct {
	requests []thc.Request
	pages    []*thc.Page
	err      error
}

func (f *fakeFetcher) Fetch(req thc.Request) (*thc.Page, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.pages) == 0 {
		return &thc.Page{Route: routeOf(req), RateLimit: okBudget()}, nil
	}
	p := f.pages[0]
	f.pages = f.pages[1:]
	return p, nil
}

func routeOf(req thc.Request) thc.Route {
	if req.Route == thc.RouteCSV {
		return thc.RouteCSV
	}
	return thc.RouteJSON
}

func okBudget() thc.RateLimit {
	return thc.RateLimit{Limit: 250, Remaining: 249, Rate: 0.5}
}

// newEngine builds an engine with a frozen clock, an isolated cache, and a
// sleep that never actually waits.
func newEngine(t *testing.T, f *thc.Page, more ...*thc.Page) (*Engine, *fakeFetcher) {
	t.Helper()
	cfg := &config.Config{
		DefaultLimit: 100,
		MaxAll:       thc.CSVMaxLimit,
		Dedup:        true,
		CacheDir:     t.TempDir(),
		CacheTTL:     24 * time.Hour,
		MinRemaining: 20,
	}
	pages := []*thc.Page{}
	if f != nil {
		pages = append(pages, f)
	}
	pages = append(pages, more...)
	ff := &fakeFetcher{pages: pages}
	return &Engine{
		Cfg:    cfg,
		Cache:  &cache.Store{Dir: cfg.CacheDir},
		Client: ff,
		Now:    func() time.Time { return time.Unix(1_800_000_000, 0) },
		Sleep:  func(time.Duration) {},
	}, ff
}

func recs(domains ...string) []thc.Record {
	out := make([]thc.Record, 0, len(domains))
	for _, d := range domains {
		out = append(out, thc.Record{Domain: d})
	}
	return out
}

func TestLookupRDNSBasic(t *testing.T) {
	e, ff := newEngine(t, &thc.Page{
		Records:         recs("a.example.com", "b.example.com"),
		MatchingRecords: 2,
		Route:           thc.RouteJSON,
		RateLimit:       okBudget(),
	})
	res, err := e.LookupRDNS("1.1.1.1", Options{})
	if err != nil {
		t.Fatalf("LookupRDNS returned error %v", err)
	}
	if res.Count != 2 || res.MatchingRecords != 2 {
		t.Errorf("count/total = %d/%d, want 2/2", res.Count, res.MatchingRecords)
	}
	if res.Truncated {
		t.Error("a complete set must not be marked truncated")
	}
	if res.Source != "ip.thc.org" || res.Route != "json" {
		t.Errorf("provenance = %q/%q", res.Source, res.Route)
	}
	if len(ff.requests) != 1 || ff.requests[0].Query != "1.1.1.1" {
		t.Errorf("requests = %+v", ff.requests)
	}
}

// A default lookup asks for 100 records via the JSON face — one request.
func TestDefaultLimitUsesJSONFace(t *testing.T) {
	e, ff := newEngine(t, &thc.Page{Records: recs("a.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()})
	if _, err := e.LookupRDNS("1.1.1.1", Options{}); err != nil {
		t.Fatalf("error %v", err)
	}
	if ff.requests[0].Route == thc.RouteCSV {
		t.Error("the default limit must use the JSON face")
	}
	if ff.requests[0].Limit != 100 {
		t.Errorf("limit = %d, want 100", ff.requests[0].Limit)
	}
}

// Anything above the JSON face's real ceiling must go through CSV, because the
// JSON face silently truncates at 100 rather than erroring.
func TestAboveJSONCeilingSwitchesToCSV(t *testing.T) {
	e, ff := newEngine(t, &thc.Page{Records: recs("a.example.com"), Route: thc.RouteCSV, RateLimit: okBudget()})
	if _, err := e.LookupRDNS("1.1.1.1", Options{Limit: 500}); err != nil {
		t.Fatalf("error %v", err)
	}
	if ff.requests[0].Route != thc.RouteCSV {
		t.Errorf("route = %q, want csv for a limit above %d", ff.requests[0].Route, thc.JSONMaxLimit)
	}
}

func TestAllUsesCeiling(t *testing.T) {
	e, ff := newEngine(t, &thc.Page{Records: recs("a.example.com"), Route: thc.RouteCSV, RateLimit: okBudget()})
	if _, err := e.LookupRDNS("1.1.1.1", Options{All: true}); err != nil {
		t.Fatalf("error %v", err)
	}
	if ff.requests[0].Limit != thc.CSVMaxLimit {
		t.Errorf("limit = %d, want the ceiling %d", ff.requests[0].Limit, thc.CSVMaxLimit)
	}
}

func TestLimitCappedAtCeiling(t *testing.T) {
	e, ff := newEngine(t, &thc.Page{Records: recs("a.example.com"), Route: thc.RouteCSV, RateLimit: okBudget()})
	if _, err := e.LookupRDNS("1.1.1.1", Options{Limit: 999999}); err != nil {
		t.Fatalf("error %v", err)
	}
	if ff.requests[0].Limit != thc.CSVMaxLimit {
		t.Errorf("limit = %d, want it capped to %d", ff.requests[0].Limit, thc.CSVMaxLimit)
	}
}

// The JSON face paginates; the engine must follow next_page_state until it has
// what was asked for.
func TestJSONPaginationFollowsPageState(t *testing.T) {
	e, ff := newEngine(t,
		&thc.Page{Records: recs("a.example.com"), MatchingRecords: 3, NextPageState: "tok1", Route: thc.RouteJSON, RateLimit: okBudget()},
		&thc.Page{Records: recs("b.example.com"), MatchingRecords: 3, NextPageState: "tok2", Route: thc.RouteJSON, RateLimit: okBudget()},
		&thc.Page{Records: recs("c.example.com"), MatchingRecords: 3, NextPageState: "", Route: thc.RouteJSON, RateLimit: okBudget()},
	)
	res, err := e.LookupRDNS("1.1.1.1", Options{Limit: 10})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if res.Count != 3 {
		t.Fatalf("count = %d, want 3", res.Count)
	}
	if len(ff.requests) != 3 {
		t.Fatalf("made %d requests, want 3", len(ff.requests))
	}
	if ff.requests[1].PageState != "tok1" || ff.requests[2].PageState != "tok2" {
		t.Errorf("page states not threaded: %q, %q", ff.requests[1].PageState, ff.requests[2].PageState)
	}
	if res.Requests != 3 {
		t.Errorf("Requests = %d, want 3", res.Requests)
	}
}

// The CSV face returns everything it will ever return in one response, so the
// engine must not try to paginate it.
func TestCSVNeverPaginates(t *testing.T) {
	e, ff := newEngine(t, &thc.Page{Records: recs("a.example.com"), Route: thc.RouteCSV, RateLimit: okBudget()})
	if _, err := e.LookupRDNS("1.1.1.1", Options{Limit: 500}); err != nil {
		t.Fatalf("error %v", err)
	}
	if len(ff.requests) != 1 {
		t.Errorf("made %d requests, want 1 — the CSV face cannot paginate", len(ff.requests))
	}
}

func TestPaginationStopsAtWantedCount(t *testing.T) {
	e, ff := newEngine(t,
		&thc.Page{Records: recs("a.example.com", "b.example.com"), MatchingRecords: 99, NextPageState: "tok", Route: thc.RouteJSON, RateLimit: okBudget()},
	)
	res, err := e.LookupRDNS("1.1.1.1", Options{Limit: 2})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if len(ff.requests) != 1 {
		t.Errorf("made %d requests; the first page already satisfied the limit", len(ff.requests))
	}
	if res.Count != 2 {
		t.Errorf("count = %d, want 2", res.Count)
	}
}

// Upstream returns the same (domain, ip) twice, differing only in tld. The
// richer variant must survive so dedup never loses a field.
func TestDedupKeepsRicherDuplicate(t *testing.T) {
	e, _ := newEngine(t, &thc.Page{
		Records: []thc.Record{
			{Domain: "x.1e100.net", IPAddress: "1.1.1.1", ApexDomain: "1e100.net"},
			{Domain: "x.1e100.net", IPAddress: "1.1.1.1", ApexDomain: "1e100.net", TLD: "net", ASN: "13335"},
			{Domain: "y.example.com", IPAddress: "1.1.1.1"},
		},
		MatchingRecords: 3,
		Route:           thc.RouteJSON,
		RateLimit:       okBudget(),
	})
	res, err := e.LookupRDNS("1.1.1.1", Options{Limit: 10})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if res.Count != 2 {
		t.Fatalf("count = %d, want 2 after dedup", res.Count)
	}
	if res.Duplicates != 1 {
		t.Errorf("Duplicates = %d, want 1", res.Duplicates)
	}
	var kept thc.Record
	for _, r := range res.Records {
		if r.Domain == "x.1e100.net" {
			kept = r
		}
	}
	if kept.TLD != "net" || kept.ASN != "13335" {
		t.Errorf("dedup kept the poorer variant: %+v", kept)
	}
}

// The same domain on two different addresses within a block is not a duplicate.
func TestDedupKeepsDistinctIPs(t *testing.T) {
	e, _ := newEngine(t, &thc.Page{
		Records: []thc.Record{
			{Domain: "x.example.com", IPAddress: "1.1.1.1"},
			{Domain: "x.example.com", IPAddress: "1.1.1.2"},
		},
		MatchingRecords: 2, Route: thc.RouteJSON, RateLimit: okBudget(),
	})
	res, err := e.LookupRDNS("1.1.1.0/24", Options{Limit: 10})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if res.Count != 2 || res.Duplicates != 0 {
		t.Errorf("count/dupes = %d/%d, want 2/0", res.Count, res.Duplicates)
	}
}

func TestDedupDisabled(t *testing.T) {
	e, _ := newEngine(t, &thc.Page{
		Records: []thc.Record{
			{Domain: "x.example.com", IPAddress: "1.1.1.1"},
			{Domain: "x.example.com", IPAddress: "1.1.1.1", TLD: "com"},
		},
		MatchingRecords: 2, Route: thc.RouteJSON, RateLimit: okBudget(),
	})
	e.Cfg.Dedup = false
	res, err := e.LookupRDNS("1.1.1.1", Options{Limit: 10})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if res.Count != 2 || res.Duplicates != 0 {
		t.Errorf("count/dupes = %d/%d, want 2/0 with dedup off", res.Count, res.Duplicates)
	}
}

// Silently returning a capped set would read as completeness, so a partial
// answer must be flagged and explained.
func TestTruncationWhenUpstreamHoldsMore(t *testing.T) {
	e, _ := newEngine(t, &thc.Page{
		Records: recs("a.example.com", "b.example.com"), MatchingRecords: 83216,
		Route: thc.RouteJSON, RateLimit: okBudget(),
	})
	res, err := e.LookupRDNS("1.1.1.1", Options{Limit: 2})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if !res.Truncated {
		t.Fatal("Truncated must be set when upstream holds more")
	}
	if !strings.Contains(res.TruncationNote, "83216") || !strings.Contains(res.TruncationNote, "--all") {
		t.Errorf("note %q should state the total and how to get more", res.TruncationNote)
	}
}

// At the ceiling there is no bigger limit to suggest, so the advice changes to
// narrowing the query.
func TestTruncationAtCeilingSuggestsNarrowing(t *testing.T) {
	rows := make([]thc.Record, 0, 3)
	for i := 0; i < 3; i++ {
		rows = append(rows, thc.Record{Domain: fmt.Sprintf("d%d.example.com", i), IPAddress: "1.1.1.1"})
	}
	e, _ := newEngine(t, &thc.Page{Records: rows, MatchingRecords: 83216, Route: thc.RouteCSV, RateLimit: okBudget()})
	e.Cfg.MaxAll = 3
	res, err := e.LookupRDNS("1.1.1.1", Options{All: true})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if !res.Truncated {
		t.Fatal("Truncated must be set")
	}
	if !strings.Contains(res.TruncationNote, "--tld") {
		t.Errorf("note %q should suggest narrowing at the ceiling", res.TruncationNote)
	}
}

// The CSV face reports no total, so a full page is indistinguishable from a
// capped one: we must not claim completeness we cannot prove.
func TestTruncationWhenTotalUnknownAndPageFull(t *testing.T) {
	e, _ := newEngine(t, &thc.Page{Records: recs("a.example.com", "b.example.com"), Route: thc.RouteCSV, RateLimit: okBudget()})
	res, err := e.LookupRDNS("1.1.1.1", Options{Limit: 2})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if !res.Truncated {
		t.Error("a full page with no upstream total must be reported as possibly truncated")
	}
}

// Dedup shrinking the set below the request is not truncation.
func TestNoFalseTruncationAfterDedup(t *testing.T) {
	e, _ := newEngine(t, &thc.Page{
		Records: []thc.Record{
			{Domain: "x.example.com", IPAddress: "1.1.1.1"},
			{Domain: "x.example.com", IPAddress: "1.1.1.1", TLD: "com"},
		},
		MatchingRecords: 2, Route: thc.RouteJSON, RateLimit: okBudget(),
	})
	res, err := e.LookupRDNS("1.1.1.1", Options{Limit: 2})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if res.Truncated {
		t.Errorf("dedup must not look like truncation: %+v", res)
	}
}

// Upstream accepts filters on a block and then ignores them, returning the
// unfiltered set. Failing loudly is the only way the caller finds out.
func TestFiltersRejectedForBlocks(t *testing.T) {
	e, ff := newEngine(t, nil)
	for _, opts := range []Options{{TLDs: []string{"com"}}, {ApexDomain: "example.com"}} {
		_, err := e.LookupRDNS("1.1.1.0/24", opts)
		if !errors.Is(err, query.ErrInvalid) {
			t.Errorf("LookupRDNS(block, %+v) error = %v, want ErrInvalid", opts, err)
		}
	}
	if len(ff.requests) != 0 {
		t.Error("a rejected filter must not reach the network")
	}
}

func TestFiltersAllowedForSingleAddress(t *testing.T) {
	e, ff := newEngine(t, &thc.Page{Records: recs("a.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()})
	if _, err := e.LookupRDNS("1.1.1.1", Options{TLDs: []string{"COM", ".net"}}); err != nil {
		t.Fatalf("error %v", err)
	}
	got := ff.requests[0].TLDs
	if len(got) != 2 || got[0] != "com" || got[1] != "net" {
		t.Errorf("TLDs = %v, want normalized [com net]", got)
	}
}

func TestInvalidApexFilterRejected(t *testing.T) {
	e, ff := newEngine(t, nil)
	if _, err := e.LookupRDNS("1.1.1.1", Options{ApexDomain: "not a domain"}); !errors.Is(err, query.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
	if len(ff.requests) != 0 {
		t.Error("an invalid filter must not reach the network")
	}
}

func TestNoRecordsIsAnAnswer(t *testing.T) {
	e, _ := newEngine(t, &thc.Page{Route: thc.RouteJSON, RateLimit: okBudget()})
	res, err := e.LookupRDNS("1.1.1.1", Options{})
	if !errors.Is(err, ErrNoRecords) {
		t.Fatalf("error = %v, want ErrNoRecords", err)
	}
	if res == nil {
		t.Fatal("the result must still be populated so provenance survives")
	}
	if res.Count != 0 || res.Source != "ip.thc.org" {
		t.Errorf("result = %+v", res)
	}
}

func TestCacheHitAvoidsSecondRequest(t *testing.T) {
	e, ff := newEngine(t, &thc.Page{Records: recs("a.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()})
	if _, err := e.LookupRDNS("1.1.1.1", Options{}); err != nil {
		t.Fatalf("first lookup error %v", err)
	}
	res, err := e.LookupRDNS("1.1.1.1", Options{})
	if err != nil {
		t.Fatalf("second lookup error %v", err)
	}
	if !res.Cached {
		t.Error("the second lookup should be served from cache")
	}
	if len(ff.requests) != 1 {
		t.Errorf("made %d requests, want 1", len(ff.requests))
	}
	if res.Count != 1 || res.Records[0].Domain != "a.example.com" {
		t.Errorf("cached result = %+v", res)
	}
}

func TestRefreshBypassesCache(t *testing.T) {
	e, ff := newEngine(t,
		&thc.Page{Records: recs("a.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()},
		&thc.Page{Records: recs("a.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()},
	)
	if _, err := e.LookupRDNS("1.1.1.1", Options{}); err != nil {
		t.Fatalf("error %v", err)
	}
	res, err := e.LookupRDNS("1.1.1.1", Options{Refresh: true})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if res.Cached {
		t.Error("--refresh must not report a cache hit")
	}
	if len(ff.requests) != 2 {
		t.Errorf("made %d requests, want 2", len(ff.requests))
	}
}

// A cached empty answer must still surface as ErrNoRecords, not as success.
func TestCachedEmptyStillReportsNoRecords(t *testing.T) {
	e, _ := newEngine(t, &thc.Page{Route: thc.RouteJSON, RateLimit: okBudget()})
	if _, err := e.LookupRDNS("1.1.1.1", Options{}); !errors.Is(err, ErrNoRecords) {
		t.Fatalf("first error = %v, want ErrNoRecords", err)
	}
	res, err := e.LookupRDNS("1.1.1.1", Options{})
	if !errors.Is(err, ErrNoRecords) {
		t.Fatalf("cached error = %v, want ErrNoRecords", err)
	}
	if !res.Cached {
		t.Error("the second lookup should be cached")
	}
}

// Options that change the answer must not share a cache entry.
func TestCacheKeyVariesWithOptions(t *testing.T) {
	e, ff := newEngine(t,
		&thc.Page{Records: recs("a.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()},
		&thc.Page{Records: recs("b.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()},
		&thc.Page{Records: recs("c.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()},
	)
	if _, err := e.LookupRDNS("1.1.1.1", Options{Limit: 10}); err != nil {
		t.Fatalf("error %v", err)
	}
	if _, err := e.LookupRDNS("1.1.1.1", Options{Limit: 20}); err != nil {
		t.Fatalf("error %v", err)
	}
	if _, err := e.LookupRDNS("1.1.1.1", Options{Limit: 10, TLDs: []string{"com"}}); err != nil {
		t.Fatalf("error %v", err)
	}
	if len(ff.requests) != 3 {
		t.Errorf("made %d requests, want 3 — limit and filters must be in the cache key", len(ff.requests))
	}
}

// The three lookups must not collide in the cache even for the same string.
func TestCacheKeySeparatesKinds(t *testing.T) {
	e, ff := newEngine(t,
		&thc.Page{Records: recs("a.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()},
		&thc.Page{Records: recs("b.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()},
	)
	if _, err := e.LookupSubdomains("example.com", Options{}); err != nil {
		t.Fatalf("error %v", err)
	}
	if _, err := e.LookupCNAMEs("example.com", Options{}); err != nil {
		t.Fatalf("error %v", err)
	}
	if len(ff.requests) != 2 {
		t.Errorf("made %d requests, want 2 — kinds must not share a cache entry", len(ff.requests))
	}
	if ff.requests[0].Kind != thc.KindSubdomains || ff.requests[1].Kind != thc.KindCNAMEs {
		t.Errorf("kinds = %q, %q", ff.requests[0].Kind, ff.requests[1].Kind)
	}
}

func TestCacheExpires(t *testing.T) {
	e, ff := newEngine(t,
		&thc.Page{Records: recs("a.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()},
		&thc.Page{Records: recs("a.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()},
	)
	now := time.Unix(1_800_000_000, 0)
	e.Now = func() time.Time { return now }
	if _, err := e.LookupRDNS("1.1.1.1", Options{}); err != nil {
		t.Fatalf("error %v", err)
	}
	now = now.Add(25 * time.Hour)
	res, err := e.LookupRDNS("1.1.1.1", Options{})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if res.Cached {
		t.Error("an entry past the TTL must not be served")
	}
	if len(ff.requests) != 2 {
		t.Errorf("made %d requests, want 2", len(ff.requests))
	}
}

// Raw bodies can be tens of megabytes and are only wanted by the request that
// asked, so they must never reach the cache.
func TestRawNotCached(t *testing.T) {
	e, _ := newEngine(t,
		&thc.Page{Records: recs("a.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget(), Raw: []byte(`{"x":1}`)},
	)
	res, err := e.LookupRDNS("1.1.1.1", Options{Raw: true})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if len(res.Raw) == 0 {
		t.Fatal("the requesting call should receive Raw")
	}
	cached, err := e.LookupRDNS("1.1.1.1", Options{Raw: true})
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if !cached.Cached {
		t.Fatal("expected a cache hit")
	}
	if len(cached.Raw) != 0 {
		t.Error("Raw must not be stored in the cache")
	}
}

// The budget is shared with everyone else using the free service; a bulk run
// that ignores it is the abuse upstream asks us to avoid.
func TestPaceWaitsWhenBudgetLow(t *testing.T) {
	e, _ := newEngine(t,
		&thc.Page{Records: recs("a.example.com"), MatchingRecords: 9, NextPageState: "tok",
			Route: thc.RouteJSON, RateLimit: thc.RateLimit{Limit: 250, Remaining: 5, Rate: 0.5}},
		&thc.Page{Records: recs("b.example.com"), MatchingRecords: 9, Route: thc.RouteJSON, RateLimit: okBudget()},
	)
	var slept []time.Duration
	e.Sleep = func(d time.Duration) { slept = append(slept, d) }
	if _, err := e.LookupRDNS("1.1.1.1", Options{Limit: 10}); err != nil {
		t.Fatalf("error %v", err)
	}
	if len(slept) != 1 {
		t.Fatalf("slept %d times, want 1 (before the second request)", len(slept))
	}
	// Climbing from 5 back above a floor of 20 at 0.5/sec takes ~32s.
	if slept[0] < 30*time.Second {
		t.Errorf("slept %s, want at least 30s", slept[0])
	}
}

func TestPaceSkipsWhenBudgetHealthy(t *testing.T) {
	e, _ := newEngine(t,
		&thc.Page{Records: recs("a.example.com"), MatchingRecords: 9, NextPageState: "tok", Route: thc.RouteJSON, RateLimit: okBudget()},
		&thc.Page{Records: recs("b.example.com"), MatchingRecords: 9, Route: thc.RouteJSON, RateLimit: okBudget()},
	)
	var slept []time.Duration
	e.Sleep = func(d time.Duration) { slept = append(slept, d) }
	if _, err := e.LookupRDNS("1.1.1.1", Options{Limit: 10}); err != nil {
		t.Fatalf("error %v", err)
	}
	if len(slept) != 0 {
		t.Errorf("slept %v with a healthy budget", slept)
	}
}

// An unknown budget must not be treated as exhausted.
func TestPaceSkipsWhenBudgetUnknown(t *testing.T) {
	e, _ := newEngine(t,
		&thc.Page{Records: recs("a.example.com"), MatchingRecords: 9, NextPageState: "tok",
			Route: thc.RouteJSON, RateLimit: thc.RateLimit{Limit: -1, Remaining: -1}},
		&thc.Page{Records: recs("b.example.com"), MatchingRecords: 9, Route: thc.RouteJSON, RateLimit: okBudget()},
	)
	var slept []time.Duration
	e.Sleep = func(d time.Duration) { slept = append(slept, d) }
	if _, err := e.LookupRDNS("1.1.1.1", Options{Limit: 10}); err != nil {
		t.Fatalf("error %v", err)
	}
	if len(slept) != 0 {
		t.Errorf("slept %v on an unknown budget", slept)
	}
}

func TestFetchErrorPropagates(t *testing.T) {
	e, _ := newEngine(t, nil)
	e.Client = &fakeFetcher{err: errors.New("connection reset")}
	if _, err := e.LookupRDNS("1.1.1.1", Options{}); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("error = %v, want the transport failure", err)
	}
}

func TestValidationRejectedBeforeNetwork(t *testing.T) {
	e, ff := newEngine(t, nil)
	if _, err := e.LookupRDNS("142.251.43.0/25", Options{}); !errors.Is(err, query.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
	if _, err := e.LookupSubdomains("1.1.1.1", Options{}); !errors.Is(err, query.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
	if _, err := e.LookupCNAMEs("not a domain", Options{}); !errors.Is(err, query.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
	if len(ff.requests) != 0 {
		t.Error("invalid input must never reach the network")
	}
}

func TestSubdomainsCanonicalizesTarget(t *testing.T) {
	e, ff := newEngine(t, &thc.Page{Records: recs("a.example.com"), MatchingRecords: 1, Route: thc.RouteJSON, RateLimit: okBudget()})
	if _, err := e.LookupSubdomains("GitHub.COM.", Options{}); err != nil {
		t.Fatalf("error %v", err)
	}
	if ff.requests[0].Query != "github.com" {
		t.Errorf("query = %q, want github.com", ff.requests[0].Query)
	}
}

func TestSortRecordsStable(t *testing.T) {
	rs := []thc.Record{
		{Domain: "b.example.com", IPAddress: "1.1.1.2"},
		{Domain: "a.example.com", IPAddress: "1.1.1.9"},
		{Domain: "a.example.com", IPAddress: "1.1.1.1"},
	}
	SortRecords(rs)
	want := []string{"a.example.com|1.1.1.1", "a.example.com|1.1.1.9", "b.example.com|1.1.1.2"}
	for i, w := range want {
		if got := rs[i].Domain + "|" + rs[i].IPAddress; got != w {
			t.Errorf("record %d = %q, want %q", i, got, w)
		}
	}
}

func TestWantLimitPrecedence(t *testing.T) {
	e, _ := newEngine(t, nil)
	tests := []struct {
		opts Options
		want int
	}{
		{Options{}, 100},
		{Options{Limit: 7}, 7},
		{Options{All: true}, thc.CSVMaxLimit},
		{Options{Limit: 7, All: true}, thc.CSVMaxLimit}, // --all wins
		{Options{Limit: 999999}, thc.CSVMaxLimit},
	}
	for _, tt := range tests {
		if got := e.wantLimit(tt.opts); got != tt.want {
			t.Errorf("wantLimit(%+v) = %d, want %d", tt.opts, got, tt.want)
		}
	}
}
