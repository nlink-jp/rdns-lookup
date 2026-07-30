package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nlink-jp/rdns-lookup/internal/cache"
	"github.com/nlink-jp/rdns-lookup/internal/config"
	"github.com/nlink-jp/rdns-lookup/internal/query"
	"github.com/nlink-jp/rdns-lookup/internal/thc"
)

// ErrNoRecords marks a lookup that upstream answered successfully with nothing
// indexed. It is not a failure: "no domains are associated with this address"
// is an answer, and the CLI maps it to its own exit code rather than to the
// error path.
var ErrNoRecords = errors.New("no records indexed")

// Fetcher is the upstream seam, mocked in tests.
type Fetcher interface {
	Fetch(thc.Request) (*thc.Page, error)
}

// Engine performs lookups against the upstream index, with caching.
type Engine struct {
	Cfg    *config.Config
	Cache  *cache.Store
	Client Fetcher
	// Now is the only clock in the tool; tests freeze it.
	Now func() time.Time
	// Sleep paces bulk runs against the rate-limit budget. Tests substitute a
	// no-op so the suite never actually waits.
	Sleep func(time.Duration)
}

// New builds an Engine from resolved configuration.
func New(cfg *config.Config, version string) *Engine {
	return &Engine{
		Cfg:   cfg,
		Cache: &cache.Store{Dir: cfg.CacheDir},
		Client: &thc.Client{
			HTTP:      &http.Client{Timeout: cfg.Timeout},
			BaseURL:   cfg.BaseURL,
			UserAgent: "rdns-lookup/" + version + " (+https://github.com/nlink-jp/rdns-lookup)",
		},
		Now:   time.Now,
		Sleep: time.Sleep,
	}
}

// Options are the per-lookup knobs shared by the CLI and MCP surfaces.
type Options struct {
	// Limit is how many records to retrieve. Zero means the configured
	// default; All overrides it.
	Limit int
	All   bool
	// TLDs and ApexDomain filter rdns lookups. Upstream silently ignores both
	// for block queries, so asking for them with a block is an error here
	// rather than a surprise later.
	TLDs       []string
	ApexDomain string
	Refresh    bool // bypass the cache read
	Raw        bool // retain the upstream response body
}

// Result is one completed lookup.
type Result struct {
	Kind  string `json:"kind"`  // rdns | subdomains | cnames
	Query string `json:"query"` // canonical target
	Block bool   `json:"block,omitempty"`

	Records []thc.Record `json:"records"`
	// Count is len(Records) after normalization.
	Count int `json:"count"`
	// MatchingRecords is the upstream total. It is 0 when upstream declined to
	// count (it says so for very large blocks) and for CSV-face reads, which
	// report no total — so a 0 here means "unknown", not "none".
	MatchingRecords int `json:"matching_records"`
	// Truncated is set when upstream holds more than we retrieved.
	Truncated bool `json:"truncated"`
	// TruncationNote explains, in one human sentence, what was left behind and
	// how to narrow the query. Empty unless Truncated.
	TruncationNote string `json:"truncation_note,omitempty"`
	// Duplicates is how many rows deduplication removed.
	Duplicates int `json:"duplicates_removed"`

	Source    string        `json:"source"` // always ip.thc.org
	Route     string        `json:"route"`  // json | csv
	Requests  int           `json:"upstream_requests"`
	Cached    bool          `json:"cached"`
	FetchedAt string        `json:"fetched_at"`
	RateLimit thc.RateLimit `json:"rate_limit"`

	// Raw holds the upstream bodies when Options.Raw was set. Never cached.
	Raw []json.RawMessage `json:"raw,omitempty"`
}

// LookupRDNS returns the domains associated with an IP or IP block.
func (e *Engine) LookupRDNS(target string, opts Options) (*Result, error) {
	addr, err := query.ClassifyAddress(target)
	if err != nil {
		return nil, err
	}
	tlds := query.NormalizeTLDs(opts.TLDs)
	apex := strings.ToLower(strings.TrimSpace(opts.ApexDomain))
	if addr.Block && (len(tlds) > 0 || apex != "") {
		// Upstream accepts these for a block and then ignores them, returning
		// the unfiltered set. Failing here is the only way the caller learns
		// their filter did nothing.
		return nil, fmt.Errorf(
			"%w: upstream ignores --tld/--apex for block queries; query a single address, or filter the output",
			query.ErrInvalid)
	}
	if apex != "" {
		if _, derr := query.ClassifyDomain(apex); derr != nil {
			return nil, fmt.Errorf("%w: --apex %q is not a valid domain", query.ErrInvalid, apex)
		}
	}
	return e.run(thc.KindRDNS, addr.Value, addr.Block, tlds, apex, opts)
}

