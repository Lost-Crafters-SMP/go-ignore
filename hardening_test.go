// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// Filesystem hardening: disappearing sources, special source types, deep
// and long paths, walker resilience under filesystem changes, containment
// edges and concurrency across the whole read surface.

func TestPolicyDisappearingSources(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{".custom": "*.tmp\n", "sub/.custom": "*.log\n"})
	optional := []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}}
	required := []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Nested: true}}

	// Sources are read at most once per Policy: deleting one after a load
	// leaves every decision unchanged.
	p := testPolicy(t, root, optional)
	if d := policyAction(t, p, "sub/a.log", false); d.Action != Exclude {
		t.Fatalf("before delete: %+v", d.Decision)
	}
	if err := os.Remove(filepath.Join(root, "sub", ".custom")); err != nil {
		t.Fatal(err)
	}
	if d := policyAction(t, p, "sub/a.log", false); d.Action != Exclude {
		t.Fatalf("after delete: %+v", d.Decision)
	}

	// A fresh Policy discovers the deletion as a deterministic error, and
	// recreating the file afterwards does not resurrect it: errors are cached
	// for the Policy's lifetime.
	q := testPolicy(t, root, required)
	if _, err := q.Match("sub/a.log", false); !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "sub/.custom") {
		t.Fatalf("missing source: %v", err)
	}
	writeTree(t, root, map[string]string{"sub/.custom": "*.txt\n"})
	again, err := q.Match("sub/a.log", false)
	if err == nil || again.Action != NoMatch {
		t.Fatalf("cached error changed: %+v %v", again, err)
	}
	if _, err := q.Match("sub/b.txt", false); err == nil {
		t.Fatal("recreated source loaded into errored policy")
	}

	// A third Policy sees the recreated source.
	r := testPolicy(t, root, optional)
	if d := policyAction(t, r, "sub/b.txt", false); d.Action != Exclude {
		t.Fatalf("recreated: %+v", d.Decision)
	}
}

func TestPolicySpecialSources(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"empty/.custom":  "",
		"blank/.custom":  "   \n\t\n# comment\n\n",
		"binary/.custom": "ok\n\x00broken\n",
	})
	specs := []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}}

	p := testPolicy(t, root, specs)
	if d := policyAction(t, p, "empty/f", false); d.Action != NoMatch {
		t.Fatalf("empty source: %+v", d.Decision)
	}
	if d := policyAction(t, p, "blank/f", false); d.Action != NoMatch {
		t.Fatalf("blank source: %+v", d.Decision)
	}
	if diags := p.Diagnostics(); len(diags) != 0 {
		t.Fatalf("empty/blank diagnostics: %+v", diags)
	}

	// A NUL byte inside a source file is a compile error, cached
	// deterministically for the Policy's lifetime.
	q := testPolicy(t, root, specs)
	first, err := q.Match("binary/ok", false)
	if err == nil || first.Action != NoMatch {
		t.Fatalf("binary source: %+v %v", first, err)
	}
	second, err2 := q.Match("binary/ok", false)
	if err2 == nil || err2.Error() != err.Error() || second.Action != NoMatch {
		t.Fatalf("unstable compile error: %v then %v", err, err2)
	}

	// A source reached through a symlink is read normally: discovery follows
	// symlinks for source files, while traversal never follows them.
	symroot := t.TempDir()
	writeTree(t, symroot, map[string]string{"real": "*.log\n"})
	if err := os.Symlink(filepath.Join(symroot, "real"), filepath.Join(symroot, ".custom")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	sp := testPolicy(t, symroot, specs)
	if d := policyAction(t, sp, "x.log", false); d.Action != Exclude || d.Rule.Source() != ".custom" {
		t.Fatalf("symlinked source: %+v", d.Decision)
	}
}

func TestPolicyRootIsFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notadir")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, map[string]string{".custom": ""})
	p := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true}})
	// Hypothetical paths below a file root match nothing on every platform:
	// the directory can never exist, so optional sources are absent there.
	if d, err := p.Match("f", false); err != nil || d.Action != NoMatch {
		t.Fatalf("root match: %+v %v", d, err)
	}
	if d, err := p.Match("notadir/f", false); err != nil || d.Action != NoMatch {
		t.Fatalf("file root child: %+v %v", d, err)
	}
	// Required nested sources still report the failure, naming the directory.
	q := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Nested: true}})
	if _, err := q.Match("notadir/f", false); err == nil || !strings.Contains(err.Error(), "notadir") {
		t.Fatalf("required under file root: %v", err)
	}
}

