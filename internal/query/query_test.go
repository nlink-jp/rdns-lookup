package query

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyAddressSingle(t *testing.T) {
	tests := []struct {
		in    string
		value string
	}{
		{"1.1.1.1", "1.1.1.1"},
		{" 142.251.43.46 ", "142.251.43.46"},
		{"142.251.43.46/32", "142.251.43.46"}, // /32 canonicalizes to the bare form
		{"::ffff:1.2.3.4", "1.2.3.4"},         // IPv4-mapped IPv6 unmaps
		{"2404:6800:4004:80e::200e", "2404:6800:4004:80e::200e"},
	}
	for _, tt := range tests {
		got, err := ClassifyAddress(tt.in)
		if err != nil {
			t.Errorf("ClassifyAddress(%q) returned error %v", tt.in, err)
			continue
		}
		if got.Value != tt.value {
			t.Errorf("ClassifyAddress(%q).Value = %q, want %q", tt.in, got.Value, tt.value)
		}
		if got.Block {
			t.Errorf("ClassifyAddress(%q).Block = true, want false", tt.in)
		}
	}
}

func TestClassifyAddressBlock(t *testing.T) {
	tests := []struct {
		in    string
		value string
	}{
		{"142.251.43.0/24", "142.251.43.0/24"},
		{"142.251.0.0/16", "142.251.0.0/16"},
		{"10.0.0.0/8", "10.0.0.0/8"},
	}
	for _, tt := range tests {
		got, err := ClassifyAddress(tt.in)
		if err != nil {
			t.Errorf("ClassifyAddress(%q) returned error %v", tt.in, err)
			continue
		}
		if got.Value != tt.value || !got.Block {
			t.Errorf("ClassifyAddress(%q) = {%q, block=%v}, want {%q, block=true}",
				tt.in, got.Value, got.Block, tt.value)
		}
	}
}

// Upstream answers a non-octet-boundary prefix with HTTP 406, so we refuse it
// locally and must say why.
func TestClassifyAddressRejectsNonOctetBoundary(t *testing.T) {
	for _, in := range []string{"142.251.43.0/25", "142.251.43.0/29", "10.0.0.0/7", "1.2.3.4/31"} {
		_, err := ClassifyAddress(in)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("ClassifyAddress(%q) error = %v, want ErrInvalid", in, err)
			continue
		}
		if !strings.Contains(err.Error(), "octet-boundary") {
			t.Errorf("ClassifyAddress(%q) error %q should explain the octet-boundary limit", in, err)
		}
	}
}

func TestClassifyAddressRejectsHostBits(t *testing.T) {
	_, err := ClassifyAddress("142.251.43.46/24")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "142.251.43.0/24") {
		t.Errorf("error %q should suggest the masked form", err)
	}
}

func TestClassifyAddressInvalid(t *testing.T) {
	for _, in := range []string{"", "   ", "not-an-ip", "example.com", "1.1.1.1 extra", "1.1.1.1\nHost: x", "999.1.1.1"} {
		if _, err := ClassifyAddress(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("ClassifyAddress(%q) error = %v, want ErrInvalid", in, err)
		}
	}
}

func TestClassifyAddressRejectsIPv6Block(t *testing.T) {
	if _, err := ClassifyAddress("2404:6800::/32"); !errors.Is(err, ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

func TestClassifyDomain(t *testing.T) {
	tests := []struct{ in, want string }{
		{"github.com", "github.com"},
		{"GitHub.COM", "github.com"},
		{"github.com.", "github.com"},
		{" example.co.uk ", "example.co.uk"},
		{"_dmarc.example.com", "_dmarc.example.com"},
		{"selector._domainkey.example.com", "selector._domainkey.example.com"},
		{"日本.jp", "xn--wgv71a.jp"},
	}
	for _, tt := range tests {
		got, err := ClassifyDomain(tt.in)
		if err != nil {
			t.Errorf("ClassifyDomain(%q) returned error %v", tt.in, err)
			continue
		}
		if got.Value != tt.want {
			t.Errorf("ClassifyDomain(%q).Value = %q, want %q", tt.in, got.Value, tt.want)
		}
	}
}

// Passing an address to a domain command is a plausible mistake, so the error
// must name the command that does accept addresses.
func TestClassifyDomainRejectsIPWithHint(t *testing.T) {
	_, err := ClassifyDomain("1.1.1.1")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "rdns") {
		t.Errorf("error %q should point at the rdns command", err)
	}
}

func TestClassifyDomainInvalid(t *testing.T) {
	long := strings.Repeat("a", 64) + ".com"
	tests := []string{
		"", "   ", "localhost", "-bad.com", "bad-.com", "exa mple.com",
		"example.com\r\nHost: evil", "under_score.example..com", long,
		strings.Repeat("a.", 130) + "com",
		"example.123",
	}
	for _, in := range tests {
		if _, err := ClassifyDomain(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("ClassifyDomain(%q) error = %v, want ErrInvalid", in, err)
		}
	}
}

func TestNormalizeTLDs(t *testing.T) {
	got := NormalizeTLDs([]string{" COM ", ".net", "com", "", "  ", "ORG"})
	want := []string{"com", "net", "org"}
	if len(got) != len(want) {
		t.Fatalf("NormalizeTLDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NormalizeTLDs = %v, want %v", got, want)
		}
	}
}

func TestNormalizeTLDsEmpty(t *testing.T) {
	if got := NormalizeTLDs(nil); len(got) != 0 {
		t.Errorf("NormalizeTLDs(nil) = %v, want empty", got)
	}
}
