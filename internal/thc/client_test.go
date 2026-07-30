package thc

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient wires a Client at a test server.
func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{HTTP: srv.Client(), BaseURL: srv.URL, UserAgent: "rdns-lookup/test"}
}

// rateLimitHeaders mirrors what ip.thc.org sends on every response.
func rateLimitHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Ratelimit-Limit", "250")
	w.Header().Set("X-Ratelimit-Remaining", "249")
	w.Header().Set("X-Ratelimit-Rate", "0.50")
}

// This body is the real shape observed from ip.thc.org on 2026-07-30.
const rdnsJSONBody = `{
  "comment": "Free Service!, Do not abuse",
  "processed_ip_address": "142.251.43.46",
  "matching_records": 110,
  "domains": [
    {"apex_domain":"1e100.net","domain":"bkk02s01-in-f14.1e100.net","country":"US","city":"Queens","asn":"15169","tld":"net","organization":"GOOGLE","ip_address":"142.251.43.46"},
    {"apex_domain":"2rkkem2jzi.com","domain":"2rkkem2jzi.com","country":"US","city":"Queens","asn":"15169","tld":"","organization":"GOOGLE","ip_address":"142.251.43.46"}
  ],
  "next_page_state": "eyJuZXh0X3BhZ2UiOjJ9"
}`

func TestFetchJSONRDNS(t *testing.T) {
	var gotPath, gotBody string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		rateLimitHeaders(w)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, rdnsJSONBody)
	}))

	page, err := c.Fetch(Request{Kind: KindRDNS, Query: "142.251.43.46", Limit: 10})
	if err != nil {
		t.Fatalf("Fetch returned error %v", err)
	}
	if gotPath != "/lookup" {
		t.Errorf("path = %q, want /lookup", gotPath)
	}
	if !strings.Contains(gotBody, `"ip_address":"142.251.43.46"`) {
		t.Errorf("request body %q should carry ip_address", gotBody)
	}
	if len(page.Records) != 2 {
		t.Fatalf("got %d records, want 2", len(page.Records))
	}
	first := page.Records[0]
	if first.Domain != "bkk02s01-in-f14.1e100.net" || first.ApexDomain != "1e100.net" ||
		first.ASN != "15169" || first.Org != "GOOGLE" || first.IPAddress != "142.251.43.46" {
		t.Errorf("first record mismapped: %+v", first)
	}
	if page.MatchingRecords != 110 {
		t.Errorf("MatchingRecords = %d, want 110", page.MatchingRecords)
	}
	if page.NextPageState != "eyJuZXh0X3BhZ2UiOjJ9" {
		t.Errorf("NextPageState = %q", page.NextPageState)
	}
	if page.Route != RouteJSON {
		t.Errorf("Route = %q, want json", page.Route)
	}
	if page.RateLimit.Limit != 250 || page.RateLimit.Remaining != 249 || page.RateLimit.Rate != 0.5 {
		t.Errorf("RateLimit = %+v", page.RateLimit)
	}
}

// Upstream's docs call the subdomains key "subdomains"; every real response
// uses "domains". The observed name is what we must decode.
func TestFetchJSONSubdomainsUsesDomainsKey(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rateLimitHeaders(w)
		_, _ = io.WriteString(w, `{"matching_records":4291,"domains":[
			{"domain":"github.com","last_seen_on":"2026-07-17"},
			{"domain":"01org.github.com","last_seen_on":"2024-12-11"}],"next_page_state":""}`)
	}))
	page, err := c.Fetch(Request{Kind: KindSubdomains, Query: "github.com", Limit: 10})
	if err != nil {
		t.Fatalf("Fetch returned error %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("got %d records, want 2", len(page.Records))
	}
	if page.Records[0].Domain != "github.com" || page.Records[0].LastSeen != "2026-07-17" {
		t.Errorf("record mismapped: %+v", page.Records[0])
	}
}

