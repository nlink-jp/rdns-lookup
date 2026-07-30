package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// record is the on-disk envelope. Storing the fetch time rather than an expiry
// lets a config TTL change apply to entries already written.
type record struct {
	FetchedAtUnix int64           `json:"fetched_at_unix"`
	Result        json.RawMessage `json:"result"`
}

// Store is a result cache rooted at a directory.
type Store struct {
	Dir string
}

// Key builds a safe cache filename from canonical parts (kind, target, and
// every option that changes the answer). Parts that are safe as a filename are
// kept readable so a human can inspect the cache directory; anything longer or
// containing awkward characters is hashed, which also keeps filenames inside
// filesystem limits for long domains and filter lists.
func Key(parts ...string) string {
	joined := strings.ToLower(strings.Join(parts, "_"))
	if safeName(joined) && len(joined) <= 120 {
		return joined + ".json"
	}
	sum := sha256.Sum256([]byte(joined))
	// Keep the first part readable for eyeballing, hash the rest.
	prefix := ""
	if len(parts) > 0 && safeName(strings.ToLower(parts[0])) && len(parts[0]) <= 16 {
		prefix = strings.ToLower(parts[0]) + "_"
	}
	return prefix + hex.EncodeToString(sum[:16]) + ".json"
}

func safeName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// Get returns the cached raw result for key when it was fetched within ttl of
// now.
func (s *Store) Get(key string, now time.Time, ttl time.Duration) (json.RawMessage, bool) {
	b, err := os.ReadFile(filepath.Join(s.Dir, key))
	if err != nil {
		return nil, false
	}
	var rec record
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, false // corrupt entries read as misses; Put overwrites them
	}
	if now.Sub(time.Unix(rec.FetchedAtUnix, 0)) > ttl {
		return nil, false
	}
	return rec.Result, true
}

// Put stores a raw result under key, stamped with the fetch time. The write is
// atomic (temp file + rename) so a crash never leaves a truncated entry.
func (s *Store) Put(key string, result json.RawMessage, now time.Time) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(record{FetchedAtUnix: now.Unix(), Result: result})
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.Dir, key), b)
}

// Count returns the number of cached entries.
func (s *Store) Count() int {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

// Clear removes every cached entry, returning the number removed.
func (s *Store) Clear() (int, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if err := os.Remove(filepath.Join(s.Dir, e.Name())); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
