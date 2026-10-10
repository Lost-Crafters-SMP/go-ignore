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
	"testing"
)

// descendEntry is one enumerated filesystem entry in the parity test.
type descendEntry struct {
	rel   string
	isDir bool
}

// assertSameDecision compares two decisions of one matcher, including
// winning-rule identity and block provenance.
func assertSameDecision(t *testing.T, got, want Decision) {
	t.Helper()
	if got.Action != want.Action {
		t.Fatalf("action %d want %d", got.Action, want.Action)
	}
	if got.Rule != want.Rule {
		t.Fatalf("rule %+v want %+v", got.Rule, want.Rule)
	}
	if (got.Block == nil) != (want.Block == nil) {
		t.Fatalf("block %+v want %+v", got.Block, want.Block)
	}
	if got.Block != nil {
		if got.Block.Directory != want.Block.Directory || got.Block.Rule != want.Block.Rule {
			t.Fatalf("block %+v want %+v", got.Block, want.Block)
		}
	}
}

// TestMatcherDescend pins the directory-descent verdict of precompiled
// rules: false exactly for blocking strict ancestors and the directory's
// own bare-directory self-exclusion, never for a canonical slash-form
// contents exclusion alone. Every case also asserts the returned decision
// is exactly Match(path, true).
func TestMatcherDescend(t *testing.T) {
	rules := func(text string) []Source {
		return []Source{{Label: "rules", Rank: RankPerDirectory, Data: []byte(text)}}
	}
	cases := []struct {
		name    string
		sources []Source
		path    string
		action  Action
		line    int    // winning endpoint or block rule line, 0 for none
		block   string // blocking ancestor, "" for none
		descend bool
	}{
		{
			name:    "glob-contents-with-reinclude",
			sources: rules("/cache/*\n!/cache/keep.txt\n"),
			path:    "cache", action: Exclude, line: 1, descend: true,
		},
		{
			name:    "glob-contents-without-negation",
			sources: rules("/cache/*\n"),
			path:    "cache", action: Exclude, line: 1, descend: true,
		},
		{
			name:    "dir-only-exclusion-blocks",
			sources: rules("/cache/\n!/cache/keep.txt\n"),
			path:    "cache", action: Exclude, line: 1, descend: false,
		},
		{
			name:    "dir-only-exclusion-blocks-descendant",
			sources: rules("/cache/\n!/cache/keep.txt\n"),
			path:    "cache/keep.txt", action: NoMatch, line: 1,
			block: "cache", descend: false,
		},
		{
			name:    "bare-name-exclusion-blocks",
			sources: rules("cache\n!cache/keep.txt\n"),
			path:    "cache", action: Exclude, line: 1, descend: false,
		},
		{
			name:    "negated-parent-stays-traversable",
			sources: rules("!cache\ncache/*\n!cache/keep.txt\n"),
			path:    "cache", action: Exclude, line: 2, descend: true,
		},
		{
			name:    "dir-exclusion-reopened-by-negation",
			sources: rules("cache/\n!cache/\n"),
			path:    "cache", action: NoMatch, descend: true,
		},
		{
			name:    "doublestar-contents-exclusion",
			sources: rules("/cache/**\n!/cache/sub/keep.txt\n"),
			path:    "cache", action: Exclude, line: 1, descend: true,
		},
		{
			name:    "doublestar-blocks-nested-subdirectory",
			sources: rules("/cache/**\n!/cache/sub/keep.txt\n"),
			path:    "cache/sub", action: Exclude, line: 1, descend: false,
		},
		{
			name:    "doublestar-blocked-descendant",
			sources: rules("/cache/**\n!/cache/sub/keep.txt\n"),
			path:    "cache/sub/keep.txt", action: NoMatch, line: 1,
			block: "cache/sub", descend: false,
		},
		{
			name:    "reopened-subdirectory-under-glob",
			sources: rules("/cache/*\n!/cache/sub/\n!/cache/sub/deep/keep.txt\n"),
			path:    "cache/sub", action: NoMatch, descend: true,
		},
		{
			name:    "reopened-deeper-directory",
			sources: rules("/cache/*\n!/cache/sub/\n!/cache/sub/deep/keep.txt\n"),
			path:    "cache/sub/deep", action: NoMatch, descend: true,
		},
		{
			name: "nested-scope-reopens-parent-exclusion",
			sources: []Source{
				{Label: "root", Data: []byte("out/\n")},
				{Label: "a", Scope: "a", Data: []byte("!out/\n")},
			},
			path: "a/out", action: NoMatch, descend: true,
		},
		{
			name: "nested-scope-reexcludes-deeper",
			sources: []Source{
				{Label: "root", Data: []byte("out/\n")},
				{Label: "a", Scope: "a", Data: []byte("!out/\n")},
				{Label: "a/b", Scope: "a/b", Data: []byte("out/\n")},
			},
			path: "a/b/out", action: Exclude, line: 1, descend: false,
		},
		{
			name: "rank-outranks-depth-on-descent",
			sources: []Source{
				{Label: "root", Rank: RankPerDirectory, Data: []byte("out/\n")},
				{Label: "a", Scope: "a", Rank: RankPerDirectory, Data: []byte("!out/\n")},
				{Label: "a/b", Scope: "a/b", Rank: RankPerDirectory, Data: []byte("out/\n")},
				{Label: "cmd", Rank: RankCommand, Data: []byte("!out/\n")},
			},
			path: "a/b/out", action: NoMatch, descend: true,
		},
		{
			name:    "ancestor-block-provenance",
			sources: rules("deep/\n"),
			path:    "deep", action: Exclude, line: 1, descend: false,
		},
		{
			name:    "ancestor-block-deep-descendant",
			sources: rules("deep/\n"),
			path:    "deep/a/b", action: NoMatch, line: 1,
			block: "deep", descend: false,
		},
		{
			name: "source-sequence-overlap-later-wins",
			sources: []Source{
				{Label: "first", Data: []byte("cache/\n")},
				{Label: "second", Data: []byte("!cache/\n")},
			},
			path: "cache", action: NoMatch, descend: true,
		},
		{
			name: "source-sequence-overlap-earlier-loses",
			sources: []Source{
				{Label: "second", Data: []byte("!cache/\n")},
				{Label: "first", Data: []byte("cache/\n")},
			},
			path: "cache", action: Exclude, line: 1, descend: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _, err := Compile(c.sources, Options{})
			if err != nil {
				t.Fatal(err)
			}
			want, err := m.Match(c.path, true)
			if err != nil {
				t.Fatal(err)
			}
			d, descend, err := m.Descend(c.path)
			if err != nil {
				t.Fatal(err)
			}
			assertSameDecision(t, d, want)
			if descend != c.descend {
				t.Fatalf("descend %v want %v (%+v)", descend, c.descend, d)
			}
			if d.Action != c.action {
				t.Fatalf("action %d want %d", d.Action, c.action)
			}
			if c.block == "" {
				if d.Block != nil {
					t.Fatalf("unexpected block %+v", d.Block)
				}
			} else if d.Block == nil || d.Block.Directory != c.block || d.Block.Rule == nil || d.Block.Rule.Line() != c.line {
				t.Fatalf("block %+v want ancestor %q line %d", d.Block, c.block, c.line)
			}
			if c.block == "" && c.line != 0 {
				if d.Rule == nil || d.Rule.Line() != c.line {
					t.Fatalf("rule %+v want line %d", d.Rule, c.line)
				}
			}
			if c.block == "" && c.line == 0 && d.Rule != nil {
				t.Fatalf("unexpected rule %+v", d.Rule)
			}
		})
	}
}