// The cnames face returns bare strings, not objects.
func TestFetchJSONCNAMEsBareStrings(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rateLimitHeaders(w)
		_, _ = io.WriteString(w, `{"matching_records":3,"domains":["266.icu","book.72moc.com"],"next_page_state":""}`)
	}))
	page, err := c.Fetch(Request{Kind: KindCNAMEs, Query: "github.io", Limit: 10})
	if err != nil {
		t.Fatalf("Fetch returned error %v", err)
	}
	if len(page.Records) != 2 || page.Records[0].Domain != "266.icu" {
		t.Fatalf("records = %+v", page.Records)
	}
}

// The load-bearing CSV test: upstream's header row is rotated by one column
// relative to the data, so a positional mapping is the only correct reading.
// These bytes are the real response for 1.1.1.1, verified field-by-field
// against the JSON face for the same query.
func TestParseCSVCorrectsRotatedHeader(t *testing.T) {
	const body = "0-kb.com,0-kb.com,,,,13335,CLOUDFLARENET,1.1.1.1\n" +
		"0.pizza,0.pizza,,,,13335,CLOUDFLARENET,1.1.1.1\n"
	recs, err := parseCSV(KindRDNS, []byte(body))
	if err != nil {
		t.Fatalf("parseCSV returned error %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	r := recs[0]
	// If the declared header were trusted, ip_address would hold "0-kb.com"
	// and every other field would shift with it.
	if r.IPAddress != "1.1.1.1" {
		t.Errorf("IPAddress = %q, want 1.1.1.1 (header rotation not corrected)", r.IPAddress)
	}
	if r.ApexDomain != "0-kb.com" || r.Domain != "0-kb.com" {
		t.Errorf("domain fields = {%q, %q}, want both 0-kb.com", r.ApexDomain, r.Domain)
	}
	if r.ASN != "13335" || r.Org != "CLOUDFLARENET" {
		t.Errorf("AS fields = {%q, %q}, want {13335, CLOUDFLARENET}", r.ASN, r.Org)
	}
	if r.TLD != "" || r.Country != "" || r.City != "" {
		t.Errorf("empty fields should stay empty, got {%q, %q, %q}", r.TLD, r.Country, r.City)
	}
}

// A CSV with the full enrichment populated pins every column position.
func TestParseCSVAllColumns(t *testing.T) {
	const body = "1e100.net,bkk02s01-in-f14.1e100.net,net,US,Queens,15169,GOOGLE,142.251.43.46\n"
	recs, err := parseCSV(KindRDNS, []byte(body))
	if err != nil {
		t.Fatalf("parseCSV returned error %v", err)
	}
	want := Record{
		ApexDomain: "1e100.net", Domain: "bkk02s01-in-f14.1e100.net", TLD: "net",
		Country: "US", City: "Queens", ASN: "15169", Org: "GOOGLE", IPAddress: "142.251.43.46",
	}
	if recs[0] != want {
		t.Errorf("record = %+v, want %+v", recs[0], want)
	}
}

// hide_header is always sent, but if upstream ignored it the header must not
// become a record named "ipAddress".
func TestParseCSVDropsHeaderRow(t *testing.T) {
	const body = "ipAddress,apexDomain,subdomain,tld,country,city,asn,organization\n" +
		"0-kb.com,0-kb.com,,,,13335,CLOUDFLARENET,1.1.1.1\n"
	recs, err := parseCSV(KindRDNS, []byte(body))
	if err != nil {
		t.Fatalf("parseCSV returned error %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1 (header not dropped)", len(recs))
	}
}

func TestParseCSVSingleColumnKinds(t *testing.T) {
	for _, kind := range []Kind{KindSubdomains, KindCNAMEs} {
		recs, err := parseCSV(kind, []byte("subdomain\ngithub.com\n01org.github.com\n\n"))
		if err != nil {
			t.Fatalf("parseCSV(%s) returned error %v", kind, err)
		}
		if len(recs) != 2 || recs[0].Domain != "github.com" || recs[1].Domain != "01org.github.com" {
			t.Errorf("parseCSV(%s) = %+v", kind, recs)
		}
	}
}

func TestFetchCSVPinsHideHeader(t *testing.T) {
	var gotQuery, gotPath string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		rateLimitHeaders(w)
		w.Header().Set("Content-Type", "text/csv")
		_, _ = io.WriteString(w, "0-kb.com,0-kb.com,,,,13335,CLOUDFLARENET,1.1.1.1\n")
	}))
	page, err := c.Fetch(Request{Kind: KindRDNS, Query: "1.1.1.1", Limit: 5000, Route: RouteCSV})
	if err != nil {
		t.Fatalf("Fetch returned error %v", err)
	}
	if gotPath != "/download" {
		t.Errorf("path = %q, want /download", gotPath)
	}
	if !strings.Contains(gotQuery, "hide_header=true") {
		t.Errorf("query %q must pin hide_header=true", gotQuery)
	}
	if !strings.Contains(gotQuery, "limit=5000") {
		t.Errorf("query %q should carry the limit", gotQuery)
	}
	if page.Route != RouteCSV {
		t.Errorf("Route = %q, want csv", page.Route)
	}
	// The CSV face reports no total, so the count we hold is all we know.
	if page.MatchingRecords != 1 {
		t.Errorf("MatchingRecords = %d, want 1 (row count)", page.MatchingRecords)
	}
	if page.NextPageState != "" {
		t.Errorf("CSV cannot paginate, got NextPageState %q", page.NextPageState)
	}
}

