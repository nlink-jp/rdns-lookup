package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/rdns-lookup/internal/thc"
)

// isolate points config at throwaway directories so tests never read or write
// the developer's real state.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	isolate(t)
	cfg, err := Load("", 0)
	if err != nil {
		t.Fatalf("Load returned error %v", err)
	}
	if cfg.DefaultLimit != DefaultLimit || cfg.MaxAll != DefaultMaxAll {
		t.Errorf("limits = %d/%d, want %d/%d", cfg.DefaultLimit, cfg.MaxAll, DefaultLimit, DefaultMaxAll)
	}
	if !cfg.Dedup {
		t.Error("Dedup should default on: upstream duplicates are noise, not data")
	}
	if cfg.CacheTTL != DefaultCacheTTL {
		t.Errorf("CacheTTL = %s, want %s", cfg.CacheTTL, DefaultCacheTTL)
	}
	if cfg.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %s, want %s", cfg.Timeout, DefaultTimeout)
	}
	if cfg.MinRemaining != DefaultMinRemaining || cfg.MCPInlineMax != DefaultMCPInlineMax {
		t.Errorf("MinRemaining/MCPInlineMax = %d/%d", cfg.MinRemaining, cfg.MCPInlineMax)
	}
	if cfg.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty (built-in)", cfg.BaseURL)
	}
}

// A missing config file is normal, not an error.
func TestLoadMissingFileIsFine(t *testing.T) {
	isolate(t)
	if _, err := Load(filepath.Join(t.TempDir(), "absent.toml"), 0); err != nil {
		t.Errorf("a missing config must not fail: %v", err)
	}
}

