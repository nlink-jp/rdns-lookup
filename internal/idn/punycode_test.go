package idn

import (
	"errors"
	"testing"
)

func TestToASCII(t *testing.T) {
	tests := []struct{ in, want string }{
		{"example.com", "example.com"},
		{"EXAMPLE.COM", "example.com"},
		{"日本.jp", "xn--wgv71a.jp"},
		{"例え.テスト", "xn--r8jz45g.xn--zckzah"},
		{"xn--wgv71a.jp", "xn--wgv71a.jp"}, // already an A-label
		{"münchen.de", "xn--mnchen-3ya.de"},
	}
	for _, tt := range tests {
		got, err := ToASCII(tt.in)
		if err != nil {
			t.Errorf("ToASCII(%q) returned error %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ToASCII(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A label already claiming to be punycode while holding non-ASCII is nonsense.
func TestToASCIIRejectsMixedLabel(t *testing.T) {
	if _, err := ToASCII("xn--日本.jp"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("error = %v, want ErrUnsupported", err)
	}
}

func TestToASCIIIsIdempotent(t *testing.T) {
	once, err := ToASCII("日本.jp")
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	twice, err := ToASCII(once)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if once != twice {
		t.Errorf("ToASCII is not idempotent: %q then %q", once, twice)
	}
}