// TestMatcherDescendContract checks input handling: the traversal root is
// not a valid query (callers evaluate roots themselves), invalid paths and
// nil matchers are rejected, and a zero-value matcher descends everywhere.
func TestMatcherDescendContract(t *testing.T) {
	var nilMatcher *Matcher
	if _, _, err := nilMatcher.Descend("a"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil matcher: %v", err)
	}
	for _, path := range []string{"", ".", "..", "/a", "a/", "a//b", "a/./b", "a\x00b"} {
		if _, _, err := new(Matcher).Descend(path); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("path %q: %v", path, err)
		}
	}
	for _, path := range []string{"a", "a/b", "a/b/c"} {
		d, descend, err := new(Matcher).Descend(path)
		if err != nil || !descend || d.Action != NoMatch || d.Rule != nil || d.Block != nil {
			t.Fatalf("zero matcher %q: %+v %v %v", path, d, descend, err)
		}
	}
}

// TestMatcherDescendMatchesWalker checks the precompiled-rules shape
// against the policy walker on one tree: for every directory, Descend's
// verdict equals the walker's shared pruning predicate, the walker visits
// a directory exactly when every ancestor permits descent, and no
// descendant of a non-descending directory is visited.
func TestMatcherDescendMatchesWalker(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":         "/cache/*\n!/cache/keep.txt\ntmp/\n!deny\ndeny/*\n!deny/keep.txt\n/span/**\n!/span/keep\nout/\n",
		"a/.gitignore":       "!out/\n",
		"a/b/.gitignore":     "out/\n",
		"a/top":              "",
		"a/out/f":            "",
		"a/b/out/f":          "",
		"cache/keep.txt":     "",
		"cache/drop.txt":     "",
		"cache/sub/keep.txt": "",
		"cache/sub/drop.txt": "",
		"deny/keep.txt":      "",
		"deny/drop.txt":      "",
		"tmp/x":              "",
		"span/keep":          "",
		"span/sub/g":         "",
		"out/deep/f":         "",
		"plain":              "",
	})
	p := testPolicy(t, root, []SourceSpec{{Filename: ".gitignore", Rank: RankPerDirectory, Optional: true, Nested: true}})

	// The same sources precompiled in one matcher: the root source first,
	// then each nested source scoped to its directory. Ranks match the
	// policy's specs so both sides order rules identically.
	mustData := func(rel string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	m, _, err := Compile([]Source{
		{Label: ".gitignore", Rank: RankPerDirectory, Data: mustData(".gitignore")},
		{Label: "a/.gitignore", Scope: "a", Rank: RankPerDirectory, Data: mustData("a/.gitignore")},
		{Label: "a/b/.gitignore", Scope: "a/b", Rank: RankPerDirectory, Data: mustData("a/b/.gitignore")},
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	var all []descendEntry
	descendVerdict := map[string]bool{} // directory -> matcher descent verdict
	visited := map[string]bool{}        // every walker visit, files included
	if got, err := collectWalk(t, p, ""); err != nil {
		t.Fatal(err)
	} else {
		for _, rec := range got {
			visited[rec.rel] = true
		}
	}
	err = filepath.WalkDir(root, func(full string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if full == root {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		all = append(all, descendEntry{filepath.ToSlash(rel), e.IsDir()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(visited) == 0 {
		t.Fatal("walker visited nothing")
	}

	// WalkDir yields parents before children, so each directory's verdict
	// is known before its descendants are checked.
	for _, e := range all {
		if !e.isDir {
			continue
		}
		d, descend, err := m.Descend(e.rel)
		if err != nil {
			t.Fatal(err)
		}
		descendVerdict[e.rel] = descend
		// The walker's own predicate, from the policy's shared evaluation.
		pd, bare, err := p.decide(e.rel, true)
		if err != nil {
			t.Fatal(err)
		}
		if blocks := blocksDescent(pd, bare); blocks == descend {
			t.Fatalf("directory %q: matcher descend %v, walker prune %v (%+v vs %+v)", e.rel, descend, blocks, d, pd)
		}
		// Decisions agree beyond the verdict, including provenance.
		if d.Action != pd.Action || (d.Rule == nil) != (pd.Rule == nil) ||
			(d.Rule != nil && (d.Rule.Source() != pd.Rule.Source() || d.Rule.Line() != pd.Rule.Line())) {
			t.Fatalf("directory %q: matcher %+v policy %+v", e.rel, d, pd)
		}
		// Reachability: the walker enters a directory exactly when every
		// strict ancestor permits descent. Non-descending directories are
		// still reported (then pruned); only their descendants disappear.
		ancestorsDescend := true
		rest := e.rel
		for {
			at := strings.LastIndexByte(rest, '/')
			if at < 0 {
				break
			}
			if ok := descendVerdict[rest[:at]]; !ok {
				ancestorsDescend = false
				break
			}
			rest = rest[:at]
		}
		if visited[e.rel] != ancestorsDescend {
			t.Fatalf("directory %q visited %v ancestors-descend %v", e.rel, visited[e.rel], ancestorsDescend)
		}
		if visited[e.rel] && !descend {
			// Reported and pruned: no entry below it may be visited.
			for other := range visited {
				if strings.HasPrefix(other, e.rel+"/") {
					t.Fatalf("pruned %q but visited descendant %q", e.rel, other)
				}
			}
		}
	}

	blockedDirs := make([]string, 0, len(descendVerdict))
	visitedDirs, totalDirs := 0, 0
	for dir, descend := range descendVerdict {
		totalDirs++
		if !descend {
			blockedDirs = append(blockedDirs, dir)
		}
		if visited[dir] {
			visitedDirs++
		}
	}
	slices.Sort(blockedDirs)
	// Git-arbitrated expectations: /cache/* and /span/** prune the bare
	// direct children cache/sub and span/sub; the nested a/b source
	// re-excludes a/b/out; out/ and tmp/ prune themselves; out/deep is
	// blocked only because its ancestor out was pruned.
	want := []string{"a/b/out", "cache/sub", "out", "out/deep", "span/sub", "tmp"}
	if fmt.Sprint(blockedDirs) != fmt.Sprint(want) {
		t.Fatalf("non-descending directories %v want %v", blockedDirs, want)
	}
	if visitedDirs != totalDirs-1 {
		t.Fatalf("visited %d directories want %d (only out/deep is unreachable)", visitedDirs, totalDirs-1)
	}
}