// LookupSubdomains enumerates the subdomains of a domain.
func (e *Engine) LookupSubdomains(target string, opts Options) (*Result, error) {
	d, err := query.ClassifyDomain(target)
	if err != nil {
		return nil, err
	}
	return e.run(thc.KindSubdomains, d.Value, false, nil, "", opts)
}

// LookupCNAMEs returns the domains that CNAME to a target domain.
func (e *Engine) LookupCNAMEs(target string, opts Options) (*Result, error) {
	d, err := query.ClassifyDomain(target)
	if err != nil {
		return nil, err
	}
	return e.run(thc.KindCNAMEs, d.Value, false, nil, "", opts)
}

// wantLimit resolves how many records this lookup should retrieve.
func (e *Engine) wantLimit(opts Options) int {
	switch {
	case opts.All:
		return e.Cfg.MaxAll
	case opts.Limit > 0:
		if opts.Limit > e.Cfg.MaxAll {
			return e.Cfg.MaxAll
		}
		return opts.Limit
	default:
		return e.Cfg.DefaultLimit
	}
}

// run is the shared path for all three lookups.
func (e *Engine) run(kind thc.Kind, canonical string, block bool, tlds []string, apex string, opts Options) (*Result, error) {
	want := e.wantLimit(opts)
	key := cache.Key(string(kind), canonical, strconv.Itoa(want), strings.Join(tlds, "-"), apex)

	if !opts.Refresh {
		if raw, ok := e.Cache.Get(key, e.Now(), e.Cfg.CacheTTL); ok {
			var res Result
			if json.Unmarshal(raw, &res) == nil {
				res.Cached = true
				if res.Count == 0 {
					return &res, ErrNoRecords
				}
				return &res, nil
			}
			// A cache entry we cannot decode is treated as a miss; the fetch
			// below overwrites it.
		}
	}

	res, err := e.fetch(kind, canonical, block, tlds, apex, want, opts.Raw)
	if err != nil {
		return nil, err
	}

	// The cached copy never carries raw bodies: they can be tens of megabytes
	// and are only ever wanted for the request that asked for them.
	raws := res.Raw
	res.Raw = nil
	if b, merr := json.Marshal(res); merr == nil {
		// A cache write failure must not fail a lookup that already succeeded.
		_ = e.Cache.Put(key, b, e.Now())
	}
	res.Raw = raws

	if res.Count == 0 {
		return res, ErrNoRecords
	}
	return res, nil
}

// fetch retrieves up to want records, paginating the JSON face when needed.
func (e *Engine) fetch(kind thc.Kind, canonical string, block bool, tlds []string, apex string, want int, keepRaw bool) (*Result, error) {
	res := &Result{
		Kind:      string(kind),
		Query:     canonical,
		Block:     block,
		Source:    "ip.thc.org",
		FetchedAt: e.Now().UTC().Format(time.RFC3339),
		// Start the budget as unknown rather than zero. A zero Remaining reads
		// as "exhausted" to pace(), which would make the very first request of
		// every lookup wait for a replenishment that was never needed.
		RateLimit: thc.RateLimit{Limit: -1, Remaining: -1},
	}

	route := thc.RouteJSON
	if want > thc.JSONMaxLimit {
		// The JSON face silently caps at 100, so anything larger must go
		// through the CSV face — which in turn cannot paginate.
		route = thc.RouteCSV
	}
	res.Route = string(route)

	var (
		records   []thc.Record
		pageState string
		total     int
		haveTotal bool
	)
	for {
		remaining := want - len(records)
		if remaining <= 0 {
			break
		}
		e.pace(res.RateLimit)

		page, err := e.Client.Fetch(thc.Request{
			Kind:       kind,
			Query:      canonical,
			Limit:      remaining,
			TLDs:       tlds,
			ApexDomain: apex,
			PageState:  pageState,
			Route:      route,
			KeepRaw:    keepRaw,
		})
		if err != nil {
			return nil, err
		}
		res.Requests++
		res.RateLimit = page.RateLimit
		if keepRaw && len(page.Raw) > 0 {
			res.Raw = append(res.Raw, json.RawMessage(page.Raw))
		}
		// Only the JSON face reports a total, and only when it managed to
		// count; keep the first real number we see.
		if !haveTotal && page.Route == thc.RouteJSON && page.MatchingRecords > 0 {
			total, haveTotal = page.MatchingRecords, true
		}
		records = append(records, page.Records...)

		// The CSV face returns everything it will ever return in one response.
		if page.Route == thc.RouteCSV || page.NextPageState == "" || len(page.Records) == 0 {
			break
		}
		pageState = page.NextPageState
	}

	if len(records) > want {
		records = records[:want]
	}
	before := len(records)
	if e.Cfg.Dedup {
		records = dedup(records)
	}
	res.Duplicates = before - len(records)
	res.Records = records
	res.Count = len(records)
	if haveTotal {
		res.MatchingRecords = total
	}
	e.markTruncation(res, want, haveTotal)
	return res, nil
}

