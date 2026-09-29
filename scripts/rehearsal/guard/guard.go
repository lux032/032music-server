// Package guard holds the path-safety checks shared by the rehearsal helper
// tools (migrate.go / dbexec.go / dbquery.go). It is a regular package (no
// //go:build ignore) precisely so these checks are unit-testable; the tools
// themselves stay ignored by ./... builds.
package guard

import (
	"fmt"
	"path/filepath"
	"strings"
)

// CheckCopyDBPath validates a rehearsal copy database path. It rejects paths
// that resolve (Abs + EvalSymlinks) into the real data directory
// (.local/data), and paths identical to sourcePath (the real database the
// rehearsal copies from, passed by rehearsal.sh as REHEARSAL_SOURCE).
// Comparison folds case (Windows) and unifies separators.
func CheckCopyDBPath(dbPath, sourcePath string) error {
	resolved, err := Resolve(dbPath)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", dbPath, err)
	}
	if IsUnderDataDir(resolved) {
		return fmt.Errorf("refusing real-database path (.local/data): %s", resolved)
	}
	if strings.TrimSpace(sourcePath) != "" {
		source, err := Resolve(sourcePath)
		if err == nil && SamePath(resolved, source) {
			return fmt.Errorf("refusing to operate on the source database itself: %s", resolved)
		}
	}
	return nil
}

// Resolve returns an absolute, symlink-evaluated, cleaned path.
func Resolve(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}

// Normal folds case and unifies separators for comparison (Windows paths are
// case-insensitive).
func Normal(p string) string {
	return strings.ToLower(strings.ReplaceAll(p, "/", `\`))
}

// SamePath reports whether two resolved paths name the same file.
func SamePath(a, b string) bool { return Normal(a) == Normal(b) }

// IsUnderDataDir reports whether a resolved path lies inside the real data
// directory (.local/data). Similar prefixes (.local/database) do not match.
func IsUnderDataDir(resolved string) bool {
	n := Normal(resolved)
	return strings.Contains(n, `\.local\data\`) || strings.HasSuffix(n, `\.local\data`)
}
