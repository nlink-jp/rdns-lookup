package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nlink-jp/rdns-lookup/internal/thc"
)

const (
	// DefaultLimit is how many records a lookup returns when neither --limit
	// nor --all is given. It matches the JSON face's real ceiling, so the
	// default costs exactly one upstream request.
	DefaultLimit = 100
	// DefaultMaxAll caps --all. The CSV face cannot paginate, and walking
	// further through the JSON face would cost hundreds of requests under a
	// 0.5 req/sec budget, so this is the practical ceiling on exhaustive
	// retrieval rather than an arbitrary policy.
	DefaultMaxAll = thc.CSVMaxLimit
	// DefaultTimeout bounds each HTTPS exchange. A 50,000-row CSV takes
	// noticeably longer than a JSON page, so this is generous.
	DefaultTimeout = 30 * time.Second
	// DefaultCacheTTL is how long a cached answer stays fresh. The upstream
	// index is historical and changes slowly, so a day is ample and keeps
	// repeated triage of the same indicator down to one request.
	DefaultCacheTTL = 24 * time.Hour
	// DefaultMinRemaining is the rate-limit floor at which a bulk run pauses
	// for replenishment instead of pressing on.
	DefaultMinRemaining = 20
)

// Config holds resolved runtime settings. No credentials: the upstream API is
// unauthenticated.
type Config struct {
	BaseURL      string        // API root override ("" = built-in)
	DefaultLimit int           // records returned when neither --limit nor --all is given
	MaxAll       int           // ceiling for --all
	Dedup        bool          // drop upstream duplicate rows
	CacheDir     string        // result-cache directory
	CacheTTL     time.Duration // how long a cached answer stays fresh
	Timeout      time.Duration // network timeout per exchange
	MinRemaining int           // pause for replenishment below this rate-limit budget
}

// Load resolves configuration. If configPath is empty the default location
// (~/.config/rdns-lookup/config.toml) is used when present. Environment
// variables override file values; a non-zero timeoutOverride wins over both.
func Load(configPath string, timeoutOverride time.Duration) (*Config, error) {
	cfg := &Config{
		DefaultLimit: DefaultLimit,
		MaxAll:       DefaultMaxAll,
		Dedup:        true,
		CacheDir:     DefaultCacheDir(),
		CacheTTL:     DefaultCacheTTL,
		Timeout:      DefaultTimeout,
		MinRemaining: DefaultMinRemaining,
	}

	if configPath == "" {
		configPath = DefaultConfigPath()
	}
	if configPath != "" {
		if f, err := os.Open(configPath); err == nil {
			defer f.Close()
			sections, perr := parseTOML(f)
			if perr != nil {
				return nil, fmt.Errorf("parse config %s: %w", configPath, perr)
			}
			if aerr := applySections(cfg, sections); aerr != nil {
				return nil, fmt.Errorf("config %s: %w", configPath, aerr)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("open config %s: %w", configPath, err)
		}
	}

	if err := applyEnv(cfg); err != nil {
		return nil, err
	}
	if timeoutOverride > 0 {
		cfg.Timeout = timeoutOverride
	}
	return cfg, validate(cfg)
}

// validate rejects settings that would silently misbehave rather than letting
// them reach upstream.
func validate(cfg *Config) error {
	if cfg.DefaultLimit < 1 {
		return fmt.Errorf("[query] default_limit must be at least 1")
	}
	if cfg.MaxAll < 1 {
		return fmt.Errorf("[query] max_all must be at least 1")
	}
	if cfg.MaxAll > thc.CSVMaxLimit {
		return fmt.Errorf("[query] max_all cannot exceed %d (the upstream CSV limit)", thc.CSVMaxLimit)
	}
	return nil
}

func applySections(cfg *Config, sections map[string]map[string]string) error {
	if a := sections["api"]; a != nil {
		if v := a["base_url"]; v != "" {
			cfg.BaseURL = v
		}
	}
	if q := sections["query"]; q != nil {
		if v := q["default_limit"]; v != "" {
			n, err := parseInt(v)
			if err != nil {
				return fmt.Errorf("[query] default_limit: %w", err)
			}
			cfg.DefaultLimit = n
		}
		if v := q["max_all"]; v != "" {
			n, err := parseInt(v)
			if err != nil {
				return fmt.Errorf("[query] max_all: %w", err)
			}
			cfg.MaxAll = n
		}
		if v := q["dedup"]; v != "" {
			b, err := parseBool(v)
			if err != nil {
				return fmt.Errorf("[query] dedup: %w", err)
			}
			cfg.Dedup = b
		}
	}
	if c := sections["cache"]; c != nil {
		if v := c["ttl_hours"]; v != "" {
			d, err := parseHours(v)
			if err != nil {
				return fmt.Errorf("[cache] ttl_hours: %w", err)
			}
			cfg.CacheTTL = d
		}
		if v := c["dir"]; v != "" {
			cfg.CacheDir = expandHome(v)
		}
	}
	if n := sections["network"]; n != nil {
		if v := n["timeout_seconds"]; v != "" {
			d, err := parseSeconds(v)
			if err != nil {
				return fmt.Errorf("[network] timeout_seconds: %w", err)
			}
			cfg.Timeout = d
		}
	}
	if r := sections["ratelimit"]; r != nil {
		if v := r["min_remaining"]; v != "" {
			n, err := parseInt(v)
			if err != nil {
				return fmt.Errorf("[ratelimit] min_remaining: %w", err)
			}
			cfg.MinRemaining = n
		}
	}
	if m := sections["mcp"]; m != nil {
	}
	return nil
}

func applyEnv(cfg *Config) error {
	if v := os.Getenv("RDNS_LOOKUP_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("RDNS_LOOKUP_DEFAULT_LIMIT"); v != "" {
		n, err := parseInt(v)
		if err != nil {
			return fmt.Errorf("RDNS_LOOKUP_DEFAULT_LIMIT: %w", err)
		}
		cfg.DefaultLimit = n
	}
	if v := os.Getenv("RDNS_LOOKUP_MAX_ALL"); v != "" {
		n, err := parseInt(v)
		if err != nil {
			return fmt.Errorf("RDNS_LOOKUP_MAX_ALL: %w", err)
		}
		cfg.MaxAll = n
	}
	if v := os.Getenv("RDNS_LOOKUP_DEDUP"); v != "" {
		b, err := parseBool(v)
		if err != nil {
			return fmt.Errorf("RDNS_LOOKUP_DEDUP: %w", err)
		}
		cfg.Dedup = b
	}
	if v := os.Getenv("RDNS_LOOKUP_CACHE_DIR"); v != "" {
		cfg.CacheDir = expandHome(v)
	}
	if v := os.Getenv("RDNS_LOOKUP_CACHE_TTL_HOURS"); v != "" {
		d, err := parseHours(v)
		if err != nil {
			return fmt.Errorf("RDNS_LOOKUP_CACHE_TTL_HOURS: %w", err)
		}
		cfg.CacheTTL = d
	}
	if v := os.Getenv("RDNS_LOOKUP_TIMEOUT_SECONDS"); v != "" {
		d, err := parseSeconds(v)
		if err != nil {
			return fmt.Errorf("RDNS_LOOKUP_TIMEOUT_SECONDS: %w", err)
		}
		cfg.Timeout = d
	}
	if v := os.Getenv("RDNS_LOOKUP_MIN_REMAINING"); v != "" {
		n, err := parseInt(v)
		if err != nil {
			return fmt.Errorf("RDNS_LOOKUP_MIN_REMAINING: %w", err)
		}
		cfg.MinRemaining = n
	}
	return nil
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean", v)
}

func parseInt(v string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("%q is not an integer", v)
	}
	return n, nil
}

