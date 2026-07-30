// Package query validates and canonicalizes lookup targets before any
// network I/O. Two shapes exist: an address target (a single IP, or a CIDR
// block on an octet boundary) for the rdns lookup, and a domain target for
// the subdomains and cnames lookups. The octet-boundary restriction is not
// ours — upstream rejects /25 and /29 with HTTP 406, so refusing them locally
// turns a wasted round trip into an immediate, explanatory error. Rejecting
// control characters and whitespace here is also the gate against smuggling
// anything into the upstream request or into a cache key.
package query
