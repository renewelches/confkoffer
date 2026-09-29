// Package scan walks a source directory and selects files using
// explicit include/exclude glob lists.
//
// Globs use github.com/gobwas/glob with "/" as the path separator, so
// "*" matches within one segment and "**" across segments. As in
// gitignore, a "**/" segment also matches zero directories: "a/**/b"
// selects "a/b" as well as "a/x/b" (see expandDoubleStar).
// A file is selected iff it matches at least one include and zero
// excludes (exclude wins on conflict).
//
// Symlinks are not followed; encountering one logs a warning and skips
// it. Path comparisons use forward-slash relative paths so behaviour is
// stable across platforms.
package scan

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/gobwas/glob"
)

// Patterns is the explicit allow/deny list for a scan.
type Patterns struct {
	Include []string
	Exclude []string
}

// Match describes a file selected by Walk.
type Match struct {
	AbsPath string      // absolute path on disk
	RelPath string      // forward-slash path relative to srcDir
	Mode    os.FileMode // permission bits
	Size    int64
}

// Walk traverses srcDir recursively and returns the files that match
// patterns. Returned matches are sorted by RelPath.
func Walk(srcDir string, patterns Patterns) ([]Match, error) {
	if len(patterns.Include) == 0 {
		return nil, fmt.Errorf("scan: no include patterns configured")
	}

	includes, err := compileAll(patterns.Include)
	if err != nil {
		return nil, fmt.Errorf("compile include: %w", err)
	}
	excludes, err := compileAll(patterns.Exclude)
	if err != nil {
		return nil, fmt.Errorf("compile exclude: %w", err)
	}

	absRoot, err := filepath.Abs(srcDir)
	if err != nil {
		return nil, fmt.Errorf("resolve srcDir: %w", err)
	}

	var matches []Match
	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absRoot {
			return nil
		}

		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)

		// Symlinks: log + skip without descending.
		if d.Type()&fs.ModeSymlink != 0 {
			slog.Warn("scan: skipping symlink", "path", relSlash)
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			// Directories themselves are not "files"; we descend.
			return nil
		}

		if !d.Type().IsRegular() {
			slog.Warn("scan: skipping non-regular file", "path", relSlash, "mode", d.Type().String())
			return nil
		}

		if !matchAny(includes, relSlash) {
			return nil
		}
		if matchAny(excludes, relSlash) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", relSlash, err)
		}

		matches = append(matches, Match{
			AbsPath: path,
			RelPath: relSlash,
			Mode:    info.Mode(),
			Size:    info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sort for deterministic ordering across platforms.
	sortMatches(matches)
	return matches, nil
}

// maxDoubleStarDirs caps the "**/" segments in one pattern. Each one
// doubles the variants expandDoubleStar compiles; real patterns use one
// or two.
const maxDoubleStarDirs = 8

func compileAll(patterns []string) ([]*glob.Pattern, error) {
	out := make([]*glob.Pattern, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		variants, err := expandDoubleStar(p)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", p, err)
		}
		for _, v := range variants {
			g, err := glob.Compile(v, '/')
			if err != nil {
				return nil, fmt.Errorf("invalid pattern %q: %w", p, err)
			}
			out = append(out, g)
		}
	}
	return out, nil
}

// expandDoubleStar returns p followed by every variant of p with one or
// more "**/" segments removed. gobwas/glob requires "**/" to consume at
// least the "/", so on its own "secrets/**/prod.env" misses
// "secrets/prod.env"; compiling the variants too gives gitignore's
// zero-or-more-directories semantics. The original comes first so a
// syntax error is reported against what the user wrote.
func expandDoubleStar(p string) ([]string, error) {
	cuts := doubleStarDirs(p)
	if len(cuts) > maxDoubleStarDirs {
		return nil, fmt.Errorf("more than %d \"**/\" segments", maxDoubleStarDirs)
	}
	variants := []string{p}
	// Cut from the last offset to the first: removing text to the right
	// of an offset leaves it valid in every variant built so far.
	for i := len(cuts) - 1; i >= 0; i-- {
		at, n := cuts[i], len(variants)
		for _, v := range variants[:n] {
			variants = append(variants, v[:at]+v[at+len("**/"):])
		}
	}
	// "**/**/x" yields "**/x" twice, and a bare "**/" yields "".
	seen := make(map[string]bool, len(variants))
	out := variants[:0]
	for _, v := range variants {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, nil
}

// doubleStarDirs returns the offset of each "**/" in p that is a whole
// path segment: at the start of p or right after a "/". Escaped
// characters and [...] classes are skipped, so "a**/b", `\**/x` and
// "[**/]" are left alone.
func doubleStarDirs(p string) []int {
	var cuts []int
	inClass := false
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '\\':
			i++ // the next byte is literal
		case inClass:
			inClass = c != ']'
		case c == '[':
			inClass = true
		case strings.HasPrefix(p[i:], "**/") && (i == 0 || p[i-1] == '/'):
			cuts = append(cuts, i)
			i += len("**/") - 1
		}
	}
	return cuts
}

func matchAny(globs []*glob.Pattern, s string) bool {
	for _, g := range globs {
		if g.Match(s) {
			return true
		}
	}
	return false
}

func sortMatches(m []Match) {
	// Use a simple insertion sort to keep the package dependency-light.
	// Real input sizes are small (a handful to a few thousand entries).
	for i := 1; i < len(m); i++ {
		for j := i; j > 0 && m[j-1].RelPath > m[j].RelPath; j-- {
			m[j-1], m[j] = m[j], m[j-1]
		}
	}
}
