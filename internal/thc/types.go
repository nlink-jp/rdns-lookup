package thc

import (
	"errors"
	"fmt"
)

// DefaultBaseURL is the ip.thc.org API root.
const DefaultBaseURL = "https://ip.thc.org/api/v1"

// JSONMaxLimit is the largest page the JSON face will actually return.
// Upstream documents no ceiling and does not error on a bigger limit — it
// simply returns 100 rows — so asking for more is silently truncated. The
// engine switches to the CSV face above this.
const JSONMaxLimit = 100

// CSVMaxLimit is the largest single request the CSV face accepts, and so the
// effective ceiling on exhaustive retrieval: the CSV face has no pagination,
// and walking further through the JSON face costs hundreds of requests under a
// 0.5 req/sec budget.
const CSVMaxLimit = 50000

// Kind identifies which of the three relationship lookups is being made.
type Kind string

const (
	// KindRDNS maps an IP or IP block to the domains associated with it.
	KindRDNS Kind = "rdns"
	// KindSubdomains enumerates the subdomains of a domain.
	KindSubdomains Kind = "subdomains"
	// KindCNAMEs finds the domains that CNAME to a target domain.
	KindCNAMEs Kind = "cnames"
)

// Route records which upstream face answered, so provenance survives into the
// result and the cache.
type Route string

const (
	// RouteJSON is the paginating JSON face (≤100 rows per request).
	RouteJSON Route = "json"
	// RouteCSV is the bulk CSV face (≤50,000 rows, no pagination).
	RouteCSV Route = "csv"
)

// Record is one normalized row. Which fields are populated depends on Kind:
// rdns fills everything upstream provides, while subdomains fills Domain and
// LastSeen, and cnames fills Domain alone.
type Record struct {
	Domain     string `json:"domain"`
	ApexDomain string `json:"apex_domain,omitempty"`
	TLD        string `json:"tld,omitempty"`
	IPAddress  string `json:"ip_address,omitempty"`
	ASN        string `json:"asn,omitempty"`
	Org        string `json:"organization,omitempty"`
	Country    string `json:"country,omitempty"`
	City       string `json:"city,omitempty"`
	LastSeen   string `json:"last_seen_on,omitempty"`
}

// Page is one upstream response.
type Page struct {
	Records []Record
	// MatchingRecords is the upstream total hit count. It is 0 for very large
	// blocks, where upstream reports that it could not count, so it is a hint
	// rather than a guarantee.
	MatchingRecords int
	// NextPageState is the opaque pagination token; empty means no more pages.
	// Always empty for RouteCSV, which cannot paginate.
	NextPageState string
	Route         Route
	RateLimit     RateLimit
	// Raw is the untouched response body, retained only when the caller asked
	// for it.
	Raw []byte
}

// RateLimit is the budget upstream reports on every response. Numeric fields
// are -1 when the header was absent or unparseable, so "unknown" is never
// mistaken for "exhausted".
type RateLimit struct {
	Limit     int     `json:"limit"`
	Remaining int     `json:"remaining"`
	Rate      float64 `json:"rate_per_second"`
}

// Unknown reports whether upstream told us nothing about the budget.
func (r RateLimit) Unknown() bool { return r.Remaining < 0 }

// Request describes one upstream call.
type Request struct {
	Kind  Kind
	Query string // IP, CIDR block, or domain, already validated
	Limit int    // rows wanted from this request
	// TLDs and ApexDomain filter rdns results. Upstream silently ignores both
	// for block queries, so callers must not set them for a block; the engine
	// enforces that.
	TLDs       []string
	ApexDomain string
	// PageState continues a JSON-face walk; ignored on the CSV face.
	PageState string
	// Route selects the upstream face. Empty means RouteJSON.
	Route Route
	// KeepRaw retains the response body in Page.Raw.
	KeepRaw bool
}

// Sentinel errors distinguishing upstream conditions the caller must handle
// differently from a generic failure.
var (
	// ErrInvalidTarget is upstream's HTTP 406 "invalid ip": the query is not
	// something it will accept. Our own validation should catch these first,
	// so reaching this means upstream is stricter than we modelled.
	ErrInvalidTarget = errors.New("upstream rejected the target")
	// ErrRateLimited means the budget is exhausted; wait for replenishment.
	ErrRateLimited = errors.New("rate limited")
)

// APIError is a non-2xx response carrying upstream's own error text.
type APIError struct {
	StatusCode int
	Detail     string
}

func (e *APIError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("ip.thc.org returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("ip.thc.org returned HTTP %d: %s", e.StatusCode, e.Detail)
}

// csvColumns is the true positional schema of the rdns CSV face.
//
// Upstream's own header row is rotated by one relative to the data: it
// declares "ipAddress,apexDomain,subdomain,tld,country,city,asn,organization"
// while a row actually arrives as
// "apexDomain,subdomain,tld,country,city,asn,organization,ipAddress" — the IP
// is emitted last, not first. Trusting the header therefore mislabels every
// single field, so requests pin hide_header=true and rows are mapped through
// this constant instead. Verified against the JSON face for the same query on
// 2026-07-30.
var csvColumns = []string{
	"apex_domain", "domain", "tld", "country", "city", "asn", "organization", "ip_address",
}
