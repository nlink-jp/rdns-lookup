package app

import (
	"flag"
	"fmt"
	"io"

	"github.com/nlink-jp/rdns-lookup/internal/cache"
	"github.com/nlink-jp/rdns-lookup/internal/config"
)

// runCache implements `cache status` and `cache clear`.
func runCache(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cache", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file path")
	fs.StringVar(cfgPath, "c", "", "config file path (shorthand)")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return exitError
	}
	if len(positionals) != 1 || (positionals[0] != "status" && positionals[0] != "clear") {
		fmt.Fprintln(stderr, "usage: rdns-lookup cache status|clear")
		return exitError
	}
	cfg, err := config.Load(*cfgPath, 0)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	store := &cache.Store{Dir: cfg.CacheDir}

	switch positionals[0] {
	case "clear":
		n, err := store.Clear()
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
		fmt.Fprintf(stdout, "cleared %d cached entr%s from %s\n", n, plural(n, "y", "ies"), cfg.CacheDir)
		return exitOK
	default: // status
		fmt.Fprintf(stdout, "cache dir:      %s\n", cfg.CacheDir)
		fmt.Fprintf(stdout, "entries:        %d\n", store.Count())
		fmt.Fprintf(stdout, "ttl:            %s\n", cfg.CacheTTL)
		fmt.Fprintf(stdout, "default limit:  %d\n", cfg.DefaultLimit)
		fmt.Fprintf(stdout, "--all ceiling:  %d\n", cfg.MaxAll)
		fmt.Fprintf(stdout, "source:         ip.thc.org\n")
		return exitOK
	}
}