func TestFetchCapsLimitToFaceMaximum(t *testing.T) {
	var gotBody, gotQuery string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rateLimitHeaders(w)
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			_, _ = io.WriteString(w, `{"domains":[],"next_page_state":""}`)
			return
		}
		gotQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, "")
	}))

	// The JSON face silently truncates above 100, so never ask for more.
	if _, err := c.Fetch(Request{Kind: KindRDNS, Query: "1.1.1.1", Limit: 5000}); err != nil {
		t.Fatalf("json fetch error %v", err)
	}
	if !strings.Contains(gotBody, `"limit":100`) {
		t.Errorf("json body %q should cap limit at %d", gotBody, JSONMaxLimit)
	}

	if _, err := c.Fetch(Request{Kind: KindRDNS, Query: "1.1.1.1", Limit: 999999, Route: RouteCSV}); err != nil {
		t.Fatalf("csv fetch error %v", err)
	}
	if !strings.Contains(gotQuery, "limit=50000") {
		t.Errorf("csv query %q should cap limit at %d", gotQuery, CSVMaxLimit)
	}
}

// Upstream reports a bad target as HTTP 406 with a JSON body.
func TestFetchInvalidTarget(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rateLimitHeaders(w)
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = io.WriteString(w, `{"status":"error","error":"invalid ip"}`)
	}))
	_, err := c.Fetch(Request{Kind: KindRDNS, Query: "142.251.43.0/25", Limit: 10})
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("error = %v, want ErrInvalidTarget", err)
	}
	if !IsInvalidTarget(err) {
		t.Error("IsInvalidTarget should report true")
	}
}

// An error envelope can also arrive with HTTP 200.
func TestFetchErrorEnvelopeOn200(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rateLimitHeaders(w)
		_, _ = io.WriteString(w, `{"status":"error","error":"invalid ip"}`)
	}))
	if _, err := c.Fetch(Request{Kind: KindRDNS, Query: "x", Limit: 10}); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("error = %v, want ErrInvalidTarget", err)
	}
}

// The CSV face returns a JSON error body despite the text/csv Accept.
func TestFetchCSVErrorEnvelope(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rateLimitHeaders(w)
		_, _ = io.WriteString(w, `{"status":"error","error":"invalid ip"}`)
	}))
	_, err := c.Fetch(Request{Kind: KindRDNS, Query: "x", Limit: 10, Route: RouteCSV})
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("error = %v, want ErrInvalidTarget", err)
	}
}

