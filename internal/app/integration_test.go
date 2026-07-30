package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// upstreamStub stands in for ip.thc.org, exercising the whole
// flags → config → engine → thc → output path without the network. It counts
// requests so cache behaviour is observable.
func upstreamStub(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	mux := http.NewServeMux()

	mux.HandleFunc("/lookup", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			IPAddress string   `json:"ip_address"`
			TLD       []string `json:"tld"`
			Limit     int      `json:"limit"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		rateHeaders(w)
		if body.IPAddress == "203.0.113.9" { // nothing indexed
			_, _ = io.WriteString(w, `{"matching_records":0,"domains":[],"next_page_state":""}`)
			return
		}
		if body.IPAddress == "198.51.100.7" { // upstream rejects it
			w.WriteHeader(http.StatusNotAcceptable)
			_, _ = io.WriteString(w, `{"status":"error","error":"invalid ip"}`)
			return
		}
		_, _ = io.WriteString(w, `{"matching_records":2,"domains":[
			{"apex_domain":"1e100.net","domain":"b.1e100.net","country":"US","city":"Queens","asn":"15169","tld":"net","organization":"GOOGLE","ip_address":"142.251.43.46"},
			{"apex_domain":"example.com","domain":"a.example.com","country":"","city":"","asn":"15169","tld":"","organization":"GOOGLE","ip_address":"142.251.43.46"}
		],"next_page_state":""}`)
	})

	mux.HandleFunc("/lookup/subdomains", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rateHeaders(w)
		_, _ = io.WriteString(w, `{"matching_records":2,"domains":[
			{"domain":"github.com","last_seen_on":"2026-07-17"},
			{"domain":"api.github.com","last_seen_on":"2026-07-10"}],"next_page_state":""}`)
	})

	mux.HandleFunc("/lookup/cnames", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rateHeaders(w)
		_, _ = io.WriteString(w, `{"matching_records":2,"domains":["a.example.org","b.example.org"],"next_page_state":""}`)
	})

	// The CSV face, reached whenever more than 100 records are requested. These
	// rows carry the real column order, not the header upstream declares.
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rateHeaders(w)
		if r.URL.Query().Get("hide_header") != "true" {
			t.Errorf("CSV request must pin hide_header=true, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/csv")
		_, _ = io.WriteString(w, "1e100.net,b.1e100.net,net,US,Queens,15169,GOOGLE,142.251.43.46\n"+
			"example.com,a.example.com,,,,15169,GOOGLE,142.251.43.46\n")
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls
}

func rateHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Ratelimit-Limit", "250")
	w.Header().Set("X-Ratelimit-Remaining", "249")
	w.Header().Set("X-Ratelimit-Rate", "0.50")
}

// wire points the CLI at the stub with isolated config and cache.
func wire(t *testing.T) *atomic.Int32 {
	t.Helper()
	srv, calls := upstreamStub(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("RDNS_LOOKUP_CACHE_DIR", t.TempDir())
	t.Setenv("RDNS_LOOKUP_BASE_URL", srv.URL)
	return calls
}

func TestRDNSTextOutput(t *testing.T) {
	wire(t)
	var stdout, stderr bytes.Buffer
	if got := runLookup("rdns", []string{"142.251.43.46"}, "test", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d, stderr %q", got, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"142.251.43.46", "2 records", "ip.thc.org (json face)",
		"a.example.com", "b.1e100.net", "AS15169 GOOGLE", "rate budget 249/250",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output should contain %q, got:\n%s", want, out)
		}
	}
	// Records are sorted, so output is stable and diffable.
	if strings.Index(out, "a.example.com") > strings.Index(out, "b.1e100.net") {
		t.Error("records should be sorted by domain")
	}
}

func TestRDNSJSONOutput(t *testing.T) {
	wire(t)
	var stdout, stderr bytes.Buffer
	if got := runLookup("rdns", []string{"--json", "142.251.43.46"}, "test", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d, stderr %q", got, stderr.String())
	}
	var res map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("output is not a single JSON object: %v\n%s", err, stdout.String())
	}
	if res["kind"] != "rdns" || res["source"] != "ip.thc.org" || res["route"] != "json" {
		t.Errorf("provenance fields wrong: %v", res)
	}
	if res["count"].(float64) != 2 || res["matching_records"].(float64) != 2 {
		t.Errorf("counts wrong: %v", res)
	}
	if res["truncated"] != false {
		t.Error("a complete set must not be flagged truncated")
	}
}

// Multiple targets emit JSONL, one object per line, so the output stays pipeable.
func TestBulkJSONIsJSONL(t *testing.T) {
	wire(t)
	var stdout, stderr bytes.Buffer
	got := runLookup("rdns", []string{"--json", "142.251.43.46", "8.8.8.8"}, "test", &stdout, &stderr)
	if got != exitOK {
		t.Fatalf("exit = %d, stderr %q", got, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (JSONL)", len(lines))
	}
	for i, line := range lines {
		var res map[string]any
		if err := json.Unmarshal([]byte(line), &res); err != nil {
			t.Errorf("line %d is not JSON: %v", i, err)
		}
	}
}

func TestBulkFromStdin(t *testing.T) {
	wire(t)
	stdin = strings.NewReader("142.251.43.46\n# comment\n8.8.8.8\n")
	t.Cleanup(func() { stdin = os.Stdin })
	var stdout, stderr bytes.Buffer
	if got := runLookup("rdns", []string{"--json"}, "test", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d, stderr %q", got, stderr.String())
	}
	if n := len(strings.Split(strings.TrimSpace(stdout.String()), "\n")); n != 2 {
		t.Errorf("got %d results from stdin, want 2", n)
	}
}

func TestBulkFromInputFile(t *testing.T) {
	wire(t)
	path := filepath.Join(t.TempDir(), "targets.txt")
	if err := os.WriteFile(path, []byte("142.251.43.46\n8.8.8.8\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if got := runLookup("rdns", []string{"--json", "--input", path}, "test", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d, stderr %q", got, stderr.String())
	}
	if n := len(strings.Split(strings.TrimSpace(stdout.String()), "\n")); n != 2 {
		t.Errorf("got %d results, want 2", n)
	}
}

func TestSubdomainsKeepsLastSeen(t *testing.T) {
	wire(t)
	var stdout, stderr bytes.Buffer
	if got := runLookup("subdomains", []string{"github.com"}, "test", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d, stderr %q", got, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "api.github.com") {
		t.Errorf("output missing a subdomain:\n%s", out)
	}
	// last_seen_on is the freshness signal, so it must reach the user.
	if !strings.Contains(out, "last seen 2026-07-17") {
		t.Errorf("output should show last-seen dates:\n%s", out)
	}
}

func TestCNAMEsOutput(t *testing.T) {
	wire(t)
	var stdout, stderr bytes.Buffer
	if got := runLookup("cnames", []string{"example.com"}, "test", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d, stderr %q", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "a.example.org") {
		t.Errorf("output = %q", stdout.String())
	}
}

// Nothing indexed is a successful answer with its own exit code, not an error.
func TestNoRecordsExitCode(t *testing.T) {
	wire(t)
	var stdout, stderr bytes.Buffer
	got := runLookup("rdns", []string{"203.0.113.9"}, "test", &stdout, &stderr)
	if got != exitNoRecord {
		t.Fatalf("exit = %d, want %d", got, exitNoRecord)
	}
	if !strings.Contains(stdout.String(), "nothing indexed") {
		t.Errorf("output should say nothing was indexed, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("an empty answer is not an error, stderr = %q", stderr.String())
	}
}

// An upstream rejection is an operational error, not an empty answer.
func TestUpstreamRejectionIsError(t *testing.T) {
	wire(t)
	var stdout, stderr bytes.Buffer
	got := runLookup("rdns", []string{"198.51.100.7"}, "test", &stdout, &stderr)
	if got != exitError {
		t.Fatalf("exit = %d, want %d", got, exitError)
	}
	if !strings.Contains(stderr.String(), "invalid ip") {
		t.Errorf("stderr should carry the upstream message, got %q", stderr.String())
	}
}

// One bad target among good ones: the good results still print, and the exit
// code reports the failure.
func TestPartialFailureStillPrintsGoodResults(t *testing.T) {
	wire(t)
	var stdout, stderr bytes.Buffer
	got := runLookup("rdns", []string{"142.251.43.46", "142.251.43.0/25"}, "test", &stdout, &stderr)
	if got != exitError {
		t.Fatalf("exit = %d, want %d", got, exitError)
	}
	if !strings.Contains(stdout.String(), "a.example.com") {
		t.Errorf("the good result should still print, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "octet-boundary") {
		t.Errorf("stderr should explain the bad target, got %q", stderr.String())
	}
}

// A repeated lookup must not become a repeated request: that is the courtesy
// the free upstream asks for.
func TestCacheAvoidsSecondRequest(t *testing.T) {
	calls := wire(t)
	for i := 0; i < 3; i++ {
		var stdout, stderr bytes.Buffer
		if got := runLookup("rdns", []string{"142.251.43.46"}, "test", &stdout, &stderr); got != exitOK {
			t.Fatalf("run %d exit = %d, stderr %q", i, got, stderr.String())
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("made %d upstream requests for 3 identical lookups, want 1", n)
	}
}

func TestRefreshForcesRequest(t *testing.T) {
	calls := wire(t)
	var stdout, stderr bytes.Buffer
	if got := runLookup("rdns", []string{"142.251.43.46"}, "test", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d", got)
	}
	stdout.Reset()
	if got := runLookup("rdns", []string{"--refresh", "142.251.43.46"}, "test", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d", got)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("made %d requests, want 2", n)
	}
	if strings.Contains(stdout.String(), "cached") {
		t.Error("--refresh must not report a cache hit")
	}
}

// Above 100 records the JSON face silently truncates, so the CLI must switch to
// the CSV face — and read its rotated columns correctly.
func TestAllSwitchesToCSVFaceAndMapsColumns(t *testing.T) {
	wire(t)
	var stdout, stderr bytes.Buffer
	if got := runLookup("rdns", []string{"--all", "--json", "142.251.43.46"}, "test", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d, stderr %q", got, stderr.String())
	}
	var res map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if res["route"] != "csv" {
		t.Errorf("route = %v, want csv for --all", res["route"])
	}
	records := res["records"].([]any)
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	// The IP must land in ip_address, not in a domain field — the whole point
	// of correcting the rotated header.
	for _, r := range records {
		rec := r.(map[string]any)
		if rec["ip_address"] != "142.251.43.46" {
			t.Errorf("ip_address = %v, want 142.251.43.46 (CSV columns mismapped)", rec["ip_address"])
		}
		if !strings.Contains(rec["domain"].(string), ".") {
			t.Errorf("domain = %v, does not look like a domain", rec["domain"])
		}
	}
}

func TestFilterWithBlockIsRejected(t *testing.T) {
	calls := wire(t)
	var stdout, stderr bytes.Buffer
	got := runLookup("rdns", []string{"--tld", "com", "142.251.43.0/24"}, "test", &stdout, &stderr)
	if got != exitError {
		t.Fatalf("exit = %d, want %d", got, exitError)
	}
	if !strings.Contains(stderr.String(), "ignores") {
		t.Errorf("stderr should explain that upstream ignores the filter, got %q", stderr.String())
	}
	if calls.Load() != 0 {
		t.Error("a rejected filter must not reach upstream")
	}
}

func TestRawIncludesUpstreamBody(t *testing.T) {
	wire(t)
	var stdout, stderr bytes.Buffer
	if got := runLookup("rdns", []string{"--raw", "--json", "142.251.43.46"}, "test", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d, stderr %q", got, stderr.String())
	}
	var res map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := res["raw"]; !ok {
		t.Error("--raw should include the upstream body")
	}
}

// Truncation must be visible in plain text, not only in JSON.
func TestTruncationVisibleInTextOutput(t *testing.T) {
	wire(t)
	t.Setenv("RDNS_LOOKUP_DEFAULT_LIMIT", "1")
	var stdout, stderr bytes.Buffer
	if got := runLookup("rdns", []string{"142.251.43.46"}, "test", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d, stderr %q", got, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "TRUNCATED") {
		t.Errorf("a capped result must be marked truncated:\n%s", out)
	}
	if !strings.Contains(out, "note:") {
		t.Errorf("output should carry the explanatory note:\n%s", out)
	}
}

func TestRunDispatchesEveryLookupCommand(t *testing.T) {
	wire(t)
	for _, args := range [][]string{
		{"rdns", "142.251.43.46"},
		{"subdomains", "github.com"},
		{"cnames", "example.com"},
	} {
		if got := Run(args, "test"); got != exitOK {
			t.Errorf("Run(%v) = %d, want %d", args, got, exitOK)
		}
	}
	if got := Run([]string{"cache", "status"}, "test"); got != exitOK {
		t.Errorf("Run(cache status) = %d, want %d", got, exitOK)
	}
}

// A bad config must fail loudly rather than silently reverting to defaults.
func TestBadConfigIsReported(t *testing.T) {
	wire(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[query]\nmax_all = 60000\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var stdout, stderr bytes.Buffer
	got := runLookup("rdns", []string{"-c", path, "142.251.43.46"}, "test", &stdout, &stderr)
	if got != exitError {
		t.Fatalf("exit = %d, want %d", got, exitError)
	}
	if !strings.Contains(stderr.String(), "50000") {
		t.Errorf("stderr should explain the config problem, got %q", stderr.String())
	}
}
