package app

import (
	"bytes"
	"strings"
	"testing"
)

// isolate points config and cache at throwaway dirs so tests never touch the
// developer's real state, and makes sure nothing reaches the real network.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("RDNS_LOOKUP_CACHE_DIR", t.TempDir())
	t.Setenv("RDNS_LOOKUP_BASE_URL", "http://127.0.0.1:1/unreachable")
}

func TestRunNoArgsIsUsageError(t *testing.T) {
	isolate(t)
	if got := Run(nil, "test"); got != exitError {
		t.Errorf("Run(nil) = %d, want %d", got, exitError)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	isolate(t)
	if got := Run([]string{"bogus"}, "test"); got != exitError {
		t.Errorf("Run(bogus) = %d, want %d", got, exitError)
	}
}

func TestVersionCommand(t *testing.T) {
	isolate(t)
	for _, arg := range []string{"version", "--version", "-v"} {
		if got := Run([]string{arg}, "v1.2.3"); got != exitOK {
			t.Errorf("Run(%q) = %d, want %d", arg, got, exitOK)
		}
	}
}

func TestHelpCommand(t *testing.T) {
	isolate(t)
	for _, arg := range []string{"help", "-h", "--help"} {
		if got := Run([]string{arg}, "test"); got != exitOK {
			t.Errorf("Run(%q) = %d, want %d", arg, got, exitOK)
		}
	}
}

// The usage text is where a user learns the two things most likely to trip them
// up: the octet-boundary limit, and that this is not PTR.
func TestUsageExplainsTheTraps(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	text := buf.String()
	for _, want := range []string{"rdns", "subdomains", "cnames", "octet-boundary", "not PTR", "ip.thc.org"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage text should mention %q", want)
		}
	}
}

func TestLookupRequiresATarget(t *testing.T) {
	isolate(t)
	for _, cmd := range []string{"rdns", "subdomains", "cnames"} {
		var stdout, stderr bytes.Buffer
		stdin = strings.NewReader("")
		got := runLookup(cmd, nil, "test", &stdout, &stderr)
		if got != exitError {
			t.Errorf("%s with no target = %d, want %d", cmd, got, exitError)
		}
		if !strings.Contains(stderr.String(), "required") {
			t.Errorf("%s should say a target is required, got %q", cmd, stderr.String())
		}
	}
}

// Validation runs before any network I/O, so an invalid target fails fast even
// with an unreachable upstream configured.
func TestInvalidTargetsRejectedWithoutNetwork(t *testing.T) {
	isolate(t)
	tests := []struct{ cmd, target string }{
		{"rdns", "142.251.43.0/25"},
		{"rdns", "not-an-ip"},
		{"subdomains", "1.1.1.1"},
		{"cnames", "localhost"},
	}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		got := runLookup(tt.cmd, []string{tt.target}, "test", &stdout, &stderr)
		if got != exitError {
			t.Errorf("%s %s = %d, want %d", tt.cmd, tt.target, got, exitError)
		}
		if stderr.Len() == 0 {
			t.Errorf("%s %s should explain the rejection", tt.cmd, tt.target)
		}
	}
}

// --tld and --apex only mean anything for rdns; accepting them elsewhere would
// silently do nothing.
func TestFiltersRejectedOnDomainCommands(t *testing.T) {
	isolate(t)
	for _, cmd := range []string{"subdomains", "cnames"} {
		for _, flag := range []string{"--tld=com", "--apex=example.com"} {
			var stdout, stderr bytes.Buffer
			got := runLookup(cmd, []string{flag, "example.com"}, "test", &stdout, &stderr)
			if got != exitError {
				t.Errorf("%s %s = %d, want %d", cmd, flag, got, exitError)
			}
			if !strings.Contains(stderr.String(), "rdns") {
				t.Errorf("%s %s should say the flag is rdns-only, got %q", cmd, flag, stderr.String())
			}
		}
	}
}

func TestCacheStatusAndClear(t *testing.T) {
	isolate(t)
	var stdout, stderr bytes.Buffer
	if got := runCache([]string{"status"}, &stdout, &stderr); got != exitOK {
		t.Fatalf("cache status = %d, stderr %q", got, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"cache dir:", "entries:", "ttl:", "ip.thc.org"} {
		if !strings.Contains(out, want) {
			t.Errorf("cache status output should contain %q, got %q", want, out)
		}
	}

	stdout.Reset()
	if got := runCache([]string{"clear"}, &stdout, &stderr); got != exitOK {
		t.Fatalf("cache clear = %d, stderr %q", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "cleared") {
		t.Errorf("cache clear output = %q", stdout.String())
	}
}

func TestCacheBadSubcommand(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{nil, {"bogus"}, {"status", "clear"}} {
		var stdout, stderr bytes.Buffer
		if got := runCache(args, &stdout, &stderr); got != exitError {
			t.Errorf("cache %v = %d, want %d", args, got, exitError)
		}
	}
}

func TestScanTargetsSkipsBlanksAndComments(t *testing.T) {
	in := strings.NewReader("1.1.1.1\n\n# a comment\n8.8.8.8 9.9.9.9\n  \n")
	got := scanTargets(in)
	want := []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}
	if len(got) != len(want) {
		t.Fatalf("scanTargets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scanTargets = %v, want %v", got, want)
		}
	}
}

func TestReadTargetsPrefersPositionals(t *testing.T) {
	got, err := readTargets([]string{"1.1.1.1"}, "", strings.NewReader("8.8.8.8\n"))
	if err != nil {
		t.Fatalf("readTargets: %v", err)
	}
	if len(got) != 1 || got[0] != "1.1.1.1" {
		t.Errorf("readTargets = %v, want [1.1.1.1] — stdin should not be read", got)
	}
}

func TestReadTargetsMissingInputFile(t *testing.T) {
	if _, err := readTargets(nil, "/nonexistent/targets.txt", strings.NewReader("")); err == nil {
		t.Error("a missing --input file should error")
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" com , net ,, org ")
	want := []string{"com", "net", "org"}
	if len(got) != len(want) {
		t.Fatalf("splitList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitList = %v, want %v", got, want)
		}
	}
	if got := splitList("  "); got != nil {
		t.Errorf("splitList(blank) = %v, want nil", got)
	}
}

// Flags may follow positionals; validated targets never start with '-'.
func TestParseInterspersed(t *testing.T) {
	isolate(t)
	var stdout, stderr bytes.Buffer
	// An invalid target still proves the flag was parsed: without --limit being
	// consumed, flag parsing itself would have failed first.
	got := runLookup("rdns", []string{"142.251.43.0/25", "--limit", "5"}, "test", &stdout, &stderr)
	if got != exitError {
		t.Fatalf("= %d, want %d", got, exitError)
	}
	if !strings.Contains(stderr.String(), "octet-boundary") {
		t.Errorf("stderr = %q, want the validation message (so the flag parsed)", stderr.String())
	}
}
