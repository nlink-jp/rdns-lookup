// Package mcp is a zero-dependency stdio MCP server (JSON-RPC 2.0) exposing
// rdns-lookup's engine as the tools lookup_rdns, lookup_subdomains,
// lookup_cnames, cache_status, and get_usage. It shares the engine with the
// CLI so their behaviour cannot diverge. Diagnostics must go to stderr only;
// stdout carries the protocol.
//
// Because one lookup can return tens of thousands of records, results above a
// configured threshold are written to a file under workspace_root and only the
// path plus a count summary is returned — an agent's context is a scarcer
// resource than disk.
package mcp
