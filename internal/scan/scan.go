// Package scan walks a source directory and selects files using
// explicit include/exclude glob lists.
//
// Globs use github.com/gobwas/glob with "/" as the path separator, so
// "**" matches any number of segments and "*" matches one segment.
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

func compileAll(patterns []string) ([]glob.Glob, error) {
	out := make([]glob.Glob, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		g, err := glob.Compile(p, '/')
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", p, err)
		}
		out = append(out, g)
		// gitignore-style ergonomics: "**/foo" should also match "foo"
		// at depth zero. gobwas/glob keeps the literal "/" so without
		// this we'd surprise users by missing root-level files.
		if rest, ok := strings.CutPrefix(p, "**/"); ok && rest != "" {
			g2, err := glob.Compile(rest, '/')
			if err != nil {
				return nil, fmt.Errorf("invalid pattern %q: %w", rest, err)
			}
			out = append(out, g2)
		}
	}
	return out, nil
}

func matchAny(globs []glob.Glob, s string) bool {
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
