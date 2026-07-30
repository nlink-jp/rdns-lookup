package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoDir marks a missing output directory: file-mediated results need
// somewhere to go, and guessing a location is not our call to make.
var ErrNoDir = errors.New("workspace: no output directory (pass workspace_root)")

// Workspace is a directory that file-mediated results are written into.
type Workspace struct {
	BaseDir string
}

// Ensure resolves and creates dir, returning a Workspace rooted at it.
func Ensure(dir string) (*Workspace, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, ErrNoDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("workspace: resolve %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("workspace: create %q: %w", abs, err)
	}
	return &Workspace{BaseDir: abs}, nil
}

// Path returns the absolute path of a relative name inside the workspace.
func (w *Workspace) Path(parts ...string) string {
	return filepath.Join(append([]string{w.BaseDir}, parts...)...)
}

// WriteFileAtomic writes data to rel inside the workspace and returns the
// absolute path. The write is atomic (temp file + rename) and confined to the
// workspace by both a lexical check and os.OpenRoot.
func (w *Workspace) WriteFileAtomic(rel string, data []byte) (string, error) {
	clean, err := resolveInside(rel)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(w.BaseDir)
	if err != nil {
		return "", fmt.Errorf("workspace: open root: %w", err)
	}
	defer root.Close()

	tmpName := ".tmp-" + filepath.Base(clean)
	f, err := root.Create(tmpName)
	if err != nil {
		return "", fmt.Errorf("workspace: create %s: %w", tmpName, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = root.Remove(tmpName)
		return "", fmt.Errorf("workspace: write %s: %w", tmpName, err)
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmpName)
		return "", fmt.Errorf("workspace: close %s: %w", tmpName, err)
	}
	if err := root.Rename(tmpName, clean); err != nil {
		_ = root.Remove(tmpName)
		return "", fmt.Errorf("workspace: rename to %s: %w", clean, err)
	}
	return w.Path(clean), nil
}

// resolveInside rejects a relative path that could escape the workspace.
func resolveInside(rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", errors.New("workspace: empty file name")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("workspace: %q must be relative", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace: %q escapes the workspace", rel)
	}
	return clean, nil
}
