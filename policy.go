// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// SourceSpec describes one named ignore source discovered on the filesystem.
type SourceSpec struct {
	// Filename is the source file path relative to its directory: a base name
	// such as ".gitignore" for nested sources, or a clean subpath such as
	// "info/exclude" for root-only sources. It uses slash separators, no
	// leading slash and no ".", ".." or empty parts.
	Filename string
	// Rank is the fixed precedence rank of every file of this source; depth
	// never overrides it.
	Rank int
	// Optional treats an absent file as no rules instead of an error. It
	// governs absence only; a file that exists but cannot be read is always
	// an error.
	Optional bool
	// Nested discovers the file in every directory below the root; otherwise
	// only at the root. Nested filenames must be base names.
	Nested bool
}

// PolicyOptions configures a filesystem policy.
type PolicyOptions struct {
	// Root is the required policy root. A relative root is resolved against
	// the process working directory once during construction; the policy
	// never reads the working directory afterwards.
	Root string
	// Sources lists the named ignore sources, in stable tie-breaking order.
	Sources []SourceSpec
	// Reserved lists root-relative clean paths that are unconditionally
	// excluded and cannot be negated. A reserved path covers itself and all
	// of its descendants.
	Reserved []string
	// Options passes matching options (folding, source size cap) to every
	// compiled source.
	Options Options
}

// PolicyDecision extends Decision with policy-layer outcomes.
type PolicyDecision struct {
	Decision
	// Reserved reports that the path matched a reserved exclusion. Reserved
	// decisions carry Action Exclude and no Rule.
	Reserved bool
}

// Ignored reports either an excluded endpoint or a blocking ancestor, including
// reserved exclusions.
func (d PolicyDecision) Ignored() bool { return d.Reserved || d.Decision.Ignored() }

// dirPolicy is the cached state of one directory: the cumulative sources from
// the root down to it, and the matcher compiled from them. Exactly one of
// matcher and err is set; err is remembered so the Policy stays deterministic.
type dirPolicy struct {
	sources     []Source
	matcher     *Matcher
	diagnostics []Diagnostic
	err         error
}

// Policy layers named ignore sources over a fixed root. Construction performs
// validation only; source files are discovered and read lazily, at most once
// per directory per Policy, and cached for the Policy's lifetime (including
// the first error). Create a new Policy to observe filesystem changes. A
// Policy is safe for concurrent use.
type Policy struct {
	root string
	// specs and reserved are immutable after construction.
	specs    []SourceSpec
	reserved map[string]struct{}
	opts     Options
	mu       sync.Mutex
	dirs     map[string]*dirPolicy
}

// NewPolicy validates the options and resolves the root to an absolute,
// cleaned path. It performs no filesystem I/O and does not require the root
// to exist; discovery happens on first use.
func NewPolicy(opts PolicyOptions) (*Policy, error) {
	if opts.Root == "" {
		return nil, fmt.Errorf("policy root: %w", ErrInvalidInput)
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("policy root: %w", err)
	}
	if len(opts.Sources) == 0 {
		return nil, fmt.Errorf("policy sources: %w", ErrInvalidInput)
	}
	specs := slices.Clone(opts.Sources)
	for i, spec := range specs {
		if err := validateFilename(spec.Filename, spec.Nested); err != nil {
			return nil, fmt.Errorf("source %d filename: %w", i, err)
		}
	}
	reserved := make(map[string]struct{}, len(opts.Reserved))
	for i, path := range opts.Reserved {
		if strings.HasPrefix(path, "/") {
			return nil, fmt.Errorf("reserved %d %q: %w", i, path, ErrInvalidInput)
		}
		if err := validatePath(path, false); err != nil {
			return nil, fmt.Errorf("reserved %d: %w", i, err)
		}
		reserved[path] = struct{}{}
	}
	return &Policy{
		root:     root,
		specs:    specs,
		reserved: reserved,
		opts:     opts.Options,
		dirs:     make(map[string]*dirPolicy),
	}, nil
}

// Root returns the absolute, cleaned policy root.
func (p *Policy) Root() string { return p.root }

// validateFilename checks a source filename: slash-separated, no leading
// slash, no NUL, no empty, "." or ".." parts; nested filenames are base names.
func validateFilename(filename string, nested bool) error {
	if filename == "" || strings.IndexByte(filename, 0) >= 0 {
		return ErrInvalidInput
	}
	if strings.HasPrefix(filename, "/") {
		return ErrInvalidInput
	}
	for part := range strings.SplitSeq(filename, "/") {
		if part == "" || part == "." || part == ".." {
			return ErrInvalidInput
		}
	}
	if nested && strings.Contains(filename, "/") {
		return ErrInvalidInput
	}
	return nil
}

// isReserved reports whether rel is reserved itself or lies below a reserved
// path. Reserved paths are byte-exact and unaffected by CaseFold.
func (p *Policy) isReserved(rel string) bool {
	if _, ok := p.reserved[rel]; ok {
		return true
	}
	for i := 0; i < len(rel); i++ {
		if rel[i] == '/' {
			if _, ok := p.reserved[rel[:i]]; ok {
				return true
			}
		}
	}
	return false
}

