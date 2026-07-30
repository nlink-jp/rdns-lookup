package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
		MCPInlineMax: 3,
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

// An agent's context is scarcer than disk, so a large result becomes a file.
func TestLargeResultGoesToFile(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{page: pageWith("a.com", "b.com", "c.com", "d.com", "e.com")})
	ws := t.TempDir()
	text, isErr := toolText(t, drive(t, e, cfg,
		call("lookup_rdns", `{"ip_address":"1.1.1.1","workspace_root":`+jsonString(ws)+`}`))[0])
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	path, ok := res["records_file"].(string)
	if !ok || path == "" {
		t.Fatalf("result should carry records_file: %v", res)
	}
	if res["records_count"].(float64) != 5 {
		t.Errorf("records_count = %v, want 5", res["records_count"])
	}
	if inner, ok := res["result"].(map[string]any); !ok {
		t.Error("result summary missing")
	} else if _, has := inner["records"]; has {
		t.Error("the summary must not repeat the records inline")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read records file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 5 {
		t.Fatalf("file has %d lines, want 5", len(lines))
	}
	var rec thc.Record
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("each line should be a record object: %v", err)
	}
	if rec.Domain == "" {
		t.Error("record has no domain")
	}
	if filepath.Dir(path) != ws {
		t.Errorf("file %q should sit in the supplied workspace", path)
	}
}

// With nowhere to write, cap inline rather than flood the caller — and say so.
func TestLargeResultWithoutWorkspaceIsCappedAndSaysSo(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{page: pageWith("a.com", "b.com", "c.com", "d.com", "e.com")})
	text, isErr := toolText(t, drive(t, e, cfg, call("lookup_rdns", `{"ip_address":"1.1.1.1"}`))[0])
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	records := res["records"].([]any)
	if len(records) != cfg.MCPInlineMax {
		t.Errorf("got %d records inline, want the cap %d", len(records), cfg.MCPInlineMax)
	}
	if res["truncated"] != true {
		t.Error("a capped result must be marked truncated")
	}
	note := res["truncation_note"].(string)
	if !strings.Contains(note, "workspace_root") {
		t.Errorf("note %q should mention workspace_root", note)
	}
}

// A CIDR block contains '/', which must not become a path separator.
func TestWorkspaceFilenameIsSafeForBlocks(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{page: pageWith("a.com", "b.com", "c.com", "d.com")})
	ws := t.TempDir()
	text, _ := toolText(t, drive(t, e, cfg,
		call("lookup_rdns", `{"ip_address":"1.1.1.0/24","workspace_root":`+jsonString(ws)+`}`))[0])
	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	path := res["records_file"].(string)
	if filepath.Dir(path) != ws {
		t.Errorf("file %q escaped into a subdirectory", path)
	}
	if strings.Contains(filepath.Base(path), "/") {
		t.Errorf("filename %q contains a separator", filepath.Base(path))
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

func TestSafeSlug(t *testing.T) {
	tests := map[string]string{
		"1.1.1.1":      "1.1.1.1",
		"1.1.1.0/24":   "1.1.1.0_24",
		"2404:6800::1": "2404_6800__1",
		"example.com":  "example.com",
		"a b":          "a_b",
	}
	for in, want := range tests {
		if got := safeSlug(in); got != want {
			t.Errorf("safeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
