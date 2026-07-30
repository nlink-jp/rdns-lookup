package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nlink-jp/rdns-lookup/internal/config"
	"github.com/nlink-jp/rdns-lookup/internal/engine"
	"github.com/nlink-jp/rdns-lookup/internal/query"
	"github.com/nlink-jp/rdns-lookup/internal/thc"
)

// stdin is indirected so tests can substitute a reader.
var stdin io.Reader = os.Stdin

// runLookup implements the rdns, subdomains, and cnames commands against
// injected writers so tests can capture output. The three share every flag
// except the rdns-only filters, and differ only in which engine method runs.
func runLookup(cmd string, args []string, version string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		limit   = fs.Int("limit", 0, "records to retrieve (default 100)")
		all     = fs.Bool("all", false, "retrieve up to the ceiling")
		tldList = fs.String("tld", "", "comma-separated TLD filter (rdns, single address only)")
		apex    = fs.String("apex", "", "apex-domain filter (rdns, single address only)")
		jsonOut = fs.Bool("json", false, "JSON output")
		raw     = fs.Bool("raw", false, "include the raw upstream response bodies")
		refresh = fs.Bool("refresh", false, "bypass the result cache and re-query")
		timeout = fs.Duration("timeout", 0, "network timeout (e.g. 10s; default 30s)")
		input   = fs.String("input", "", "read newline-separated targets from a file")
		cfgPath = fs.String("config", "", "config file path")
	)
	fs.BoolVar(jsonOut, "j", false, "JSON output (shorthand)")
	fs.StringVar(cfgPath, "c", "", "config file path (shorthand)")

	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return exitError
	}

	if cmd != "rdns" && (*tldList != "" || *apex != "") {
		fmt.Fprintf(stderr, "%s: --tld and --apex apply to the rdns command only\n", cmd)
		return exitError
	}

	targets, err := readTargets(positionals, *input, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	if len(targets) == 0 {
		fmt.Fprintf(stderr, "%s: at least one %s is required\n", cmd, targetNoun(cmd))
		return exitError
	}

	cfg, err := config.Load(*cfgPath, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	e := engine.New(cfg, version)

	opts := engine.Options{
		Limit:      *limit,
		All:        *all,
		TLDs:       splitList(*tldList),
		ApexDomain: *apex,
		Refresh:    *refresh,
		Raw:        *raw,
	}

	var results []*engine.Result
	hadError, empty, found := false, 0, 0
	for _, tgt := range targets {
		res, lerr := lookupOne(e, cmd, tgt, opts)
		switch {
		case errors.Is(lerr, engine.ErrNoRecords):
			empty++
			results = append(results, res)
		case errors.Is(lerr, query.ErrInvalid), thc.IsInvalidTarget(lerr):
			fmt.Fprintf(stderr, "%s: %v\n", tgt, lerr)
			hadError = true
		case lerr != nil:
			fmt.Fprintf(stderr, "%s: error: %v\n", tgt, lerr)
			hadError = true
		default:
			found++
			results = append(results, res)
		}
	}

	for _, r := range results {
		engine.SortRecords(r.Records)
	}
	if *jsonOut {
		writeJSON(stdout, results)
	} else {
		writeText(stdout, results)
	}

	switch {
	case hadError:
		return exitError
	case found == 0 && empty > 0:
		return exitNoRecord
	default:
		return exitOK
	}
}

func lookupOne(e *engine.Engine, cmd, target string, opts engine.Options) (*engine.Result, error) {
	switch cmd {
	case "subdomains":
		return e.LookupSubdomains(target, opts)
	case "cnames":
		return e.LookupCNAMEs(target, opts)
	default:
		return e.LookupRDNS(target, opts)
	}
}

func targetNoun(cmd string) string {
	if cmd == "rdns" {
		return "target (IP address or octet-boundary CIDR block)"
	}
	return "domain"
}

// readTargets assembles the target list from positionals, an optional --input
// file, and (when neither is given) stdin.
func readTargets(positionals []string, inputPath string, in io.Reader) ([]string, error) {
	targets := append([]string(nil), positionals...)
	if inputPath != "" {
		f, err := os.Open(inputPath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		targets = append(targets, scanTargets(f)...)
	} else if len(targets) == 0 {
		targets = append(targets, scanTargets(in)...)
	}
	return targets, nil
}

// scanTargets reads newline-separated targets, skipping blanks and #-comments.
// Each line may hold whitespace-separated targets.
func scanTargets(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.Fields(line)...)
	}
	return out
}