func TestLoadFromFile(t *testing.T) {
	isolate(t)
	path := writeConfig(t, `
# comment
[api]
base_url = "https://example.test/api/v1"

[query]
default_limit = 25
max_all = 1000
dedup = false

[cache]
ttl_hours = 6
dir = "/tmp/rdns-cache-test"

[network]
timeout_seconds = 12

[ratelimit]
min_remaining = 50

[mcp]
inline_max_records = 10
workspace = "/tmp/rdns-ws-test"
`)
	cfg, err := Load(path, 0)
	if err != nil {
		t.Fatalf("Load returned error %v", err)
	}
	if cfg.BaseURL != "https://example.test/api/v1" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.DefaultLimit != 25 || cfg.MaxAll != 1000 || cfg.Dedup {
		t.Errorf("query = %d/%d/%v", cfg.DefaultLimit, cfg.MaxAll, cfg.Dedup)
	}
	if cfg.CacheTTL != 6*time.Hour || cfg.CacheDir != "/tmp/rdns-cache-test" {
		t.Errorf("cache = %s/%s", cfg.CacheTTL, cfg.CacheDir)
	}
	if cfg.Timeout != 12*time.Second {
		t.Errorf("Timeout = %s", cfg.Timeout)
	}
	if cfg.MinRemaining != 50 {
		t.Errorf("MinRemaining = %d", cfg.MinRemaining)
	}
	if cfg.MCPInlineMax != 10 || cfg.WorkspaceDir != "/tmp/rdns-ws-test" {
		t.Errorf("mcp = %d/%s", cfg.MCPInlineMax, cfg.WorkspaceDir)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	isolate(t)
	path := writeConfig(t, "[query]\ndefault_limit = 25\n[cache]\nttl_hours = 6\n")
	t.Setenv("RDNS_LOOKUP_DEFAULT_LIMIT", "7")
	t.Setenv("RDNS_LOOKUP_CACHE_TTL_HOURS", "48")
	t.Setenv("RDNS_LOOKUP_DEDUP", "off")
	t.Setenv("RDNS_LOOKUP_BASE_URL", "https://env.test/api")
	cfg, err := Load(path, 0)
	if err != nil {
		t.Fatalf("Load returned error %v", err)
	}
	if cfg.DefaultLimit != 7 {
		t.Errorf("DefaultLimit = %d, want the env value 7", cfg.DefaultLimit)
	}
	if cfg.CacheTTL != 48*time.Hour {
		t.Errorf("CacheTTL = %s, want the env value 48h", cfg.CacheTTL)
	}
	if cfg.Dedup {
		t.Error("Dedup should follow the env value")
	}
	if cfg.BaseURL != "https://env.test/api" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
}

// The timeout flag is the only flag threaded into config, and it must win.
func TestTimeoutOverrideBeatsEverything(t *testing.T) {
	isolate(t)
	path := writeConfig(t, "[network]\ntimeout_seconds = 12\n")
	t.Setenv("RDNS_LOOKUP_TIMEOUT_SECONDS", "20")
	cfg, err := Load(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Load returned error %v", err)
	}
	if cfg.Timeout != 5*time.Second {
		t.Errorf("Timeout = %s, want the flag value 5s", cfg.Timeout)
	}
}

// max_all above the upstream CSV limit cannot be honored, so it must be
// rejected rather than silently clamped.
func TestValidateRejectsMaxAllAboveUpstreamLimit(t *testing.T) {
	isolate(t)
	path := writeConfig(t, "[query]\nmax_all = 60000\n")
	_, err := Load(path, 0)
	if err == nil {
		t.Fatal("max_all above the upstream limit must be rejected")
	}
	if !strings.Contains(err.Error(), "50000") {
		t.Errorf("error %q should name the upstream limit", err)
	}
}

func TestValidateRejectsNonPositive(t *testing.T) {
	isolate(t)
	tests := []string{
		"[query]\ndefault_limit = 0\n",
		"[query]\nmax_all = 0\n",
		"[mcp]\ninline_max_records = 0\n",
	}
	for _, body := range tests {
		if _, err := Load(writeConfig(t, body), 0); err == nil {
			t.Errorf("config %q should be rejected", body)
		}
	}
}

func TestBadValuesRejected(t *testing.T) {
	isolate(t)
	tests := []string{
		"[query]\ndedup = maybe\n",
		"[query]\ndefault_limit = lots\n",
		"[cache]\nttl_hours = -1\n",
		"[network]\ntimeout_seconds = nope\n",
		"[ratelimit]\nmin_remaining = x\n",
		"[mcp]\ninline_max_records = x\n",
	}
	for _, body := range tests {
		if _, err := Load(writeConfig(t, body), 0); err == nil {
			t.Errorf("config %q should be rejected", body)
		}
	}
}

func TestBadEnvRejected(t *testing.T) {
	isolate(t)
	tests := map[string]string{
		"RDNS_LOOKUP_DEFAULT_LIMIT":   "lots",
		"RDNS_LOOKUP_MAX_ALL":         "lots",
		"RDNS_LOOKUP_DEDUP":           "maybe",
		"RDNS_LOOKUP_CACHE_TTL_HOURS": "-3",
		"RDNS_LOOKUP_TIMEOUT_SECONDS": "soon",
		"RDNS_LOOKUP_MIN_REMAINING":   "few",
		"RDNS_LOOKUP_MCP_INLINE_MAX":  "few",
	}
	for k, v := range tests {
		t.Run(k, func(t *testing.T) {
			isolate(t)
			t.Setenv(k, v)
			if _, err := Load("", 0); err == nil {
				t.Errorf("%s=%q should be rejected", k, v)
			}
		})
	}
}

func TestMalformedTOMLRejected(t *testing.T) {
	isolate(t)
	for _, body := range []string{"[query\ndefault_limit = 1\n", "default_limit\n", "= 5\n"} {
		if _, err := Load(writeConfig(t, body), 0); err == nil {
			t.Errorf("malformed TOML %q should be rejected", body)
		}
	}
}

func TestInlineCommentsAndQuotes(t *testing.T) {
	isolate(t)
	cfg, err := Load(writeConfig(t, "[query]\ndefault_limit = 30 # thirty\n[api]\nbase_url = \"https://q.test\" # quoted\n"), 0)
	if err != nil {
		t.Fatalf("Load returned error %v", err)
	}
	if cfg.DefaultLimit != 30 {
		t.Errorf("DefaultLimit = %d, want 30", cfg.DefaultLimit)
	}
	if cfg.BaseURL != "https://q.test" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
}

func TestBoolSpellings(t *testing.T) {
	isolate(t)
	for _, v := range []string{"1", "true", "yes", "on"} {
		cfg, err := Load(writeConfig(t, "[query]\ndedup = "+v+"\n"), 0)
		if err != nil || !cfg.Dedup {
			t.Errorf("dedup = %q should parse as true (err %v)", v, err)
		}
	}
	for _, v := range []string{"0", "false", "no", "off"} {
		cfg, err := Load(writeConfig(t, "[query]\ndedup = "+v+"\n"), 0)
		if err != nil || cfg.Dedup {
			t.Errorf("dedup = %q should parse as false (err %v)", v, err)
		}
	}
}

// Cached answers are re-fetchable transient state, so they belong under the
// cache home, not the data home.
func TestDefaultPathsHonorXDG(t *testing.T) {
	cfgHome, cacheHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	if got, want := DefaultConfigPath(), filepath.Join(cfgHome, "rdns-lookup", "config.toml"); got != want {
		t.Errorf("DefaultConfigPath = %q, want %q", got, want)
	}
	if got, want := DefaultCacheDir(), filepath.Join(cacheHome, "rdns-lookup"); got != want {
		t.Errorf("DefaultCacheDir = %q, want %q", got, want)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got, want := expandHome("~/x"), filepath.Join(home, "x"); got != want {
		t.Errorf("expandHome(~/x) = %q, want %q", got, want)
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("expandHome should leave absolute paths alone, got %q", got)
	}
}

// The default ceiling must track the upstream constant rather than drift.
func TestDefaultMaxAllMatchesUpstreamLimit(t *testing.T) {
	if DefaultMaxAll != thc.CSVMaxLimit {
		t.Errorf("DefaultMaxAll = %d, want the upstream CSV limit %d", DefaultMaxAll, thc.CSVMaxLimit)
	}
}