func parseSeconds(v string) (time.Duration, error) {
	s, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || s <= 0 {
		return 0, fmt.Errorf("%q is not a positive number", v)
	}
	return time.Duration(s * float64(time.Second)), nil
}

func parseHours(v string) (time.Duration, error) {
	h, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || h <= 0 {
		return 0, fmt.Errorf("%q is not a positive number", v)
	}
	return time.Duration(h * float64(time.Hour)), nil
}

// DefaultConfigPath returns the default config file location, honoring
// XDG_CONFIG_HOME.
func DefaultConfigPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "rdns-lookup", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "rdns-lookup", "config.toml")
}

// DefaultCacheDir returns the default cache directory, honoring
// XDG_CACHE_HOME. Cached answers are re-fetchable transient state, so they
// belong under the cache home, not data.
func DefaultCacheDir() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "rdns-lookup")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "rdns-lookup-cache"
	}
	return filepath.Join(home, ".cache", "rdns-lookup")
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// parseTOML parses the minimal subset rdns-lookup needs: [section] headers and
// key = value lines, where value is an optionally quoted string. Comments start
// with '#'. It intentionally does not support arrays, nested tables, or typed
// values.
func parseTOML(r io.Reader) (map[string]map[string]string, error) {
	sections := map[string]map[string]string{}
	current := ""
	sections[current] = map[string]string{}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		if strings.HasPrefix(raw, "[") {
			end := strings.IndexByte(raw, ']')
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated section header", line)
			}
			current = strings.TrimSpace(raw[1:end])
			if _, ok := sections[current]; !ok {
				sections[current] = map[string]string{}
			}
			continue
		}
		eq := strings.IndexByte(raw, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: expected key = value", line)
		}
		key := strings.TrimSpace(raw[:eq])
		val := parseValue(strings.TrimSpace(raw[eq+1:]))
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", line)
		}
		sections[current][key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return sections, nil
}

// parseValue strips surrounding quotes, or trims a trailing inline comment from
// a bare value.
func parseValue(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		q := v[0]
		if end := strings.IndexByte(v[1:], q); end >= 0 {
			return v[1 : 1+end]
		}
	}
	if hash := strings.IndexByte(v, '#'); hash >= 0 {
		v = strings.TrimSpace(v[:hash])
	}
	return v
}