func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseInterspersed parses fs while tolerating flags that appear after
// positional arguments. Validated targets never begin with '-', so there is no
// ambiguity.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positionals = append(positionals, args[0])
		args = args[1:]
	}
	return positionals, nil
}

// writeJSON prints one indented object for a single result, or JSONL (one
// compact object per line) for a bulk run.
func writeJSON(w io.Writer, results []*engine.Result) {
	enc := json.NewEncoder(w)
	if len(results) == 1 {
		enc.SetIndent("", "  ")
	}
	for _, r := range results {
		_ = enc.Encode(r)
	}
}

// writeText renders each result as a provenance header plus its records.
func writeText(w io.Writer, results []*engine.Result) {
	for i, r := range results {
		if i > 0 {
			fmt.Fprintln(w)
		}
		writeTextOne(w, r)
	}
}

func writeTextOne(w io.Writer, r *engine.Result) {
	fmt.Fprintf(w, "%s  [%s]  %s\n", r.Query, r.Kind, countSummary(r))
	fmt.Fprintf(w, "  source: %s (%s face)%s%s\n",
		r.Source, r.Route, cachedNote(r), rateNote(r))
	if r.Truncated {
		fmt.Fprintf(w, "  note: %s\n", r.TruncationNote)
	}
	if len(r.Records) == 0 {
		fmt.Fprintln(w, "  (nothing indexed)")
		return
	}
	for _, rec := range r.Records {
		fmt.Fprintf(w, "  %s%s\n", rec.Domain, recordDetail(rec))
	}
}

// countSummary states what was retrieved against what upstream holds, so a
// capped answer never looks complete.
func countSummary(r *engine.Result) string {
	s := fmt.Sprintf("%d record%s", r.Count, plural(r.Count, "", "s"))
	if r.MatchingRecords > 0 {
		s += fmt.Sprintf(" of %d upstream", r.MatchingRecords)
	}
	if r.Duplicates > 0 {
		s += fmt.Sprintf(", %d duplicate%s removed", r.Duplicates, plural(r.Duplicates, "", "s"))
	}
	if r.Truncated {
		s += ", TRUNCATED"
	}
	return s
}

func cachedNote(r *engine.Result) string {
	if r.Cached {
		return ", cached"
	}
	return fmt.Sprintf(", %d request%s", r.Requests, plural(r.Requests, "", "s"))
}

func rateNote(r *engine.Result) string {
	if r.Cached || r.RateLimit.Unknown() {
		return ""
	}
	return fmt.Sprintf(", rate budget %d/%d left", r.RateLimit.Remaining, r.RateLimit.Limit)
}

// recordDetail appends whatever enrichment upstream supplied for this record.
// Which fields are present depends on the lookup kind, so an empty tail is
// normal for subdomains and cnames rather than a sign of missing data.
func recordDetail(rec thc.Record) string {
	var parts []string
	if rec.IPAddress != "" {
		parts = append(parts, rec.IPAddress)
	}
	if rec.ASN != "" {
		asn := "AS" + rec.ASN
		if rec.Org != "" {
			asn += " " + rec.Org
		}
		parts = append(parts, asn)
	}
	if loc := location(rec); loc != "" {
		parts = append(parts, loc)
	}
	if rec.LastSeen != "" {
		parts = append(parts, "last seen "+rec.LastSeen)
	}
	if len(parts) == 0 {
		return ""
	}
	return "  (" + strings.Join(parts, ", ") + ")"
}

func location(rec thc.Record) string {
	switch {
	case rec.City != "" && rec.Country != "":
		return rec.City + ", " + rec.Country
	case rec.Country != "":
		return rec.Country
	case rec.City != "":
		return rec.City
	}
	return ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
