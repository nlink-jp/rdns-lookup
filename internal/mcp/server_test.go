package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/rdns-lookup/internal/cache"
	"github.com/nlink-jp/rdns-lookup/internal/config"
	"github.com/nlink-jp/rdns-lookup/internal/engine"
	"github.com/nlink-jp/rdns-lookup/internal/thc"
)

// fakeFetcher replays a fixed page for every request.
type fakeFetcher struct {
	page *thc.Page
	err  error
}

func (f fakeFetcher) Fetch(thc.Request) (*thc.Page, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.page == nil {
		return &thc.Page{Route: thc.RouteJSON, RateLimit: thc.RateLimit{Limit: 250, Remaining: 249, Rate: 0.5}}, nil
	}
	return f.page, nil
}

func newServer(t *testing.T, f engine.Fetcher) (*engine.Engine, *config.Config) {
	t.Helper()
	cfg := &config.Config{
		DefaultLimit: 100,
		MaxAll:       thc.CSVMaxLimit,
		Dedup:        true,
		CacheDir:     t.TempDir(),
		CacheTTL:     24 * time.Hour,
		MinRemaining: 20,
	}
	e := &engine.Engine{
		Cfg:    cfg,
		Cache:  &cache.Store{Dir: cfg.CacheDir},
		Client: f,
		Now:    func() time.Time { return time.Unix(1_800_000_000, 0) },
		Sleep:  func(time.Duration) {},
	}
	return e, cfg
}

func pageWith(domains ...string) *thc.Page {
	recs := make([]thc.Record, 0, len(domains))
	for _, d := range domains {
		recs = append(recs, thc.Record{Domain: d, IPAddress: "1.1.1.1"})
	}
	return &thc.Page{
		Records: recs, MatchingRecords: len(recs), Route: thc.RouteJSON,
		RateLimit: thc.RateLimit{Limit: 250, Remaining: 249, Rate: 0.5},
	}
}

// drive runs the real Serve loop over newline-delimited JSON-RPC and decodes
// every reply.
func drive(t *testing.T, e *engine.Engine, cfg *config.Config, reqs ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := Serve(e, cfg, "test", strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out); err != nil {
		t.Fatalf("Serve returned error %v", err)
	}
	var got []map[string]any
	dec := json.NewDecoder(&out)
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("decode reply: %v", err)
		}
		got = append(got, m)
	}
	return got
}

