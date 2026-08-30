// Package mcp is a zero-dependency stdio MCP server (JSON-RPC 2.0) exposing
// rdns-lookup's engine as the tools lookup_rdns, lookup_subdomains,
// lookup_cnames, cache_status, and get_usage. It shares the engine with the
// CLI so their behaviour cannot diverge. Diagnostics must go to stderr only;
// stdout carries the protocol.
//
// Every record retrieved is returned inline: the server writes no files, owns
// no output directory and takes no path argument, so it works unchanged against
// a client that has no filesystem of its own. One lookup can return tens of
// thousands of records, so `limit` (and `all`) is what bounds a response —
// deciding what is too big for a model belongs to the client, which is the only
// side that knows the context window.
package mcp
