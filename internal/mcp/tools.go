package mcp

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nlink-jp/rdns-lookup/internal/engine"
	"github.com/nlink-jp/rdns-lookup/internal/query"
	"github.com/nlink-jp/rdns-lookup/internal/thc"
)

// usageMarkdown is the operating manual returned by the get_usage tool. Its
// coherence with the real tools and error codes is pinned by usage_test.go.
//
//go:embed usage.md
var usageMarkdown string

// Instructions is the initialize-time hint (surfaced via the MCP
// `instructions` field) that makes get_usage discoverable and steers clients
// away from common errors.
const Instructions = "rdns-lookup queries the free DNS index at ip.thc.org for the relationships around an " +
	"IP or a domain: lookup_rdns gives the domains associated with an address or octet-boundary block, " +
	"lookup_subdomains enumerates a domain's subdomains, and lookup_cnames finds the domains that CNAME to " +
	"a domain. It reads a third-party index, so no packet reaches the target under investigation — prefer it " +
	"as the opening move, before anything that resolves. Note that its rdns data is not PTR: it aggregates " +
	"PTR names, domains whose A records point at the address, and Certificate Transparency names. Always " +
	"read `truncated` and `matching_records` before concluding a set is complete. Every record retrieved is " +
	"returned inline, so `limit` is what bounds the response — ask for what you can hold. Tool errors are " +
	"structured JSON ({code, message}). Call " +
	"get_usage for the full tool reference and error-recovery table. No credentials are required."

// closeSchemas sets additionalProperties:false on every tool's top-level input
// schema, as organization ADR-021 §10 requires, so a client validating
// arguments against the schema refuses a mistyped parameter instead of sending
// it on.
//
// It is a pass over the finished list rather than a helper each schema has to
// call, so a tool added later as a plain literal cannot forget it — the rule is
// enforced by the one place every schema goes through, not by authors
// remembering. Only the top level is touched; a nested object that deliberately
// accepts free-form keys keeps whatever it declares.
//
// The schema binds validating clients only; decodeArgs below is what refuses
// an unknown argument that reaches this server anyway. ADR-021 §4 requires
// both halves, and this one is the declaration.
func closeSchemas(defs []map[string]any) []map[string]any {
	for _, def := range defs {
		if schema, ok := def["inputSchema"].(map[string]any); ok {
			schema["additionalProperties"] = false
		}
	}
	return defs
}

// decodeArgs decodes a tool's arguments strictly: an argument the tool does not
// declare is refused by name, and a malformed argument object is refused rather
// than read as an empty one. Every tool decodes through here.
//
// closeSchemas above is only the declared half of org ADR-021 §4 — what a
// schema-checking client refuses before the call. This is the half that
// actually refuses, and it is needed because not every client checks the
// schema, and a caller speaking JSON-RPC directly checks nothing. The plain
// json.Unmarshal this replaces accepted any field it did not recognize, so a
// misspelt `limit` fell back to the default while the result read as the
// bounded set that was asked for — and `limit` is this server's only bound on
// the response, because every record retrieved comes back inline.
func decodeArgs(raw json.RawMessage, into any) error {
	raw = bytes.TrimSpace(raw)
	// Omitted or null arguments mean the empty object, not an error: the
	// required-target check below produces the useful message in that case.
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return errors.New("arguments: " + err.Error())
	}
	return nil
}

