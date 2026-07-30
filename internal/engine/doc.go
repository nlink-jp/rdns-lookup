// Package engine is the shared core behind both the CLI and the MCP server, so
// their behaviour cannot diverge. One lookup runs: validate the target, choose
// an upstream face, read the cache, fetch and paginate, normalize, then write
// the cache back.
//
// Three of its jobs exist because of upstream quirks rather than by choice.
// Route selection hides the asymmetry between a JSON face capped at 100 rows
// and a CSV face capped at 50,000 with no pagination. Deduplication removes
// rows upstream returns twice (the same domain with an empty and a populated
// tld). And truncation is reported explicitly — the upstream total is carried
// into every result next to the count actually retrieved — because stopping at
// a ceiling silently would read as "this is everything".
//
// The engine is the only clock reader (Engine.Now), which keeps caching and
// rate-limit pacing deterministic in tests.
package engine
