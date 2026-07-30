// Package cache is a fixed-TTL result cache rooted at a directory, one JSON
// file per entry. Each entry records when it was fetched rather than when it
// expires, so the TTL can be changed in config and take effect on entries
// already on disk — the right trade for an upstream whose data has no
// per-record freshness of its own (unlike DNS TTLs, which doh-lookup honors
// per entry). Freshness lives in the record, not the file mtime, so it
// survives copies. The clock is supplied by the caller — the engine is the
// only clock reader — keeping the store deterministic and testable. Writes are
// atomic (temp file + rename).
//
// Caching here is also a courtesy: upstream is free, unauthenticated, and asks
// not to be abused, so a repeated lookup must not become a repeated request.
package cache