// toolsList returns the advertised tool set with JSON Schema for each input.
func toolsList() any {
	limitProp := map[string]any{
		"type":        "integer",
		"description": "Records to retrieve (default 100, the cost of one upstream request). Every record retrieved comes back inline, so this also bounds the size of the response.",
	}
	allProp := map[string]any{
		"type":        "boolean",
		"description": "Retrieve up to the ceiling (50000). Costs more upstream budget; check `truncated` in the result.",
	}
	return map[string]any{
		"tools": closeSchemas([]map[string]any{
			{
				"name":        "get_usage",
				"description": "Return this server's operating manual (markdown): the tools, the result schema, and the error-recovery table. Call it once before first use.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			},
			{
				"name": "lookup_rdns",
				"description": "Domains associated with an IP address or octet-boundary CIDR block (/8, /16, /24, /32). " +
					"This is an aggregate index — PTR names, domains whose A records point at the address, and " +
					"Certificate Transparency names — not a PTR lookup, so it answers \"what else is hosted here\". " +
					"No packet reaches the address.",
				"inputSchema": map[string]any{
					"type":     "object",
					"required": []string{"ip_address"},
					"properties": map[string]any{
						"ip_address":  map[string]any{"type": "string", "description": "IP address, or a CIDR block on an octet boundary (e.g. 142.251.43.0/24). Other prefix lengths are rejected upstream."},
						"limit":       limitProp,
						"all":         allProp,
						"tld":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "TLD filter (e.g. [\"com\"]). Single addresses only — upstream ignores it for blocks, so passing it with a block is an error."},
						"apex_domain": map[string]any{"type": "string", "description": "Apex-domain filter (e.g. example.com). Single addresses only, same reason as tld."},
						"refresh":     map[string]any{"type": "boolean", "description": "Bypass the local cache and re-query."},
					},
				},
			},
			{
				"name":        "lookup_subdomains",
				"description": "Subdomains of a domain, from the same index. Each record carries the date it was last seen, which is the freshness signal. No packet reaches the domain.",
				"inputSchema": map[string]any{
					"type":     "object",
					"required": []string{"domain"},
					"properties": map[string]any{
						"domain":  map[string]any{"type": "string", "description": "Domain name (IDN ok), e.g. github.com."},
						"limit":   limitProp,
						"all":     allProp,
						"refresh": map[string]any{"type": "boolean", "description": "Bypass the local cache and re-query."},
					},
				},
			},
			{
				"name":        "lookup_cnames",
				"description": "Domains that point a CNAME at the given target domain — the reverse direction of an ordinary CNAME lookup. Useful for finding who depends on a hosting target. No packet reaches the domain.",
				"inputSchema": map[string]any{
					"type":     "object",
					"required": []string{"target_domain"},
					"properties": map[string]any{
						"target_domain": map[string]any{"type": "string", "description": "Target domain to search CNAMEs for, e.g. github.io."},
						"limit":         limitProp,
						"all":           allProp,
						"refresh":       map[string]any{"type": "boolean", "description": "Bypass the local cache and re-query."},
					},
				},
			},
			{
				"name":        "cache_status",
				"description": "Report the local result-cache state: entry count, TTL, and the default and ceiling record limits.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			},
		}),
	}
}

func (s *server) toolsCall(params json.RawMessage) (toolResult, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return toolResult{}, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	switch p.Name {
	case "get_usage":
		// No arguments — which still means "none", not "any".
		if err := decodeArgs(p.Arguments, &struct{}{}); err != nil {
			return errorResult("invalid_input", err.Error()), nil
		}
		return textResult(false, usageMarkdown), nil
	case "lookup_rdns":
		return s.toolLookup(thc.KindRDNS, p.Arguments), nil
	case "lookup_subdomains":
		return s.toolLookup(thc.KindSubdomains, p.Arguments), nil
	case "lookup_cnames":
		return s.toolLookup(thc.KindCNAMEs, p.Arguments), nil
	case "cache_status":
		if err := decodeArgs(p.Arguments, &struct{}{}); err != nil {
			return errorResult("invalid_input", err.Error()), nil
		}
		return s.toolCacheStatus(), nil
	default:
		return toolResult{}, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
	}
}

// commonArgs are the arguments all three lookup tools share. They are embedded
// rather than repeated so the three structs below cannot drift apart.
type commonArgs struct {
	Limit   int  `json:"limit"`
	All     bool `json:"all"`
	Refresh bool `json:"refresh"`
}

// The three lookup tools take one argument struct each, holding exactly what
// that tool's schema declares.
//
// This used to be one union struct covering all three. With strict decoding
// that would have kept a hole the schemas do not have: `ip_address` sent to
// lookup_subdomains decodes into a declared field, so the decoder accepts it
// and the tool then ignores it — the same silent-drop this change exists to
// remove, one step in. Splitting the structs lets the decoder enforce each
// tool's own schema instead of a rule written out beside it.
type rdnsArgs struct {
	IPAddress  string   `json:"ip_address"`
	TLD        []string `json:"tld"`
	ApexDomain string   `json:"apex_domain"`
	commonArgs
}

type subdomainsArgs struct {
	Domain string `json:"domain"`
	commonArgs
}

type cnamesArgs struct {
	TargetDomain string `json:"target_domain"`
	commonArgs
}

func (s *server) toolLookup(kind thc.Kind, raw json.RawMessage) toolResult {
	var (
		target, field string
		common        commonArgs
		tlds          []string
		apexDomain    string
	)
	switch kind {
	case thc.KindSubdomains:
		var a subdomainsArgs
		if err := decodeArgs(raw, &a); err != nil {
			return errorResult("invalid_input", err.Error())
		}
		target, field, common = a.Domain, "domain", a.commonArgs
	case thc.KindCNAMEs:
		var a cnamesArgs
		if err := decodeArgs(raw, &a); err != nil {
			return errorResult("invalid_input", err.Error())
		}
		target, field, common = a.TargetDomain, "target_domain", a.commonArgs
	default:
		var a rdnsArgs
		if err := decodeArgs(raw, &a); err != nil {
			return errorResult("invalid_input", err.Error())
		}
		target, field, common = a.IPAddress, "ip_address", a.commonArgs
		tlds, apexDomain = a.TLD, a.ApexDomain
	}
	if strings.TrimSpace(target) == "" {
		return errorResult("invalid_input", "provide '"+field+"'")
	}

	opts := engine.Options{
		Limit:      common.Limit,
		All:        common.All,
		TLDs:       tlds,
		ApexDomain: apexDomain,
		Refresh:    common.Refresh,
	}

	var (
		res *engine.Result
		err error
	)
	switch kind {
	case thc.KindSubdomains:
		res, err = s.e.LookupSubdomains(target, opts)
	case thc.KindCNAMEs:
		res, err = s.e.LookupCNAMEs(target, opts)
	default:
		res, err = s.e.LookupRDNS(target, opts)
	}

	switch {
	case errors.Is(err, engine.ErrNoRecords):
		// Nothing indexed is a real answer, not a failure: return the
		// populated result so the caller sees the provenance and the zero.
	case errors.Is(err, query.ErrInvalid), thc.IsInvalidTarget(err):
		return errorResult("invalid_input", err.Error())
	case errors.Is(err, thc.ErrRateLimited):
		return errorResult("rate_limited", err.Error()+": the upstream budget is spent; wait for replenishment (about 0.5 requests/second) and retry")
	case err != nil:
		return errorResult("network_error", err.Error())
	}

	engine.SortRecords(res.Records)
	return jsonResult(res)
}

func (s *server) toolCacheStatus() toolResult {
	return jsonResult(map[string]any{
		"cache_dir":     s.cfg.CacheDir,
		"entries":       s.e.Cache.Count(),
		"ttl_hours":     s.cfg.CacheTTL.Hours(),
		"default_limit": s.cfg.DefaultLimit,
		"all_ceiling":   s.cfg.MaxAll,
		"source":        "ip.thc.org",
	})
}

// errorResult renders a structured tool error: {code, message}. Codes:
// invalid_input, network_error, rate_limited.
func errorResult(code, message string) toolResult {
	b, _ := json.Marshal(map[string]string{"code": code, "message": message})
	return textResult(true, string(b))
}

// jsonResult marshals v into a non-error text result.
func jsonResult(v any) toolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errorResult("network_error", "encode result: "+err.Error())
	}
	return textResult(false, string(b))
}
