package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPutGetRoundTrip(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	now := time.Unix(1_800_000_000, 0)
	want := json.RawMessage(`{"count":2}`)

	if err := s.Put("k.json", want, now); err != nil {
		t.Fatalf("Put returned error %v", err)
	}
	got, ok := s.Get("k.json", now, time.Hour)
	if !ok {
		t.Fatal("Get missed an entry just written")
	}
	if string(got) != string(want) {
		t.Errorf("Get = %s, want %s", got, want)
	}
}

func TestGetMissWhenAbsent(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if _, ok := s.Get("nope.json", time.Unix(0, 0), time.Hour); ok {
		t.Error("Get should miss on an absent entry")
	}
}

// The TTL is applied at read time, so changing it in config affects entries
// already on disk.
func TestGetRespectsTTLAtReadTime(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	now := time.Unix(1_800_000_000, 0)
	if err := s.Put("k.json", json.RawMessage(`1`), now); err != nil {
		t.Fatalf("Put returned error %v", err)
	}
	later := now.Add(2 * time.Hour)
	if _, ok := s.Get("k.json", later, time.Hour); ok {
		t.Error("an entry older than the TTL must not be served")
	}
	if _, ok := s.Get("k.json", later, 3*time.Hour); !ok {
		t.Error("the same entry must be served under a longer TTL")
	}
}

// A corrupt entry reads as a miss so a bad file can never break a lookup.
func TestCorruptEntryReadsAsMiss(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := s.Get("bad.json", time.Unix(0, 0), time.Hour); ok {
		t.Error("a corrupt entry must read as a miss")
	}
	// And it must be overwritable.
	if err := s.Put("bad.json", json.RawMessage(`1`), time.Unix(0, 0)); err != nil {
		t.Fatalf("Put over a corrupt entry returned error %v", err)
	}
	if _, ok := s.Get("bad.json", time.Unix(0, 0), time.Hour); !ok {
		t.Error("Put should have replaced the corrupt entry")
	}
}

func TestCountAndClear(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	now := time.Unix(0, 0)
	for _, k := range []string{"a.json", "b.json", "c.json"} {
		if err := s.Put(k, json.RawMessage(`1`), now); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if n := s.Count(); n != 3 {
		t.Errorf("Count = %d, want 3", n)
	}
	n, err := s.Clear()
	if err != nil {
		t.Fatalf("Clear returned error %v", err)
	}
	if n != 3 {
		t.Errorf("Clear removed %d, want 3", n)
	}
	if got := s.Count(); got != 0 {
		t.Errorf("Count after Clear = %d, want 0", got)
	}
}

func TestCountAndClearOnMissingDir(t *testing.T) {
	s := &Store{Dir: filepath.Join(t.TempDir(), "absent")}
	if n := s.Count(); n != 0 {
		t.Errorf("Count = %d, want 0", n)
	}
	n, err := s.Clear()
	if err != nil {
		t.Errorf("Clear on a missing dir returned error %v", err)
	}
	if n != 0 {
		t.Errorf("Clear removed %d, want 0", n)
	}
}

// Non-JSON files and subdirectories are left alone, so anything else stored
// alongside the cache survives a clear.
func TestClearLeavesOtherFiles(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if err := s.Put("a.json", json.RawMessage(`1`), time.Unix(0, 0)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Error("Clear removed a non-cache file")
	}
	if _, err := os.Stat(filepath.Join(dir, "sub")); err != nil {
		t.Error("Clear removed a subdirectory")
	}
}

func TestKeyReadableWhenSafe(t *testing.T) {
	got := Key("rdns", "1.1.1.1", "100", "", "")
	if got != "rdns_1.1.1.1_100__.json" {
		t.Errorf("Key = %q, want a readable name", got)
	}
}

// Different options must never collide, and awkward characters must not escape
// into a path.
func TestKeyDistinguishesAndSanitizes(t *testing.T) {
	a := Key("rdns", "1.1.1.0/24", "100", "", "")
	b := Key("rdns", "1.1.1.0/25", "100", "", "")
	if a == b {
		t.Error("distinct targets produced the same key")
	}
	for _, k := range []string{a, b} {
		if strings.ContainsAny(k, "/\\:") {
			t.Errorf("key %q contains a path character", k)
		}
	}
}

func TestKeyHashesLongInputs(t *testing.T) {
	long := strings.Repeat("sub.", 60) + "example.com"
	k := Key("subdomains", long, "100", "", "")
	if len(k) > 80 {
		t.Errorf("key length %d should be bounded, got %q", len(k), k)
	}
	if !strings.HasPrefix(k, "subdomains_") {
		t.Errorf("key %q should keep a readable prefix", k)
	}
	// A different long input must still differ.
	if k == Key("subdomains", long+"x", "100", "", "") {
		t.Error("hashed keys collided")
	}
}

// A crash mid-write must never leave a truncated entry, and temp files must not
// be counted as cache entries.
func TestPutIsAtomic(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if err := s.Put("k.json", json.RawMessage(`{"a":1}`), time.Unix(0, 0)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("a temp file survived: %s", e.Name())
		}
	}
	if s.Count() != 1 {
		t.Errorf("Count = %d, want 1", s.Count())
	}
}

func TestPutCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "cache")
	s := &Store{Dir: dir}
	if err := s.Put("k.json", json.RawMessage(`1`), time.Unix(0, 0)); err != nil {
		t.Fatalf("Put should create the directory, got %v", err)
	}
	if _, ok := s.Get("k.json", time.Unix(0, 0), time.Hour); !ok {
		t.Error("entry not readable after Put created the dir")
	}
}
