package mcp

import (
	"encoding/json"
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
// This test covers the client half only. The server half is not in place here;
// TestUnknownArgumentIsAcceptedByTheServer records that.
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

// TestUnknownArgumentIsAcceptedByTheServer records the half of the contract
// this server does NOT have, so the gap is visible in code and not only in a
// report.
//
// toolLookup decodes with plain json.Unmarshal, without
// DisallowUnknownFields, so an unknown argument is silently ignored rather
// than named in an error. The closed schema above binds clients that validate
// against it; a client that does not validate — or any caller speaking
// JSON-RPC directly — still gets its typo accepted, and a misspelled `limit`
// then falls back to the default while looking like it took effect.
//
// Closing that gap changes what existing callers get back (a silently-ignored
// argument becomes an error), so it is a deliberate behaviour change and not
// part of the schema sweep. When it is made, this test should fail and be
// replaced by its opposite.
func TestUnknownArgumentIsAcceptedByTheServer(t *testing.T) {
	e, cfg := newServer(t, pageFetcher())
	text, isErr := toolText(t, drive(t, e, cfg,
		call("lookup_rdns", `{"ip_address":"1.1.1.1","limitt":5}`))[0])
	if isErr {
		t.Fatalf("expected the unknown argument to be ignored, got an error: %s", text)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("result is not JSON: %v (%s)", err, text)
	}
	if res["source"] != "ip.thc.org" {
		t.Fatalf("the call did not actually succeed: %s", text)
	}
	t.Log("unknown argument 'limitt' was accepted and ignored: the closed " +
		"schema binds validating clients only, not this server")
}

func pageFetcher() fakeFetcher {
	return fakeFetcher{page: pageWith("example.com")}
}
