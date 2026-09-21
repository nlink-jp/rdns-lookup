package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEveryToolSchemaIsClosed is the arch test organization ADR-021 §10
// requires: every registered tool's input schema sets
// additionalProperties:false, so a client validating arguments against the
// schema refuses a mistyped parameter instead of sending it on.
//
// The schemas are read off the wire — the tools/list response a client really
// receives — not from the literals, so the closing pass in closeSchemas is
// observed where it has to hold rather than where it is written.
//
// This test covers the client half only. The server half — the decoder that
// refuses a typo a client forwarded anyway — is
// TestUnknownArgumentIsRefusedByName.
func TestEveryToolSchemaIsClosed(t *testing.T) {
	e, cfg := newServer(t, fakeFetcher{})
	got := drive(t, e, cfg, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if len(got) == 0 {
		t.Fatal("tools/list produced no reply")
	}
	result, _ := got[0]["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	// Vacuity guard: with an empty list every assertion below passes without
	// having examined anything.
	if len(tools) == 0 {
		t.Fatalf("tools/list advertises no tools, so this test proves nothing: %v", got[0])
	}
	for _, tl := range tools {
		m, _ := tl.(map[string]any)
		name, _ := m["name"].(string)
		schema, ok := m["inputSchema"].(map[string]any)
		if !ok {
			t.Errorf("tool %q has no object inputSchema", name)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("tool %q: schema type = %v, want object", name, schema["type"])
		}
		if schema["additionalProperties"] != false {
			t.Errorf("tool %q: input schema does not set additionalProperties:false "+
				"(got %v) — a validating client would pass an agent's mistyped "+
				"argument through unnoticed (organization ADR-021 §10)",
				name, schema["additionalProperties"])
		}
	}
}

// TestUnknownArgumentIsRefusedByName is the enforcing half of org ADR-021 §4.
// It replaces TestUnknownArgumentIsAcceptedByTheServer, which pinned the
// opposite: that a typo reaching this server was accepted and ignored. Closing
// that gap is the deliberate behaviour change that test said would be decided
// on its own, so the test it left behind is inverted here rather than kept
// beside its contradiction.
//
// The closed schema binds clients that validate against it; a client that does
// not validate — or any caller speaking JSON-RPC directly — used to get its
// typo accepted, and a misspelled `limit` then fell back to the default while
// the result read as the bounded set that was asked for. `limit` is this
// server's only bound on a response, because every record retrieved comes back
// inline, so that was the argument least safe to drop.
//
// The message must name the offending field: a caller told only "invalid
// arguments" has to re-read the schema to find its own typo.
func TestUnknownArgumentIsRefusedByName(t *testing.T) {
	cases := []struct {
		tool  string
		args  string
		field string
	}{
		{"lookup_rdns", `{"ip_address":"1.1.1.1","limitt":5}`, "limitt"},
		{"lookup_rdns", `{"ip_address":"1.1.1.1","apex":"example.com"}`, "apex"},
		{"lookup_subdomains", `{"domain":"example.com","alll":true}`, "alll"},
		{"lookup_cnames", `{"target_domain":"example.com","refesh":true}`, "refesh"},
		// Each lookup tool now decodes its own schema, not a union of all
		// three: `ip_address` is a real field on lookup_rdns and no field at
		// all on the other two, which is what their schemas say.
		{"lookup_subdomains", `{"domain":"example.com","ip_address":"1.1.1.1"}`, "ip_address"},
		{"lookup_cnames", `{"target_domain":"example.com","tld":["com"]}`, "tld"},
		{"cache_status", `{"verbose":true}`, "verbose"},
		{"get_usage", `{"topic":"limits"}`, "topic"},
	}
	for _, tc := range cases {
		t.Run(tc.tool+"/"+tc.field, func(t *testing.T) {
			e, cfg := newServer(t, pageFetcher())
			text, isErr := toolText(t, drive(t, e, cfg, call(tc.tool, tc.args))[0])
			if !isErr {
				t.Fatalf("%s accepted unknown argument %q: %s", tc.tool, tc.field, text)
			}
			code, msg := structuredError(t, text)
			if code != "invalid_input" {
				t.Errorf("%s: error code = %q, want invalid_input", tc.tool, code)
			}
			// Matching the decoder's own phrasing, not just the field name: a
			// message like "provide 'domain'" happens to contain "domain", so
			// a bare substring test can pass for the wrong reason.
			want := `unknown field "` + tc.field + `"`
			if !strings.Contains(msg, want) {
				t.Errorf("%s: error does not name the offending argument: want %s, got %s", tc.tool, want, msg)
			}
		})
	}
}

// TestMalformedArgumentsAreRefused pins the other half of the decode. The old
// code did check its error, but only when `len(raw) > 0`; these wrong-typed
// arguments must be refused, and must not be reported as a missing target,
// which would contradict a request that named one.
func TestMalformedArgumentsAreRefused(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args string
	}{
		{"number for string", "lookup_rdns", `{"ip_address":1}`},
		{"string for integer", "lookup_rdns", `{"ip_address":"1.1.1.1","limit":"all"}`},
		{"string for array", "lookup_rdns", `{"ip_address":"1.1.1.1","tld":"com"}`},
		{"array for object", "lookup_subdomains", `["example.com"]`},
		{"string for boolean", "lookup_cnames", `{"target_domain":"example.com","all":"yes"}`},
	}
	for _, tc := range cases {
		t.Run(tc.tool+"/"+tc.name, func(t *testing.T) {
			e, cfg := newServer(t, pageFetcher())
			text, isErr := toolText(t, drive(t, e, cfg, call(tc.tool, tc.args))[0])
			if !isErr {
				t.Fatalf("%s accepted malformed arguments: %s", tc.tool, text)
			}
			_, msg := structuredError(t, text)
			if strings.Contains(msg, "provide '") {
				t.Errorf("%s reported the target as missing instead of malformed: %s", tc.tool, msg)
			}
			if !strings.Contains(msg, "arguments:") {
				t.Errorf("%s: error is not a decode error: %s", tc.tool, msg)
			}
		})
	}
}

// TestOmittedArgumentsStillMeanNone pins the boundary of the change: strict
// decoding must not turn a legitimately argument-less call into an error, and
// a genuinely absent target must still get the message that names it rather
// than a decode error.
func TestOmittedArgumentsStillMeanNone(t *testing.T) {
	for _, args := range []string{`{}`, `null`} {
		e, cfg := newServer(t, pageFetcher())
		text, isErr := toolText(t, drive(t, e, cfg, call("cache_status", args))[0])
		if isErr {
			t.Errorf("cache_status with arguments %q was refused: %s", args, text)
		}
	}
	e, cfg := newServer(t, pageFetcher())
	text, isErr := toolText(t, drive(t, e, cfg, call("lookup_rdns", `{}`))[0])
	if !isErr {
		t.Fatalf("lookup_rdns with no target should fail: %s", text)
	}
	if _, msg := structuredError(t, text); !strings.Contains(msg, "provide 'ip_address'") {
		t.Errorf("an absent target should still be named, not reported as a decode error: %s", msg)
	}
}

// structuredError decodes a {code, message} tool error, the shape errorResult
// produces.
func structuredError(t *testing.T, text string) (string, string) {
	t.Helper()
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(text), &e); err != nil {
		t.Fatalf("tool error is not structured JSON: %v (%s)", err, text)
	}
	return e.Code, e.Message
}

func pageFetcher() fakeFetcher {
	return fakeFetcher{page: pageWith("example.com")}
}
