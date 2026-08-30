package mcp

import (
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
		"tools": []map[string]any{
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
		},
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
		return textResult(false, usageMarkdown), nil
	case "lookup_rdns":
		return s.toolLookup(thc.KindRDNS, p.Arguments), nil
	case "lookup_subdomains":
		return s.toolLookup(thc.KindSubdomains, p.Arguments), nil
	case "lookup_cnames":
		return s.toolLookup(thc.KindCNAMEs, p.Arguments), nil
	case "cache_status":
		return s.toolCacheStatus(), nil
	default:
		return toolResult{}, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
	}
}

// toolArgs is the union of the three lookup tools' arguments; each tool reads
// only the target field that belongs to it.
type toolArgs struct {
	IPAddress    string   `json:"ip_address"`
	Domain       string   `json:"domain"`
	TargetDomain string   `json:"target_domain"`
	Limit        int      `json:"limit"`
	All          bool     `json:"all"`
	TLD          []string `json:"tld"`
	ApexDomain   string   `json:"apex_domain"`
	Refresh      bool     `json:"refresh"`
}

func (s *server) toolLookup(kind thc.Kind, raw json.RawMessage) toolResult {
	var a toolArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return errorResult("invalid_input", "arguments: "+err.Error())
		}
	}

	target, field := a.IPAddress, "ip_address"
	switch kind {
	case thc.KindSubdomains:
		target, field = a.Domain, "domain"
	case thc.KindCNAMEs:
		target, field = a.TargetDomain, "target_domain"
	}
	if strings.TrimSpace(target) == "" {
		return errorResult("invalid_input", "provide '"+field+"'")
	}

	opts := engine.Options{
		Limit:      a.Limit,
		All:        a.All,
		TLDs:       a.TLD,
		ApexDomain: a.ApexDomain,
		Refresh:    a.Refresh,
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
