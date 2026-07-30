package query

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/nlink-jp/rdns-lookup/internal/idn"
)

// ErrInvalid marks input that must not be sent anywhere near the network.
var ErrInvalid = errors.New("invalid input")

// Address is a validated rdns target: a single IP, or a CIDR block whose
// prefix length falls on an octet boundary.
type Address struct {
	Original string // input as given (trimmed)
	Value    string // canonical form sent upstream
	Block    bool   // true when the target is a multi-address block
	Addr     netip.Addr
	Bits     int // prefix length; 32/128 for a single address
}

// ClassifyAddress validates an IP or CIDR block for the rdns lookup.
//
// Only octet-boundary prefixes are accepted for IPv4 (/8, /16, /24, /32).
// Upstream answers anything else with HTTP 406 "invalid ip", so this is a
// local restatement of an upstream limit rather than a policy of ours.
func ClassifyAddress(input string) (Address, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return Address{}, fmt.Errorf("%w: empty input", ErrInvalid)
	}
	if err := checkPrintable(in); err != nil {
		return Address{}, err
	}

	if !strings.Contains(in, "/") {
		addr, err := netip.ParseAddr(in)
		if err != nil {
			return Address{}, fmt.Errorf("%w: %q is not an IP address or CIDR block", ErrInvalid, in)
		}
		if addr.Zone() != "" {
			return Address{}, fmt.Errorf("%w: IPv6 zone is not allowed", ErrInvalid)
		}
		addr = addr.Unmap()
		return Address{Original: in, Value: addr.String(), Addr: addr, Bits: addr.BitLen()}, nil
	}

	pfx, err := netip.ParsePrefix(in)
	if err != nil {
		return Address{}, fmt.Errorf("%w: %q is not a valid CIDR block", ErrInvalid, in)
	}
	addr := pfx.Addr().Unmap()
	bits := pfx.Bits()
	if addr.Is4() && bits%8 != 0 {
		return Address{}, fmt.Errorf(
			"%w: upstream accepts only octet-boundary blocks (/8, /16, /24, /32); /%d is rejected with HTTP 406",
			ErrInvalid, bits)
	}
	if !addr.Is4() && bits != addr.BitLen() {
		return Address{}, fmt.Errorf("%w: IPv6 blocks are not supported upstream (use a single address)", ErrInvalid)
	}
	// A /32 (or /128) names exactly one address; canonicalize it to the bare
	// form so it shares a cache entry with the same address written plainly,
	// and so the single-address filter rules apply to it.
	if bits == addr.BitLen() {
		return Address{Original: in, Value: addr.String(), Addr: addr, Bits: bits}, nil
	}
	if pfx.Masked() != pfx {
		return Address{}, fmt.Errorf("%w: %q has host bits set (did you mean %s?)", ErrInvalid, in, pfx.Masked())
	}
	return Address{Original: in, Value: pfx.String(), Block: true, Addr: addr, Bits: bits}, nil
}

// Domain is a validated, canonicalized domain target (lowercase A-label).
type Domain struct {
	Original string
	Value    string
}

// ClassifyDomain validates DNS-name syntax for the subdomains and cnames
// lookups: total ≤253 after an optional trailing dot, labels 1–63, LDH plus
// underscore, and a non-numeric final label. IDN U-labels are converted to
// punycode first, so the charset check always sees the wire form.
func ClassifyDomain(input string) (Domain, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return Domain{}, fmt.Errorf("%w: empty input", ErrInvalid)
	}
	if err := checkPrintable(in); err != nil {
		return Domain{}, err
	}
	// An IP would be silently accepted by the label rules below only if it had
	// a non-numeric final label, which it never does — but say so explicitly,
	// because passing an IP to `subdomains` is a plausible mistake with a
	// specific fix.
	if _, err := netip.ParseAddr(in); err == nil {
		return Domain{}, fmt.Errorf("%w: %q is an IP address; use the rdns command for addresses", ErrInvalid, in)
	}

	name := strings.ToLower(strings.TrimSuffix(in, "."))
	if name == "" {
		return Domain{}, fmt.Errorf("%w: empty domain", ErrInvalid)
	}
	if !isASCII(name) {
		conv, err := idn.ToASCII(name)
		if err != nil {
			return Domain{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		name = conv
	}
	if len(name) > 253 {
		return Domain{}, fmt.Errorf("%w: domain exceeds 253 characters", ErrInvalid)
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return Domain{}, fmt.Errorf("%w: %q needs at least two labels", ErrInvalid, in)
	}
	for _, l := range labels {
		if err := checkLabel(l); err != nil {
			return Domain{}, err
		}
	}
	if allDigits(labels[len(labels)-1]) {
		return Domain{}, fmt.Errorf("%w: top-level label cannot be all-numeric", ErrInvalid)
	}
	return Domain{Original: in, Value: name}, nil
}

// checkPrintable rejects control characters and embedded whitespace. This is
// the gate against smuggling anything into the upstream HTTP request or into a
// cache key, applied before classification so it holds for every target kind.
func checkPrintable(in string) error {
	for _, r := range in {
		if r < 0x21 || r == 0x7f {
			return fmt.Errorf("%w: control or whitespace character in input", ErrInvalid)
		}
	}
	return nil
}

func checkLabel(l string) error {
	if l == "" {
		return fmt.Errorf("%w: empty label", ErrInvalid)
	}
	if len(l) > 63 {
		return fmt.Errorf("%w: label exceeds 63 characters", ErrInvalid)
	}
	if l[0] == '-' || l[len(l)-1] == '-' {
		return fmt.Errorf("%w: label cannot start or end with a hyphen", ErrInvalid)
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		// LDH plus underscore. Underscore is a valid DNS label octet and is
		// the canonical form of many indexed names (_dmarc, _domainkey,
		// _acme-challenge, the _service._proto labels of SRV/TLSA), so it may
		// appear in any position including the first.
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return fmt.Errorf("%w: label contains %q", ErrInvalid, rune(c))
	}
	return nil
}

// NormalizeTLDs canonicalizes a TLD filter list: lowercase, leading dots
// stripped, empties dropped, duplicates removed.
func NormalizeTLDs(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		t = strings.TrimPrefix(t, ".")
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
