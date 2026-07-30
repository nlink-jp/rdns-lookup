// Package idn converts IDN U-labels (e.g. Japanese domains) to punycode
// A-labels via an in-house RFC 3492 encoder — no x/net/idna, keeping the
// zero-dependency policy. Simplified IDNA: lowercasing only; UTS #46 mapping,
// bidi, and contextual rules are out of scope, and input is assumed
// NFC-normalized. Cache keys and upstream API requests always use the A-label
// form. Ported from the doh-lookup sibling.
package idn