func TestFetchRateLimited(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	if _, err := c.Fetch(Request{Kind: KindRDNS, Query: "1.1.1.1", Limit: 10}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}

func TestFetchServerError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "upstream down")
	}))
	_, err := c.Fetch(Request{Kind: KindRDNS, Query: "1.1.1.1", Limit: 10})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadGateway || !strings.Contains(apiErr.Error(), "upstream down") {
		t.Errorf("APIError = %+v", apiErr)
	}
}

// Missing rate-limit headers must read as unknown, never as exhausted.
func TestRateLimitUnknownWhenHeadersAbsent(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"domains":[],"next_page_state":""}`)
	}))
	page, err := c.Fetch(Request{Kind: KindRDNS, Query: "1.1.1.1", Limit: 10})
	if err != nil {
		t.Fatalf("Fetch returned error %v", err)
	}
	if !page.RateLimit.Unknown() {
		t.Errorf("RateLimit %+v should report Unknown", page.RateLimit)
	}
}

func TestFetchKeepRaw(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rateLimitHeaders(w)
		_, _ = io.WriteString(w, rdnsJSONBody)
	}))
	page, err := c.Fetch(Request{Kind: KindRDNS, Query: "142.251.43.46", Limit: 10, KeepRaw: true})
	if err != nil {
		t.Fatalf("Fetch returned error %v", err)
	}
	if len(page.Raw) == 0 {
		t.Error("Raw should be retained when KeepRaw is set")
	}

	page, err = c.Fetch(Request{Kind: KindRDNS, Query: "142.251.43.46", Limit: 10})
	if err != nil {
		t.Fatalf("Fetch returned error %v", err)
	}
	if len(page.Raw) != 0 {
		t.Error("Raw should be empty when KeepRaw is unset")
	}
}

func TestFetchSendsUserAgentAndFilters(t *testing.T) {
	var gotUA, gotBody string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		rateLimitHeaders(w)
		_, _ = io.WriteString(w, `{"domains":[],"next_page_state":""}`)
	}))
	_, err := c.Fetch(Request{
		Kind: KindRDNS, Query: "1.1.1.1", Limit: 10,
		TLDs: []string{"com", "net"}, ApexDomain: "example.com",
	})
	if err != nil {
		t.Fatalf("Fetch returned error %v", err)
	}
	if gotUA != "rdns-lookup/test" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if !strings.Contains(gotBody, `"tld":["com","net"]`) {
		t.Errorf("body %q should carry the tld filter", gotBody)
	}
	if !strings.Contains(gotBody, `"apex_domain":"example.com"`) {
		t.Errorf("body %q should carry the apex filter", gotBody)
	}
}

func TestFetchUnknownKind(t *testing.T) {
	c := &Client{HTTP: http.DefaultClient}
	if _, err := c.Fetch(Request{Kind: Kind("bogus"), Query: "x"}); err == nil {
		t.Error("an unknown kind should error before any request")
	}
	if _, err := c.Fetch(Request{Kind: Kind("bogus"), Query: "x", Route: RouteCSV}); err == nil {
		t.Error("an unknown kind should error before any CSV request")
	}
}

func TestFetchEmptyResults(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rateLimitHeaders(w)
		_, _ = io.WriteString(w,
			`{"comment":"Could not fetch result count, matching_records will be zero, this can happen for /8 blocks",`+
				`"matching_records":0,"domains":[],"next_page_state":""}`)
	}))
	page, err := c.Fetch(Request{Kind: KindRDNS, Query: "2404:6800:4004:80e::200e", Limit: 10})
	if err != nil {
		t.Fatalf("an empty index is not an error, got %v", err)
	}
	if len(page.Records) != 0 || page.MatchingRecords != 0 {
		t.Errorf("page = %+v, want empty", page)
	}
}
