// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// WalkFunc receives one descendant of the traversal root: a clean,
// policy-root-relative slash path, whether it is a directory, and its policy
// decision. Returning filepath.SkipDir skips the remaining entries of the
// containing directory (or the descent of a directory); any other error
// aborts the walk and is returned by Walk unchanged.
type WalkFunc func(rel string, isDir bool, d PolicyDecision) error

// Walk traverses the subtree rooted at traverse and reports every descendant
// to fn with its policy decision. traverse may be empty or "." for the whole
// policy root, a clean root-relative slash directory, or an absolute path
// inside the policy root. The traversal root itself is not reported; if it is
// ignored or reserved, nothing is visited. Policy namespaces stay rooted at
// the policy root regardless of the traversal root.
//
// Walk never follows symlinks: they are reported as non-directories and their
// targets are not visited. Entries arrive in deterministic lexicographic
// order. Directories whose decision is Ignored (excluded endpoint, blocking
// ancestor or reserved) are reported and then pruned: their descendants are
// neither visited nor discovered. Ignore source files are ordinary candidates
// and are not implicitly excluded. The first filesystem or policy-discovery
// error aborts the walk and is returned wrapped with its path.
func (p *Policy) Walk(traverse string, fn WalkFunc) error {
	if fn == nil {
		return fmt.Errorf("walk callback: %w", ErrInvalidInput)
	}
	rel, err := p.traverseRel(traverse)
	if err != nil {
		return err
	}
	if rel != "" {
		d, err := p.Match(rel, true)
		if err != nil {
			return err
		}
		if d.Ignored() {
			return nil
		}
	}
	start := p.root
	if rel != "" {
		start = filepath.Join(p.root, filepath.FromSlash(rel))
	}
	return filepath.WalkDir(start, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", p.relDisplay(full), err)
		}
		if full == start {
			return nil
		}
		entryRel, err := p.relOf(full)
		if err != nil {
			return err
		}
		isDir := entry.IsDir()
		d, err := p.Match(entryRel, isDir)
		if err != nil {
			return err
		}
		if err := fn(entryRel, isDir, d); err != nil {
			return err
		}
		if isDir && d.Ignored() {
			return filepath.SkipDir
		}
		return nil
	})
}

// traverseRel validates and normalises a traversal root to a clean
// root-relative slash directory ("" for the policy root).
func (p *Policy) traverseRel(traverse string) (string, error) {
	if traverse == "" || traverse == "." {
		return "", nil
	}
	if filepath.IsAbs(traverse) {
		rel, err := filepath.Rel(p.root, filepath.Clean(traverse))
		if err != nil {
			return "", fmt.Errorf("traversal root %q: %w", traverse, err)
		}
		rel = filepath.ToSlash(rel)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return "", fmt.Errorf("traversal root %q: %w outside policy root", traverse, ErrInvalidInput)
		}
		if rel == "." {
			return "", nil
		}
		if err := validatePath(rel, false); err != nil {
			return "", fmt.Errorf("traversal root %q: %w", traverse, err)
		}
		return rel, nil
	}
	slash := filepath.ToSlash(traverse)
	if err := validatePath(slash, false); err != nil {
		return "", fmt.Errorf("traversal root %q: %w", traverse, err)
	}
	return slash, nil
}

// relOf converts a walk path to a clean root-relative slash path.
func (p *Policy) relOf(full string) (string, error) {
	rest := strings.TrimPrefix(full, p.root)
	rest = strings.TrimPrefix(rest, string(filepath.Separator))
	if rest == "" || rest == full {
		return "", fmt.Errorf("walk %s: %w outside policy root", p.relDisplay(full), ErrInvalidInput)
	}
	rel := filepath.ToSlash(rest)
	if err := validatePath(rel, false); err != nil {
		return "", fmt.Errorf("walk %s: %w", p.relDisplay(full), err)
	}
	return rel, nil
}

// relDisplay renders a filesystem path for error messages.
func (p *Policy) relDisplay(full string) string {
	rest := strings.TrimPrefix(full, p.root)
	rest = strings.TrimPrefix(rest, string(filepath.Separator))
	if rest == "" {
		return "."
	}
	return filepath.ToSlash(rest)
}
