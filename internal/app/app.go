// Package app implements the rdns-lookup command-line interface: subcommand
// dispatch plus the rdns / subdomains / cnames / cache / mcp commands. Core
// logic lives in the query, thc, cache, config, and engine packages; this
// package is the thin I/O shell around them.
package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/nlink-jp/rdns-lookup/internal/config"
	"github.com/nlink-jp/rdns-lookup/internal/engine"
	"github.com/nlink-jp/rdns-lookup/internal/mcp"
)

// Exit codes. A lookup is not a membership test: finding nothing indexed is a
// successful answer, distinct from an operational failure.
const (
	exitOK       = 0 // at least one target returned records
	exitNoRecord = 1 // every target returned nothing indexed
	exitError    = 2 // usage / validation / network error
)

// Run dispatches a subcommand and returns a process exit code.
func Run(args []string, version string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitError
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "rdns", "subdomains", "cnames":
		return runLookup(cmd, rest, version, os.Stdout, os.Stderr)
	case "cache":
		return runCache(rest, os.Stdout, os.Stderr)
	case "mcp":
		return cmdMCP(rest, version)
	case "version", "--version", "-v":
		fmt.Println("rdns-lookup " + version)
		fmt.Println("Data source: ip.thc.org (free, unauthenticated DNS index) — no credentials.")
		return exitOK
	case "help", "-h", "--help":
		usage(os.Stdout)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage(os.Stderr)
		return exitError
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `rdns-lookup — look up an IP's domains, a domain's subdomains, and reverse CNAMEs

Usage:
  rdns-lookup <command> [flags] [target...]

Commands:
  rdns <ip|cidr ...>       Domains associated with an IP or IP block
  subdomains <domain ...>  Subdomains of a domain
  cnames <domain ...>      Domains that CNAME to a domain
  cache status             Show the result-cache state
  cache clear              Clear the result cache
  mcp                      Run as a local MCP server (stdio)
  version                  Print the version

Lookup flags:
  --limit <n>              Records to retrieve (default 100)
  --all                    Retrieve up to the ceiling (50000)
  --tld <list>             Comma-separated TLD filter (rdns, single address only)
  --apex <domain>          Apex-domain filter (rdns, single address only)
  -j, --json               JSON output (JSONL when multiple targets)
  --raw                    Include the raw upstream response bodies
  --refresh                Bypass the result cache and re-query
  --timeout <dur>          Network timeout (e.g. 10s; default 30s)
  --input <file>           Read newline-separated targets from a file
  -c, --config <path>      Config file (default ~/.config/rdns-lookup/config.toml)

Bulk input: pass multiple targets, --input <file>, or pipe them on stdin.
Bulk runs pace themselves against the upstream rate-limit budget.

Lookup exit codes:
  0  at least one target returned records
  1  every target returned nothing indexed
  2  error (invalid input, network failure, ...)

rdns accepts a single address or an octet-boundary block (/8, /16, /24, /32);
upstream rejects other prefix lengths, so they are refused before any request.
Note that upstream's "reverse DNS" is not PTR: it is an aggregate index of PTR
names, domains whose A records point at the address, and names seen in
Certificate Transparency logs — so it answers "what else is here", which
doh-lookup's PTR lookup cannot. Because only a third-party index is read, no
packet reaches the target under investigation.

Results state how many records upstream holds against how many were retrieved,
so a partial answer is never mistaken for a complete one. All data comes from
ip.thc.org, a free service; no credentials are required.
`)
}

// notifyContext is signal.NotifyContext, and mcpStdin and mcpStdout the
// process's streams; tests stand in for them.
var (
	notifyContext           = signal.NotifyContext
	mcpStdin      io.Reader = os.Stdin
	mcpStdout     io.Writer = os.Stdout
)

// cmdMCP runs the stdio MCP server until stdin closes, answering every
// request that arrived, or until SIGINT or SIGTERM.
func cmdMCP(args []string, version string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfgPath := fs.String("config", "", "config file path")
	fs.StringVar(cfgPath, "c", "", "config file path (shorthand)")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	cfg, err := config.Load(*cfgPath, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitError
	}
	// SIGINT and SIGTERM cancel the server's ctx: it returns at once and its
	// cleanup runs (server-migration §2).
	ctx, stop := notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcp.Serve(ctx, engine.New(cfg, version), cfg, version, mcpStdin, mcpStdout); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		return exitError
	}
	return exitOK
}
