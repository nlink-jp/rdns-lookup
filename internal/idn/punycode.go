package idn

import (
	"errors"
	"fmt"
	"strings"
)

// RFC 3492 §5 parameter values.
const (
	base        = 36
	tmin        = 1
	tmax        = 26
	skew        = 38
	damp        = 700
	initialBias = 72
	initialN    = 128
)

// ErrUnsupported marks input the simplified IDNA cannot convert safely.
var ErrUnsupported = errors.New("cannot convert to punycode")

// ToASCII converts a (possibly IDN) domain name to its A-label form.
// Simplified IDNA: Unicode lowercasing only — UTS #46 mapping, bidi, and
// contextual rules are out of scope, and input is assumed NFC-normalized.
// Pure-ASCII names pass through unchanged (already lowercased by the
// caller's normalization or here).
func ToASCII(domain string) (string, error) {
	domain = strings.ToLower(domain)
	labels := strings.Split(domain, ".")
	for i, l := range labels {
		if isASCII(l) {
			continue
		}
		if strings.HasPrefix(l, "xn--") {
			// Non-ASCII in a label already claiming to be punycode is nonsense.
			return "", fmt.Errorf("%w: label %q mixes xn-- with non-ASCII", ErrUnsupported, l)
		}
		enc, err := encodeLabel(l)
		if err != nil {
			return "", err
		}
		labels[i] = "xn--" + enc
	}
	return strings.Join(labels, "."), nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}

// encodeLabel is the RFC 3492 §6.3 encoding algorithm for one label.
func encodeLabel(input string) (string, error) {
	runes := []rune(input)
	var out strings.Builder

	// Copy the basic (ASCII) code points, then the delimiter.
	b := 0
	for _, r := range runes {
		if r < initialN {
			out.WriteRune(r)
			b++
		}
	}
	if b > 0 {
		out.WriteByte('-')
	}

	n, delta, bias := rune(initialN), 0, initialBias
	h := b
	for h < len(runes) {
		// Find the smallest unhandled code point >= n.
		m := rune(0x7fffffff)
		for _, r := range runes {
			if r >= n && r < m {
				m = r
			}
		}
		if int64(delta)+int64(m-n)*int64(h+1) > int64(1<<31-1) {
			return "", fmt.Errorf("%w: overflow in %q", ErrUnsupported, input)
		}
		delta += int(m-n) * (h + 1)
		n = m
		for _, r := range runes {
			if r < n {
				delta++
				if delta == 1<<31-1 {
					return "", fmt.Errorf("%w: overflow in %q", ErrUnsupported, input)
				}
			}
			if r == n {
				q := delta
				for k := base; ; k += base {
					t := k - bias
					switch {
					case k <= bias:
						t = tmin
					case t > tmax:
						t = tmax
					}
					if q < t {
						break
					}
					out.WriteByte(digit(t + (q-t)%(base-t)))
					q = (q - t) / (base - t)
				}
				out.WriteByte(digit(q))
				bias = adapt(delta, h+1, h == b)
				delta = 0
				h++
			}
		}
		delta++
		n++
	}
	return out.String(), nil
}

// adapt is the RFC 3492 §6.1 bias adaptation function.
func adapt(delta, numPoints int, firstTime bool) int {
	if firstTime {
		delta /= damp
	} else {
		delta /= 2
	}
	delta += delta / numPoints
	k := 0
	for delta > ((base-tmin)*tmax)/2 {
		delta /= base - tmin
		k += base
	}
	return k + (base-tmin+1)*delta/(delta+skew)
}

// digit maps 0..35 to 'a'..'z', '0'..'9'.
func digit(d int) byte {
	if d < 26 {
		return byte('a' + d)
	}
	return byte('0' + d - 26)
}