// toolText returns the text payload of a tools/call reply, plus its isError.
func toolText(t *testing.T, reply map[string]any) (string, bool) {
	t.Helper()
	result, ok := reply["result"].(map[string]any)
	if !ok {
		t.Fatalf("reply has no result: %v", reply)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("reply has no content: %v", reply)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	isErr, _ := result["isError"].(bool)
	return text, isErr
}

func call(name, args string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

func TestInitialize(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	got := drive(t, e, cfg, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if len(got) != 1 {
		t.Fatalf("got %d replies, want 1", len(got))
	}
	res := got[0]["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" {
		t.Errorf("protocolVersion = %v, want the client's value echoed", res["protocolVersion"])
	}
	info := res["serverInfo"].(map[string]any)
	if info["name"] != "rdns-lookup" {
		t.Errorf("serverInfo.name = %v", info["name"])
	}
	if !strings.Contains(res["instructions"].(string), "get_usage") {
		t.Error("instructions should point at get_usage")
	}
}

func TestInitializeDefaultsProtocolVersion(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	got := drive(t, e, cfg, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	res := got[0]["result"].(map[string]any)
	if res["protocolVersion"] != defaultProtocolVersion {
		t.Errorf("protocolVersion = %v, want %q", res["protocolVersion"], defaultProtocolVersion)
	}
}

// A notification has no id and must never be answered.
func TestNotificationGetsNoReply(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	got := drive(t, e, cfg,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":null,"method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
	)
	if len(got) != 1 {
		t.Fatalf("got %d replies, want 1 (only the ping)", len(got))
	}
}

func TestUnknownMethod(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	got := drive(t, e, cfg, `{"jsonrpc":"2.0","id":1,"method":"nope"}`)
	errObj := got[0]["error"].(map[string]any)
	if errObj["code"].(float64) != -32601 {
		t.Errorf("code = %v, want -32601", errObj["code"])
	}
}

func TestUnknownTool(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	got := drive(t, e, cfg, call("nope", "{}"))
	errObj := got[0]["error"].(map[string]any)
	if errObj["code"].(float64) != -32602 {
		t.Errorf("code = %v, want -32602", errObj["code"])
	}
}

// Malformed JSON leaves the stream unrecoverable, so report and stop.
func TestMalformedJSONReportsParseError(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	var out bytes.Buffer
	if err := Serve(e, cfg, "test", strings.NewReader("{not json\n"), &out); err != nil {
		t.Fatalf("Serve returned error %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj := m["error"].(map[string]any)
	if errObj["code"].(float64) != -32700 {
		t.Errorf("code = %v, want -32700", errObj["code"])
	}
}

func TestToolsListAdvertisesEveryTool(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	got := drive(t, e, cfg, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := got[0]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"get_usage", "lookup_rdns", "lookup_subdomains", "lookup_cnames", "cache_status"} {
		if !names[want] {
			t.Errorf("tools/list is missing %q", want)
		}
	}
}

func TestGetUsageReturnsManual(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	text, isErr := toolText(t, drive(t, e, cfg, call("get_usage", "{}"))[0])
	if isErr {
		t.Error("get_usage should not be an error")
	}
	if !strings.Contains(text, "rdns-lookup MCP server") {
		t.Errorf("usage text looks wrong: %.80q", text)
	}
}

func TestLookupRDNSInline(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{page: pageWith("a.example.com", "b.example.com")})
	text, isErr := toolText(t, drive(t, e, cfg, call("lookup_rdns", `{"ip_address":"1.1.1.1"}`))[0])
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if res["kind"] != "rdns" || res["source"] != "ip.thc.org" {
		t.Errorf("result = %v", res)
	}
	if res["count"].(float64) != 2 {
		t.Errorf("count = %v, want 2", res["count"])
	}
	if _, ok := res["records"]; !ok {
		t.Error("a small result should be inline")
	}
}

func TestLookupSubdomainsAndCNAMEs(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{page: pageWith("a.example.com")})
	for _, tc := range []struct{ tool, args, kind string }{
		{"lookup_subdomains", `{"domain":"example.com"}`, "subdomains"},
		{"lookup_cnames", `{"target_domain":"example.com"}`, "cnames"},
	} {
		text, isErr := toolText(t, drive(t, e, cfg, call(tc.tool, tc.args))[0])
		if isErr {
			t.Fatalf("%s: unexpected error %s", tc.tool, text)
		}
		var res map[string]any
		if err := json.Unmarshal([]byte(text), &res); err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
		if res["kind"] != tc.kind {
			t.Errorf("%s: kind = %v, want %q", tc.tool, res["kind"], tc.kind)
		}
	}
}

func TestMissingTargetArgument(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	for _, tc := range []struct{ tool, field string }{
		{"lookup_rdns", "ip_address"},
		{"lookup_subdomains", "domain"},
		{"lookup_cnames", "target_domain"},
	} {
		text, isErr := toolText(t, drive(t, e, cfg, call(tc.tool, "{}"))[0])
		if !isErr {
			t.Errorf("%s without a target should be an error", tc.tool)
		}
		if !strings.Contains(text, "invalid_input") || !strings.Contains(text, tc.field) {
			t.Errorf("%s error %q should name %q", tc.tool, text, tc.field)
		}
	}
}

func TestInvalidTargetIsStructuredError(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	text, isErr := toolText(t, drive(t, e, cfg, call("lookup_rdns", `{"ip_address":"142.251.43.0/25"}`))[0])
	if !isErr {
		t.Fatal("a /25 block must be rejected")
	}
	var errObj map[string]string
	if err := json.Unmarshal([]byte(text), &errObj); err != nil {
		t.Fatalf("tool error is not structured JSON: %v", err)
	}
	if errObj["code"] != "invalid_input" {
		t.Errorf("code = %q, want invalid_input", errObj["code"])
	}
	if !strings.Contains(errObj["message"], "octet-boundary") {
		t.Errorf("message %q should explain the limit", errObj["message"])
	}
}

// Upstream ignores filters on a block, so the tool must refuse the combination.
func TestFilterWithBlockIsError(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{page: pageWith("a.example.com")})
	text, isErr := toolText(t, drive(t, e, cfg, call("lookup_rdns", `{"ip_address":"1.1.1.0/24","tld":["com"]}`))[0])
	if !isErr {
		t.Fatal("a filter with a block must be refused")
	}
	if !strings.Contains(text, "invalid_input") {
		t.Errorf("error %q should be invalid_input", text)
	}
}

// Nothing indexed is a real answer about the target, not a failure.
func TestNoRecordsIsNotAnError(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	text, isErr := toolText(t, drive(t, e, cfg, call("lookup_rdns", `{"ip_address":"1.1.1.1"}`))[0])
	if isErr {
		t.Fatalf("an empty index must not be an error: %s", text)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res["count"].(float64) != 0 {
		t.Errorf("count = %v, want 0", res["count"])
	}
	if res["source"] != "ip.thc.org" {
		t.Error("provenance should survive an empty answer")
	}
}

func TestRateLimitedMapsToItsOwnCode(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{err: thc.ErrRateLimited})
	text, isErr := toolText(t, drive(t, e, cfg, call("lookup_rdns", `{"ip_address":"1.1.1.1"}`))[0])
	if !isErr {
		t.Fatal("a rate-limited fetch should be an error result")
	}
	var errObj map[string]string
	if err := json.Unmarshal([]byte(text), &errObj); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errObj["code"] != "rate_limited" {
		t.Errorf("code = %q, want rate_limited", errObj["code"])
	}
	if !strings.Contains(errObj["message"], "wait") {
		t.Errorf("message %q should tell the caller to wait", errObj["message"])
	}
}

func TestNetworkErrorCode(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{err: &thc.APIError{StatusCode: 502, Detail: "bad gateway"}})
	text, _ := toolText(t, drive(t, e, cfg, call("lookup_rdns", `{"ip_address":"1.1.1.1"}`))[0])
	if !strings.Contains(text, "network_error") {
		t.Errorf("error %q should be network_error", text)
	}
}

// Every record retrieved comes back inline. The server used to spill a result
// past an inline cap to a caller-supplied workspace_root, which made it unusable
// by a client with no filesystem — and never reached past `limit` anyway, since
// `limit` bounds the upstream fetch itself.
func TestEveryRetrievedRecordIsReturnedInline(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{page: pageWith("a.com", "b.com", "c.com", "d.com", "e.com")})
	text, isErr := toolText(t, drive(t, e, cfg, call("lookup_rdns", `{"ip_address":"1.1.1.1"}`))[0])
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	records, _ := res["records"].([]any)
	if len(records) != 5 {
		t.Fatalf("got %d records inline, want all 5 that were retrieved", len(records))
	}
	if res["truncated"] == true {
		t.Error("nothing was held back, so the result must not be marked truncated")
	}
	for _, gone := range []string{"records_file", "records_count", "workspace"} {
		if strings.Contains(text, gone) {
			t.Errorf("result still carries %q — file mediation was not removed: %s", gone, text)
		}
	}
}

// `truncated` still means what it always meant: the upstream index held more
// than `limit` retrieved. That is the caller's signal to raise `limit`.
func TestTruncatedStillReportsAnIncompleteUpstreamSet(t *testing.T) {
	page := pageWith("a.com", "b.com")
	page.MatchingRecords = 900
	e, cfg := newServer(t, fakeFetcher{page: page})
	text, _ := toolText(t, drive(t, e, cfg, call("lookup_rdns", `{"ip_address":"1.1.1.1"}`))[0])
	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res["truncated"] != true {
		t.Errorf("an incomplete upstream set must stay marked truncated: %v", res)
	}
	if res["matching_records"].(float64) != 900 {
		t.Errorf("matching_records = %v, want the upstream total 900", res["matching_records"])
	}
}

func TestCacheStatus(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	text, isErr := toolText(t, drive(t, e, cfg, call("cache_status", "{}"))[0])
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res["cache_dir"] != cfg.CacheDir {
		t.Errorf("cache_dir = %v", res["cache_dir"])
	}
	if res["ttl_hours"].(float64) != 24 {
		t.Errorf("ttl_hours = %v, want 24", res["ttl_hours"])
	}
	if res["all_ceiling"].(float64) != float64(thc.CSVMaxLimit) {
		t.Errorf("all_ceiling = %v", res["all_ceiling"])
	}
	if res["source"] != "ip.thc.org" {
		t.Errorf("source = %v", res["source"])
	}
}

func TestBadArgumentsAreStructuredError(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	text, isErr := toolText(t, drive(t, e, cfg, call("lookup_rdns", `{"ip_address":123}`))[0])
	if !isErr {
		t.Fatal("mistyped arguments should be an error")
	}
	if !strings.Contains(text, "invalid_input") {
		t.Errorf("error %q should be invalid_input", text)
	}
}
