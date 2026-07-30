package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "ws")
	ws, err := Ensure(dir)
	if err != nil {
		t.Fatalf("Ensure returned error %v", err)
	}
	if fi, err := os.Stat(ws.BaseDir); err != nil || !fi.IsDir() {
		t.Fatalf("Ensure did not create a directory: %v", err)
	}
	if !filepath.IsAbs(ws.BaseDir) {
		t.Errorf("BaseDir %q should be absolute", ws.BaseDir)
	}
}

// Guessing an output location is not ours to do, so an empty dir is an error.
func TestEnsureRequiresDir(t *testing.T) {
	for _, in := range []string{"", "   "} {
		if _, err := Ensure(in); !errors.Is(err, ErrNoDir) {
			t.Errorf("Ensure(%q) error = %v, want ErrNoDir", in, err)
		}
	}
}

func TestWriteFileAtomic(t *testing.T) {
	ws, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	path, err := ws.WriteFileAtomic("out.jsonl", []byte("{\"a\":1}\n"))
	if err != nil {
		t.Fatalf("WriteFileAtomic returned error %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "{\"a\":1}\n" {
		t.Errorf("content = %q", got)
	}
	if filepath.Dir(path) != ws.BaseDir {
		t.Errorf("path %q should sit directly in the workspace", path)
	}
}

func TestWriteFileAtomicLeavesNoTempFile(t *testing.T) {
	ws, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := ws.WriteFileAtomic("out.jsonl", []byte("x")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	entries, err := os.ReadDir(ws.BaseDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("a temp file survived: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("%d entries, want 1", len(entries))
	}
}

func TestWriteFileAtomicOverwrites(t *testing.T) {
	ws, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := ws.WriteFileAtomic("out.jsonl", []byte("first")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	path, err := ws.WriteFileAtomic("out.jsonl", []byte("second"))
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "second" {
		t.Errorf("content = %q, want the second write", got)
	}
}

// The directory comes from tool arguments, which are attacker-reachable in an
// agent setting, so an escape must be refused rather than merely unlikely.
func TestWriteFileRejectsEscape(t *testing.T) {
	base := t.TempDir()
	ws, err := Ensure(filepath.Join(base, "ws"))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	escapes := []string{
		"../escaped.jsonl",
		"../../escaped.jsonl",
		"a/../../escaped.jsonl",
		filepath.Join(base, "absolute.jsonl"),
		"/etc/passwd",
		"",
		"   ",
	}
	for _, rel := range escapes {
		if _, err := ws.WriteFileAtomic(rel, []byte("x")); err == nil {
			t.Errorf("WriteFileAtomic(%q) should have been refused", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "escaped.jsonl")); !os.IsNotExist(err) {
		t.Error("a file escaped the workspace")
	}
}

func TestPath(t *testing.T) {
	ws, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got, want := ws.Path("a", "b.jsonl"), filepath.Join(ws.BaseDir, "a", "b.jsonl"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}
