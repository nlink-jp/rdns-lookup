// Command rdns-lookup queries the free DNS index published by THC
// (ip.thc.org) for the relationships around an IP or a domain — the domains
// associated with an IP or IP block, the subdomains of a domain, and the
// domains that CNAME to a domain — as a CLI and a local MCP server. Unlike
// dig -x or the doh-lookup sibling, which return the single PTR name the
// address owner published, this reads a third-party aggregate index and so
// sends no packet to the target under investigation: the safest opening move
// in a triage. The relationship-breadth, credential-zero sibling of
// asn-lookup (attribution), whois-lookup (registration), abuse-lookup
// (reputation), and doh-lookup (current resolution).
package main

import (
	"os"

	"github.com/nlink-jp/rdns-lookup/internal/app"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(app.Run(os.Args[1:], version))
}