// markTruncation decides whether the caller is looking at a partial answer, and
// says so in words. Silently returning a capped set would read as completeness.
func (e *Engine) markTruncation(res *Result, want int, haveTotal bool) {
	// Deduplication can leave Count below want even when nothing was withheld,
	// so compare the retrieved-before-dedup size against the request instead.
	retrieved := res.Count + res.Duplicates
	switch {
	case haveTotal && res.MatchingRecords > retrieved:
		res.Truncated = true
		res.TruncationNote = fmt.Sprintf(
			"upstream holds %d records; %d retrieved. %s",
			res.MatchingRecords, retrieved, narrowingHint(res, want, e.Cfg.MaxAll))
	case !haveTotal && retrieved >= want:
		// Without a total we cannot prove completeness: a full page is exactly
		// what a capped answer looks like.
		res.Truncated = true
		res.TruncationNote = fmt.Sprintf(
			"retrieved the full %d records requested and upstream reported no total, so more may exist. %s",
			want, narrowingHint(res, want, e.Cfg.MaxAll))
	}
}

func narrowingHint(res *Result, want, maxAll int) string {
	if want < maxAll {
		return "Raise --limit or pass --all for more."
	}
	if res.Kind == string(thc.KindRDNS) && !res.Block {
		return fmt.Sprintf("This is the ceiling (%d, the upstream CSV limit); narrow with --tld or --apex.", maxAll)
	}
	return fmt.Sprintf("This is the ceiling (%d, the upstream CSV limit) and upstream cannot paginate beyond it.", maxAll)
}

// pace waits for rate-limit replenishment when the budget is nearly spent. The
// service is free and asks not to be abused; a bulk run that ignores the
// budget is exactly the abuse it means.
func (e *Engine) pace(rl thc.RateLimit) {
	if rl.Unknown() || rl.Remaining > e.Cfg.MinRemaining {
		return
	}
	if e.Sleep == nil {
		return
	}
	// Wait for enough replenishment to climb back above the floor. Upstream
	// reports its refill rate; fall back to a conservative 0.5/sec if absent.
	rate := rl.Rate
	if rate <= 0 {
		rate = 0.5
	}
	need := float64(e.Cfg.MinRemaining-rl.Remaining) + 1
	e.Sleep(time.Duration(need / rate * float64(time.Second)))
}

// dedup removes rows upstream returns more than once.
//
// The rdns face can emit the same (domain, ip_address) pair twice, differing
// only in whether tld is populated — roughly 15% of rows on a /24. The variant
// carrying more information wins, so dropping a duplicate never loses a field.
func dedup(in []thc.Record) []thc.Record {
	type key struct{ domain, ip string }
	at := map[key]int{}
	out := make([]thc.Record, 0, len(in))

	for _, r := range in {
		k := key{strings.ToLower(r.Domain), r.IPAddress}
		if idx, seen := at[k]; seen {
			if fieldCount(r) > fieldCount(out[idx]) {
				out[idx] = r
			}
			continue
		}
		at[k] = len(out)
		out = append(out, r)
	}
	return out
}

// fieldCount scores how much a record actually carries, so the richer of two
// duplicates survives.
func fieldCount(r thc.Record) int {
	n := 0
	for _, v := range []string{r.ApexDomain, r.TLD, r.IPAddress, r.ASN, r.Org, r.Country, r.City, r.LastSeen} {
		if v != "" {
			n++
		}
	}
	return n
}

// SortRecords orders records by domain for stable, diffable output. Upstream
// order is already roughly sorted, but pagination and dedup can disturb it.
func SortRecords(rs []thc.Record) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Domain != rs[j].Domain {
			return rs[i].Domain < rs[j].Domain
		}
		return rs[i].IPAddress < rs[j].IPAddress
	})
}
