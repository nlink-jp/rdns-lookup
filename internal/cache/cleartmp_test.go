package cache

import (
	"os"
	"path/filepath"
	"testing"
)

// cache clear also removes the temp file a write left when the process ended
// part-way (a SIGTERM after the MCP server's grace, a crash): it held an
// answer, and Clear is the one way an operator deletes cached answers.
func TestClearRemovesALeftoverTempFile(t *testing.T) {
	dir := t.TempDir()
	left := filepath.Join(dir, ".tmp-123456")
	if err := os.WriteFile(left, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: dir}
	if _, err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Fatalf("the temp file is still there (%v)", err)
	}
}