func TestPolicyLongAndDeepPaths(t *testing.T) {
	root := t.TempDir()
	// A 90-level chain keeps the absolute path comfortably inside Windows
	// MAX_PATH while exercising recursive discovery, cumulative compilation
	// and deep walker descent.
	const depth = 90
	chain := strings.Repeat("d/", depth)
	writeTree(t, root, map[string]string{
		".custom":      "/top-literal\n",
		chain + "file": "",
	})
	for level := 1; level <= depth; level++ {
		writeTree(t, root, map[string]string{strings.Repeat("d/", level) + ".custom": fmt.Sprintf("deep-%d\n", level)})
	}
	p := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Nested: true}})
	deepest := chain + "file"
	d := policyAction(t, p, deepest, false)
	if d.Action != NoMatch {
		t.Fatalf("deep miss: %+v", d.Decision)
	}
	hit := chain + "deep-42"
	if d := policyAction(t, p, hit, false); d.Action != Exclude || d.Rule.Source() != strings.Repeat("d/", 42)+".custom" || d.Rule.Line() != 1 {
		t.Fatalf("deep provenance: %+v source=%q", d.Decision, d.Rule.Source())
	}
	if d := policyAction(t, p, "top-literal", false); d.Action != Exclude || d.Rule.Source() != ".custom" {
		t.Fatalf("root provenance: %+v", d.Decision)
	}
	levels := 0
	err := p.Walk("", func(_ string, _ bool, _ PolicyDecision) error {
		levels++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 90 nested dirs + 90 of their .custom files + the root source + the file.
	if levels != depth*2+2 {
		t.Fatalf("deep walk visits=%d want %d", levels, depth*2+2)
	}

	// A 255-byte component (the common filesystem limit) as rule and path.
	long := strings.Repeat("n", 255)
	writeTree(t, root, map[string]string{long: ""})
	if d := policyAction(t, p, long, false); d.Action != NoMatch {
		t.Fatalf("long component: %+v", d.Decision)
	}

	// Very long pure queries: no filesystem involvement, bounded behaviour.
	m := mustCompile(t, "/"+strings.Repeat("x", 4000)+"\n**/"+strings.Repeat("y", 2000)+"\n", Options{})
	deep := strings.Repeat("z/", 1500) + "q"
	d2, err := m.Match(deep, false)
	if err != nil || d2.Action != NoMatch {
		t.Fatalf("long query: %+v %v", d2, err)
	}
	d3, err := m.Match(strings.Repeat("x", 4000), false)
	if err != nil || d3.Action != Exclude {
		t.Fatalf("long literal: %+v %v", d3, err)
	}
}

func TestPolicyWalkerResilience(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".custom":   "tmp/\n",
		"a/":        "",
		"a/b/":      "",
		"a/b/c":     "",
		"z/":        "",
		"z/last":    "",
		"tmp/":      "",
		"tmp/inner": "",
	})
	p := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}})

	// Callback cancellation stops iteration exactly at the cancelling entry.
	count := 0
	sentinel := errors.New("stop now")
	err := p.Walk("", func(_ string, _ bool, _ PolicyDecision) error {
		count++
		if count == 3 {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) || count != 3 {
		t.Fatalf("cancellation: %v count=%d", err, count)
	}

	// A symlink pointing outside the policy root is reported as a plain
	// entry and never traversed.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "escape.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	visited := map[string]bool{}
	if err := p.Walk("", func(rel string, isDir bool, _ PolicyDecision) error {
		visited[rel] = true
		if rel == "escape" && isDir {
			t.Error("symlink traversed as directory")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !visited["escape"] || visited["escape/escape.txt"] {
		t.Fatalf("escaping symlink visits: %v", visited)
	}

	// A directory removed while being walked surfaces the ReadDir failure,
	// wrapped with the failing path.
	err = p.Walk("", func(rel string, _ bool, _ PolicyDecision) error {
		if rel == "a/b" {
			return os.RemoveAll(filepath.Join(root, "a", "b"))
		}
		return nil
	})
	if err == nil || !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "a/b") {
		t.Fatalf("mid-walk deletion: %v", err)
	}

	// Reserving a source filename reports the file itself as reserved while
	// leaving its rules effective for every other path.
	res := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}}, ".custom")
	d, err := res.Match(".custom", false)
	if err != nil || !d.Reserved || d.Action != Exclude {
		t.Fatalf("reserved source: %+v %v", d, err)
	}
	if d := policyAction(t, res, "tmp/x", false); d.Block == nil || d.Block.Directory != "tmp" {
		t.Fatalf("rules after reserved source: %+v", d.Decision)
	}
}