// Match evaluates a clean, root-relative slash path against the policy.
// Reserved paths are decided before any rule and cannot be negated. Source
// discovery for the path's directory (and its ancestors) happens on first
// use; discovery errors are returned and remain stable for the Policy.
func (p *Policy) Match(rel string, isDir bool) (PolicyDecision, error) {
	if err := validatePath(rel, false); err != nil {
		return PolicyDecision{}, fmt.Errorf("path: %w", err)
	}
	if p.isReserved(rel) {
		return PolicyDecision{Decision: Decision{Action: Exclude}, Reserved: true}, nil
	}
	dir := ""
	if at := strings.LastIndexByte(rel, '/'); at >= 0 {
		dir = rel[:at]
	}
	state, err := p.matcherFor(dir)
	if err != nil {
		return PolicyDecision{}, err
	}
	decision, err := state.matcher.Match(rel, isDir)
	if err != nil {
		return PolicyDecision{}, err
	}
	return PolicyDecision{Decision: decision}, nil
}

// MatchPath evaluates an absolute path against the policy. Containment is
// lexical, against the resolved policy root: like Git's gitignore matching,
// physical escapes through intermediate symlinks are not detected. The root
// itself, relative paths and paths outside the root are invalid input.
func (p *Policy) MatchPath(abs string, isDir bool) (PolicyDecision, error) {
	rel, err := p.absToRel(abs)
	if err != nil {
		return PolicyDecision{}, err
	}
	return p.Match(rel, isDir)
}

// absToRel converts an absolute path to a clean root-relative slash path.
func (p *Policy) absToRel(abs string) (string, error) {
	if !filepath.IsAbs(abs) {
		return "", fmt.Errorf("path %q: %w", abs, ErrInvalidInput)
	}
	rel, err := filepath.Rel(p.root, filepath.Clean(abs))
	if err != nil {
		return "", fmt.Errorf("path %q: %w", abs, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path %q: %w outside policy root", abs, ErrInvalidInput)
	}
	if err := validatePath(rel, false); err != nil {
		return "", fmt.Errorf("path %q: %w", abs, err)
	}
	return rel, nil
}

// Diagnostics returns compilation diagnostics for every source discovered so
// far, sorted by source label, line and kind. Because discovery is lazy, the
// set grows as further directories are first evaluated.
func (p *Policy) Diagnostics() []Diagnostic {
	p.mu.Lock()
	defer p.mu.Unlock()
	dirs := make([]string, 0, len(p.dirs))
	for dir := range p.dirs {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)
	var out []Diagnostic
	for _, dir := range dirs {
		out = append(out, p.dirs[dir].diagnostics...)
	}
	sortDiagnostics(out)
	return out
}

// matcherFor returns the cached cumulative policy of dir, loading it and its
// ancestors on first use. dir is a clean root-relative slash directory ("" is
// the root). Loads and compilation happen under the lock; compiled matchers
// are immutable and shared afterwards.
func (p *Policy) matcherFor(dir string) (*dirPolicy, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dirLocked(dir)
}

// dirLocked builds or fetches the cumulative policy of one directory. The
// caller holds p.mu.
func (p *Policy) dirLocked(dir string) (*dirPolicy, error) {
	if p.dirs == nil {
		p.dirs = make(map[string]*dirPolicy)
	}
	if state, ok := p.dirs[dir]; ok {
		return state, state.err
	}
	sources := []Source{}
	if dir != "" {
		at := strings.LastIndexByte(dir, '/')
		parentDir := ""
		if at >= 0 {
			parentDir = dir[:at]
		}
		parent, err := p.dirLocked(parentDir)
		if err != nil {
			state := &dirPolicy{err: err}
			p.dirs[dir] = state
			return state, state.err
		}
		sources = slices.Clone(parent.sources)
	}
	for _, spec := range p.specs {
		if dir != "" && !spec.Nested {
			continue
		}
		full := filepath.Join(p.root, filepath.FromSlash(dir), filepath.FromSlash(spec.Filename))
		data, err := os.ReadFile(full)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && spec.Optional {
				continue
			}
			err = fmt.Errorf("read %s: %w", sourceLabel(dir, spec.Filename), err)
			state := &dirPolicy{err: err}
			p.dirs[dir] = state
			return state, state.err
		}
		sources = append(sources, Source{
			Label: sourceLabel(dir, spec.Filename),
			Scope: dir,
			Rank:  spec.Rank,
			Data:  data,
		})
	}
	matcher, diagnostics, err := Compile(sources, p.opts)
	if err != nil {
		err = fmt.Errorf("compile %s: %w", dir, err)
		state := &dirPolicy{err: err}
		p.dirs[dir] = state
		return state, state.err
	}
	state := &dirPolicy{sources: sources, matcher: matcher, diagnostics: diagnostics}
	p.dirs[dir] = state
	return state, nil
}

// sourceLabel identifies a source file root-relatively, so winning rules and
// diagnostics name unambiguous files such as ".gitignore" or "a/b/.gitignore".
func sourceLabel(dir, filename string) string {
	if dir == "" {
		return filename
	}
	return dir + "/" + filename
}