func TestPolicyMatchPathMatrix(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{".custom": "*.log\n"})
	p := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}})
	join := func(parts ...string) string { return filepath.Join(parts...) }

	if d, err := p.MatchPath(join(root, "sub", "x.log"), false); err != nil || d.Action != Exclude {
		t.Fatalf("nested absolute: %+v %v", d, err)
	}
	if d, err := p.MatchPath(filepath.ToSlash(join(root, "sub", "x.log")), false); err != nil || d.Action != Exclude {
		t.Fatalf("forward-slash absolute: %+v %v", d, err)
	}
	for _, bad := range []string{
		"x.log",          // relative
		"relative/x.log", // relative
		".",              // cwd
		"",               // empty
		join("..", "x"),  // escaping
		join(root) + string(filepath.Separator),
	} {
		if _, err := p.MatchPath(bad, false); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	// A sibling whose name shares the root's prefix is outside.
	sibling := join(filepath.Dir(root), filepath.Base(root)+"-sib", "x.log")
	if err := os.MkdirAll(filepath.Dir(sibling), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := p.MatchPath(sibling, false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("sibling prefix: %v", err)
	}
	// The root itself is not a candidate.
	if _, err := p.MatchPath(root, false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("root itself: %v", err)
	}
	// NUL is rejected regardless of form.
	if _, err := p.MatchPath(join(root, "a\x00b"), false); err == nil {
		t.Fatal("accepted NUL")
	}
	// Platform containment semantics: Windows filepath.Rel is
	// case-insensitive, Unix is case-sensitive. Both are lexical.
	if runtime.GOOS == "windows" {
		var b strings.Builder
		b.WriteString(strings.ToLower(root[:3]) + strings.ToUpper(root[3:3+len(root)-3]))
		if d, err := p.MatchPath(join(b.String(), "x.log"), false); err != nil || d.Action != Exclude {
			t.Fatalf("case-varied absolute: %+v %v", d, err)
		}
	} else {
		if _, err := p.MatchPath(strings.ToUpper(root), false); err == nil {
			t.Fatal("accepted case-varied root")
		}
	}
}

func TestPolicyConcurrentSurface(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{".custom": "*.log\n!keep.log\ntmp/\n"}
	for i := range 8 {
		dir := fmt.Sprintf("d%d", i)
		files[dir+"/"] = ""
		files[dir+"/.custom"] = fmt.Sprintf("local%d\n", i)
		files[dir+"/x.log"] = ""
		files[dir+"/keep.log"] = ""
	}
	writeTree(t, root, files)
	p := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Nested: true}}, "tmp")

	var wg sync.WaitGroup
	for w := range 6 {
		wg.Go(func() {
			for i := range 25 {
				dir := fmt.Sprintf("d%d", i%8)
				d, err := p.Match(dir+"/x.log", false)
				if err != nil || d.Action != Exclude {
					t.Errorf("x.log: %+v %v", d, err)
					return
				}
				if _, err := p.Match(dir+"/local", false); err != nil {
					t.Errorf("local: %v", err)
					return
				}
				if w%2 == 0 {
					if err := p.Walk(dir, func(string, bool, PolicyDecision) error { return nil }); err != nil {
						t.Error(err)
						return
					}
				}
				if w%3 == 0 && len(p.Diagnostics()) != 0 {
					t.Error("unexpected diagnostics")
					return
				}
				if p.Root() == "" {
					t.Error("empty root")
					return
				}
			}
		})
	}
	wg.Wait()
}
