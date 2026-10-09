// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeTree materialises a map of slash-relative paths; a trailing slash
// marks a directory, anything else a regular file with the given content.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if strings.HasSuffix(path, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func testPolicy(t *testing.T, root string, specs []SourceSpec, reserved ...string) *Policy {
	t.Helper()
	p, err := NewPolicy(PolicyOptions{Root: root, Sources: specs, Reserved: reserved})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func policyAction(t *testing.T, p *Policy, path string, isDir bool) PolicyDecision {
	t.Helper()
	d, err := p.Match(path, isDir)
	if err != nil {
		t.Fatalf("match %q: %v", path, err)
	}
	return d
}

func TestPolicyConstruction(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "rel", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTree(t, base, map[string]string{"rel/.custom": "*.log\n", "rel/sub/f": ""})
	specs := []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}}

	for _, c := range []struct {
		name string
		root string
	}{
		{"empty", ""},
	} {
		if _, err := NewPolicy(PolicyOptions{Root: c.root}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s root: %v", c.name, err)
		}
	}
	if _, err := NewPolicy(PolicyOptions{Root: base}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("no sources accepted")
	}
	for _, c := range []struct {
		spec   SourceSpec
		reason string
	}{
		{SourceSpec{Filename: "", Nested: true}, "empty filename"},
		{SourceSpec{Filename: "/abs", Nested: true}, "absolute filename"},
		{SourceSpec{Filename: "a//b"}, "empty part"},
		{SourceSpec{Filename: "a/../b"}, "parent part"},
		{SourceSpec{Filename: "a/./b"}, "dot part"},
		{SourceSpec{Filename: "a\x00b"}, "NUL"},
		{SourceSpec{Filename: "info/exclude", Nested: true}, "nested subpath"},
	} {
		if _, err := NewPolicy(PolicyOptions{Root: base, Sources: []SourceSpec{c.spec}}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s accepted: %v", c.reason, err)
		}
	}
	for _, reserved := range []string{"..", "a/../b", "/abs", "", ".", "a//b", "a\x00b", "a/"} {
		if _, err := NewPolicy(PolicyOptions{Root: base, Sources: specs, Reserved: []string{reserved}}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("reserved %q accepted: %v", reserved, err)
		}
	}

	// A relative root resolves once, against the construction working
	// directory; later matches never consult the process CWD.
	t.Chdir(base)
	p := testPolicy(t, "rel", specs)
	if want, err := filepath.Abs("rel"); err != nil || p.Root() != filepath.Clean(want) {
		t.Fatalf("root %q want %q (err %v)", p.Root(), want, err)
	}
	t.Chdir(filepath.Join(base, "rel", "sub"))
	if d := policyAction(t, p, "sub/f", false); d.Action != NoMatch {
		t.Fatalf("action %d", d.Action)
	}
	if d := policyAction(t, p, "sub/x.log", false); d.Action != Exclude || d.Rule.Source() != ".custom" {
		t.Fatalf("decision %+v", d.Decision)
	}
}

func TestPolicyRankAndDepth(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".custom":     "!keep.log\n",
		"sub/.custom": "*.log\n",
		".root-only":  "only.txt\n",
	})
	specs := []SourceSpec{
		{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true},
		{Filename: ".root-only", Rank: RankGlobal, Optional: true},
	}
	p := testPolicy(t, root, specs)

	// Same rank, different depth: the nested exclusion and the root negation
	// both come from the rank-300 spec, so depth decides and the deeper
	// exclusion wins, as in Git.
	if d := policyAction(t, p, "sub/keep.log", false); d.Action != Exclude || d.Rule.Source() != "sub/.custom" {
		t.Fatalf("same rank deeper exclusion: %+v", d.Decision)
	}

	// A lower-ranked source never overrides a higher rank at any depth, but
	// still applies when no higher-ranked rule matches.
	writeTree(t, root, map[string]string{"sub/.low": "*.log\nplain.txt\n"})
	specs = append(specs, SourceSpec{Filename: ".low", Rank: RankInfoExclude, Optional: true, Nested: true})
	p = testPolicy(t, root, specs)
	if d := policyAction(t, p, "sub/keep.log", false); d.Action != Exclude || d.Rule.Source() != "sub/.custom" {
		t.Fatalf("deeper same-rank exclusion beats root negation: %+v source %q", d.Decision, d.Rule.Source())
	}
	if d := policyAction(t, p, "sub/plain.txt", false); d.Action != Exclude || d.Rule.Rank() != RankInfoExclude {
		t.Fatalf("lower-ranked rule applies: %+v", d.Decision)
	}

	// A higher-ranked source overrides deeper lower-ranked rules.
	writeTree(t, root, map[string]string{"sub/.high": "!keep.log\n"})
	specs = append(specs, SourceSpec{Filename: ".high", Rank: RankCommand, Optional: true, Nested: true})
	p = testPolicy(t, root, specs)
	if d := policyAction(t, p, "sub/keep.log", false); d.Action != Include || d.Rule.Rank() != RankCommand {
		t.Fatalf("command negation: %+v rule rank %d", d.Decision, d.Rule.Rank())
	}
	if d := policyAction(t, p, "sub/other.log", false); d.Action != Exclude || d.Rule.Source() != "sub/.custom" {
		t.Fatalf("nested exclusion: %+v source %q", d.Decision, d.Rule.Source())
	}

	// Root-only sources apply everywhere (scope "") but are not discovered
	// in subdirectories.
	if d := policyAction(t, p, "only.txt", false); d.Action != Exclude || d.Rule.Source() != ".root-only" {
		t.Fatalf("root-only rule: %+v", d.Decision)
	}
	writeTree(t, root, map[string]string{"sub/.root-only": "!only.txt\n"})
	p = testPolicy(t, root, specs)
	if d := policyAction(t, p, "sub/only.txt", false); d.Action != Exclude || d.Rule.Source() != ".root-only" {
		t.Fatalf("nested root-only file must not load: %+v source %q", d.Decision, d.Rule.Source())
	}

	// Spec order breaks equal (rank, depth) ties: later definitions win.
	root = t.TempDir()
	writeTree(t, root, map[string]string{"first": "x\n", "second": "!x\n"})
	p = testPolicy(t, root, []SourceSpec{
		{Filename: "first", Rank: 350, Optional: true, Nested: true},
		{Filename: "second", Rank: 350, Optional: true, Nested: true},
	})
	if d := policyAction(t, p, "x", false); d.Action != Include || d.Rule.Source() != "second" {
		t.Fatalf("spec order: %+v source %q", d.Decision, d.Rule.Source())
	}
}

func TestPolicyNestedBlocking(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":     "build/\n*.log\n!keep.log\n",
		"build/.ignore":  "!keep\n",
		"nested/.custom": "temp/\n",
	})
	p := testPolicy(t, root, []SourceSpec{
		{Filename: ".gitignore", Rank: RankPerDirectory, Optional: true, Nested: true},
		{Filename: ".ignore", Rank: RankCommand, Optional: true, Nested: true},
		{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true},
	})

	if d := policyAction(t, p, "build/out.o", false); d.Block == nil || d.Block.Directory != "build" || d.Block.Rule.Source() != ".gitignore" {
		t.Fatalf("blocked: %+v", d.Decision)
	}
	// Command-rank negation cannot reopen a blocked directory either.
	if d := policyAction(t, p, "build/keep", false); d.Block == nil || !d.Ignored() {
		t.Fatalf("negation reopened block: %+v", d.Decision)
	}
	if d := policyAction(t, p, "a/keep.log", false); d.Action != Include {
		t.Fatalf("negation outside block: %+v", d.Decision)
	}
	if d := policyAction(t, p, "nested/temp/x", false); d.Block == nil || d.Block.Directory != "nested/temp" {
		t.Fatalf("nested block: %+v", d.Decision)
	}
}

func TestPolicyReserved(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".custom":       "!.git\n!secret\n",
		".git/":         "",
		".git/config":   "",
		"x/.git/":       "",
		"x/.git/config": "",
		"secret/keys":   "",
	})
	p := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}},
		".git", "secret/keys")

	for _, c := range []struct {
		path     string
		dir      bool
		reserved bool
	}{
		{".git", true, true},
		{".git/config", false, true},
		{".git/deep/nested", false, true},
		{"secret/keys", false, true},
		{"x/.git", true, false},
		{"x/.git/config", false, false},
		{"secret/other", false, false},
		{"git", false, false},
	} {
		d := policyAction(t, p, c.path, c.dir)
		if d.Reserved != c.reserved {
			t.Fatalf("%q reserved=%v want %v", c.path, d.Reserved, c.reserved)
		}
		if d.Reserved {
			if d.Action != Exclude || d.Rule != nil || d.Block != nil || !d.Ignored() {
				t.Fatalf("%q reserved decision %+v", c.path, d)
			}
		}
	}
	// The root negations cannot override reserved paths.
	if d := policyAction(t, p, ".git", true); !d.Reserved || d.Action != Exclude {
		t.Fatalf("negation overrode reserved: %+v", d)
	}
	if d := policyAction(t, p, "secret/keys", false); !d.Reserved || d.Action != Exclude {
		t.Fatalf("negation overrode reserved exact: %+v", d)
	}
	// The same negations do not leak to similarly named paths elsewhere.
	if d := policyAction(t, p, "git", false); d.Reserved || d.Action != NoMatch {
		t.Fatalf("similarly named path: %+v", d)
	}
}

func TestPolicySnapshotCaching(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"sub/.custom": "*.log\n"})
	p := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}})
	if d := policyAction(t, p, "sub/a.log", false); d.Action != Exclude {
		t.Fatalf("first: %+v", d.Decision)
	}
	// Source files are read at most once per Policy: rewriting them must not
	// change decisions of the same Policy.
	writeTree(t, root, map[string]string{"sub/.custom": "*.txt\n"})
	if d := policyAction(t, p, "sub/a.log", false); d.Action != Exclude {
		t.Fatalf("cached: %+v", d.Decision)
	}
	if d := policyAction(t, p, "sub/a.txt", false); d.Action != NoMatch {
		t.Fatalf("cached new file: %+v", d.Decision)
	}
	// A new Policy observes the change.
	p2 := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}})
	if d := policyAction(t, p2, "sub/a.txt", false); d.Action != Exclude {
		t.Fatalf("fresh: %+v", d.Decision)
	}
	if d := policyAction(t, p2, "sub/a.log", false); d.Action != NoMatch {
		t.Fatalf("fresh old file: %+v", d.Decision)
	}
}

func TestPolicyErrors(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".optional": "*.log\n",
		"blocker/":  "", // a directory where a source file should be
	})
	required := []SourceSpec{{Filename: ".required", Rank: RankPerDirectory, Nested: true}}
	optional := []SourceSpec{{Filename: ".optional", Rank: RankPerDirectory, Optional: true, Nested: true}}

	// Absent required source: error, deterministic across repeats.
	p := testPolicy(t, root, required)
	_, err := p.Match("a/f", false)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("absent required: %v", err)
	}
	again, err2 := p.Match("a/f", false)
	if err2 == nil || again.Decision != (Decision{}) || err2.Error() != err.Error() {
		t.Fatalf("unstable error: %v then %v", err, err2)
	}
	// The failure also covers descendants deterministically.
	if _, errDescendant := p.Match("a/b/f", false); errDescendant == nil || errDescendant.Error() != err.Error() {
		t.Fatalf("descendant error: %v", errDescendant)
	}

	// Absent optional source is fine; readable optional source loads.
	p = testPolicy(t, root, optional)
	if d := policyAction(t, p, "deep/x.log", false); d.Action != Exclude || d.Rule.Source() != ".optional" {
		t.Fatalf("optional: %+v", d.Decision)
	}

	// A source that exists but cannot be read (here: a directory) is an
	// error even when optional.
	p = testPolicy(t, root, []SourceSpec{{Filename: "blocker", Rank: RankPerDirectory, Optional: true, Nested: true}})
	if _, err := p.Match("f", false); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unreadable optional: %v", err)
	}
	if _, err := p.Match("sub/f", false); err == nil {
		t.Fatal("nested unreadable accepted")
	}

	// Invalid paths are rejected up front.
	for _, path := range []string{"", "/a", "a//b", ".", "..", "a/..", "a\x00b", "a/."} {
		if _, err := p.Match(path, false); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("path %q: %v", path, err)
		}
	}
}

func TestPolicyMatchPath(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{".custom": "*.log\n"})
	p := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}})

	full := filepath.Join(root, "sub", "x.log")
	if d, err := p.MatchPath(full, false); err != nil || d.Action != Exclude {
		t.Fatalf("absolute inside: %+v %v", d, err)
	}
	rel, err := p.MatchPath(filepath.Join(p.Root(), "sub", "x.log"), false)
	if err != nil || rel.Action != Exclude || rel.Rule.Source() != ".custom" {
		t.Fatalf("root-based: %+v %v", rel, err)
	}
	outside := filepath.Join(filepath.Dir(root), "x.log")
	if _, err := p.MatchPath(outside, false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("outside: %v", err)
	}
	for _, bad := range []string{"relative/x.log", "", "..", root + string(filepath.Separator) + ".." + string(filepath.Separator) + "x"} {
		if _, err := p.MatchPath(bad, false); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("path %q: %v", bad, err)
		}
	}
	if _, err := p.MatchPath(root, false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("root itself: %v", err)
	}
}

func TestPolicyDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"sub/.custom": "[unclosed\nok\n",
		"tiny":        "x\n",
	})
	p := testPolicy(t, root, []SourceSpec{
		{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true},
		{Filename: "tiny", Rank: RankPerDirectory, Optional: true, Nested: true},
	})
	if got := p.Diagnostics(); len(got) != 0 {
		t.Fatalf("lazy diagnostics before use: %+v", got)
	}
	policyAction(t, p, "sub/ok", false)
	diags := p.Diagnostics()
	if len(diags) != 1 {
		t.Fatalf("diagnostics: %+v", diags)
	}
	if diags[0].Source != "sub/.custom" || diags[0].Line != 1 || diags[0].Kind != MalformedPattern {
		t.Fatalf("diagnostic: %+v", diags[0])
	}
	// The malformed line is inert; the valid rule below it still matches.
	if d := policyAction(t, p, "sub/ok", false); d.Action != Exclude || d.Rule.Line() != 2 {
		t.Fatalf("valid rule disturbed: %+v", d.Decision)
	}

	// Oversize sources are skipped wholesale with a diagnostic.
	small := PolicyOptions{
		Root:    root,
		Sources: []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}},
		Options: Options{MaxSourceBytes: 4},
	}
	p, err := NewPolicy(small)
	if err != nil {
		t.Fatal(err)
	}
	if d := policyAction(t, p, "sub/ok", false); d.Action != NoMatch {
		t.Fatalf("oversize rules applied: %+v", d.Decision)
	}
	diags = p.Diagnostics()
	if len(diags) != 1 || diags[0].Kind != OversizeSource || diags[0].Source != "sub/.custom" {
		t.Fatalf("oversize diagnostic: %+v", diags)
	}
}

// walkRecord captures one walker visit.
type walkRecord struct {
	rel   string
	isDir bool
	d     PolicyDecision
}

func collectWalk(t *testing.T, p *Policy, traverse string) ([]walkRecord, error) {
	t.Helper()
	var got []walkRecord
	err := p.Walk(traverse, func(rel string, isDir bool, d PolicyDecision) error {
		got = append(got, walkRecord{rel, isDir, d})
		return nil
	})
	return got, err
}

func TestPolicyWalk(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":       "build/\n*.log\n!keep.log\nreal/\n",
		"keep.log":         "",
		".git/":            "",
		".git/config":      "",
		"build/":           "",
		"build/skip.txt":   "",
		"build/.gitignore": "!skip.txt\n",
		"src/main.go":      "",
		"src/util.log":     "",
		"src/keep.log":     "",
		"docs/guide.md":    "",
	})
	p := testPolicy(t, root, []SourceSpec{{Filename: ".gitignore", Rank: RankPerDirectory, Optional: true, Nested: true}}, ".git")

	got, err := collectWalk(t, p, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]PolicyDecision{}
	for _, rec := range got {
		seen[rec.rel] = rec.d
	}
	// Pruned subtrees deliver only their roots; the root itself is absent;
	// ignore sources are ordinary candidates.
	wantOrder := []string{".git", ".gitignore", "build", "docs", "docs/guide.md", "keep.log", "src", "src/keep.log", "src/main.go", "src/util.log"}
	if len(got) != len(wantOrder) {
		t.Fatalf("visits %d want %d: %+v", len(got), len(wantOrder), keysOf(seen))
	}
	for i, rel := range wantOrder {
		if got[i].rel != rel {
			t.Fatalf("order[%d] %q want %q (%+v)", i, got[i].rel, rel, got)
		}
	}
	if !seen[".git"].Reserved || !seen[".git"].Ignored() {
		t.Fatalf("reserved walk decision %+v", seen[".git"])
	}
	if seen["build"].Action != Exclude || seen["build"].Rule.Source() != ".gitignore" {
		t.Fatalf("pruned build decision %+v", seen["build"])
	}
	if seen["src/keep.log"].Action != Include {
		t.Fatalf("negation %+v", seen["src/keep.log"])
	}
	if seen["src/util.log"].Action != Exclude {
		t.Fatalf("exclusion %+v", seen["src/util.log"])
	}
	if seen["src/main.go"].Action != NoMatch || seen[".gitignore"].Action != NoMatch {
		t.Fatalf("source file as candidate %+v %+v", seen["src/main.go"], seen[".gitignore"])
	}

	// Deterministic repeat.
	again, err := collectWalk(t, p, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(got) {
		t.Fatal("unstable walk length")
	}
	for i := range got {
		if again[i] != got[i] || again[i].d != got[i].d {
			t.Fatalf("walk[%d] unstable: %+v vs %+v", i, got[i], again[i])
		}
	}
}

func keysOf(m map[string]PolicyDecision) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// walkAll enumerates every descendant of root without pruning, computing
// decisions through Match — the enumerator differential for the walker.
func walkAll(t *testing.T, p *Policy, traverse string) []walkRecord {
	t.Helper()
	var out []walkRecord
	start := filepath.Join(p.Root(), filepath.FromSlash(traverse))
	err := filepath.WalkDir(start, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if full == start {
			return nil
		}
		rel, err := filepath.Rel(p.Root(), full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		d, err := p.Match(rel, entry.IsDir())
		if err != nil {
			return err
		}
		out = append(out, walkRecord{rel, entry.IsDir(), d})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPolicyWalkerEquivalence(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":     "*.log\n!keep.log\ntmp/\n",
		"a/.gitignore":   "!special.log\n",
		"a/b/.gitignore": "*.tmp\n",
		"a/b/tmp/":       "",
		"a/b/tmp/inner":  "",
		"a/x.log":        "",
		"a/keep.log":     "",
		"a/special.log":  "",
		"a/b/f.tmp":      "",
		"a/b/keep.log":   "",
		"c/.gitignore":   "everything\n",
		"c/thing":        "",
		"c/sub/thing":    "",
		"top.log":        "",
		"keep.log":       "",
	})
	p := testPolicy(t, root, []SourceSpec{{Filename: ".gitignore", Rank: RankPerDirectory, Optional: true, Nested: true}})
	all := walkAll(t, p, "")
	// Expected walker visits: everything except descendants of ignored dirs.
	want := map[string]bool{}
	pruned := ""
	for _, rec := range all {
		if pruned != "" && (rec.rel == pruned || strings.HasPrefix(rec.rel, pruned+"/")) {
			continue
		}
		pruned = ""
		want[rec.rel] = true
		if rec.isDir && rec.d.Ignored() {
			pruned = rec.rel
		}
	}
	got, err := collectWalk(t, p, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("walker %d visits want %d", len(got), len(want))
	}
	for _, rec := range got {
		if !want[rec.rel] {
			t.Fatalf("unexpected visit %q", rec.rel)
		}
	}
	// Caller-supplied paths equal walker results for the same entries.
	for _, rec := range got {
		d, err := p.Match(rec.rel, rec.isDir)
		if err != nil || d != rec.d {
			t.Fatalf("match %q: %+v vs walk %+v (%v)", rec.rel, d, rec.d, err)
		}
	}
}

func TestPolicyWalkSubtreeAndSkip(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		// "excluded/" must come from an ancestor scope: a directory's own
		// ignore files apply only to its descendants, never to itself.
		".custom":        "out/\nexcluded/\n",
		"src/.custom":    "*.log\n",
		"src/a.log":      "",
		"src/b.log":      "",
		"src/sub/f":      "",
		"src/out/":       "",
		"src/out/x":      "",
		"other/keep.log": "",
		"excluded/.x":    "",
	})
	writeTree(t, root, map[string]string{"excluded/.custom2": "*\n"})
	p := testPolicy(t, root, []SourceSpec{
		{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true},
		{Filename: ".custom2", Rank: RankCommand, Optional: true, Nested: true},
	})

	// Subtree traversal keeps the root-relative namespace: the root rule
	// "out/" still prunes src/out, and only src descendants are visited.
	got, err := collectWalk(t, p, "src")
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, rec := range got {
		rels = append(rels, rec.rel)
	}
	want := []string{"src/.custom", "src/a.log", "src/b.log", "src/out", "src/sub", "src/sub/f"}
	if strings.Join(rels, ",") != strings.Join(want, ",") {
		t.Fatalf("subtree walk %v want %v", rels, want)
	}

	// An ignored traversal root visits nothing.
	if got, err := collectWalk(t, p, "excluded"); err != nil || len(got) != 0 {
		t.Fatalf("ignored traversal root: %+v %v", got, err)
	}

	// SkipDir on a file skips the rest of its directory; on a directory it
	// skips the descent.
	var seen []string
	err = p.Walk("", func(rel string, _ bool, _ PolicyDecision) error {
		seen = append(seen, rel)
		if rel == "src/a.log" {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(seen) != fmt.Sprint([]string{".custom", "excluded", "other", "other/keep.log", "src", "src/.custom", "src/a.log"}) {
		t.Fatalf("skipdir sequence %v", seen)
	}

	// Callback errors abort unchanged.
	sentinel := errors.New("stop")
	err = p.Walk("", func(_ string, _ bool, _ PolicyDecision) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("callback error: %v", err)
	}

	// Filesystem errors are wrapped and identify the path.
	if _, err := collectWalk(t, p, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing walk root: %v", err)
	}
	if err := p.Walk("..", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("escaping traversal root: %v", err)
	}
	if err := p.Walk("", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil callback: %v", err)
	}
}

func TestPolicyWalkSymlinks(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"real/inner.txt": "",
		"real/":          "",
		"direct.log":     "",
	})
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	p := testPolicy(t, root, []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}})

	// Symlinks are never followed: link is a non-directory entry and
	// link/inner.txt is not visited.
	got, err := collectWalk(t, p, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range got {
		if rec.rel == "link" && rec.isDir {
			t.Fatal("symlink reported as directory")
		}
		if rec.rel == "link/inner.txt" {
			t.Fatal("symlink followed")
		}
	}
	// Matching treats the symlink path itself as a candidate.
	if d, err := p.Match("link", false); err != nil || d.Action != NoMatch {
		t.Fatalf("symlink match: %+v %v", d, err)
	}
}

func TestPolicyConcurrent(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{".gitignore": "*.log\n!keep.log\nbuild/\n"}
	for i := range 12 {
		dir := fmt.Sprintf("d%02d", i)
		files[dir+"/"] = ""
		files[dir+"/.gitignore"] = fmt.Sprintf("local%02d\n", i)
		files[dir+"/file.log"] = ""
		files[dir+"/keep.log"] = ""
		files[fmt.Sprintf("d%02d/sub", i)+"/f"] = ""
	}
	writeTree(t, root, files)
	p := testPolicy(t, root, []SourceSpec{{Filename: ".gitignore", Rank: RankPerDirectory, Optional: true, Nested: true}})

	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for i := range 40 {
				dir := fmt.Sprintf("d%02d", i%12)
				d, err := p.Match(dir+"/file.log", false)
				if err != nil || d.Action != Exclude {
					t.Errorf("file.log: %+v %v", d, err)
					return
				}
				d, err = p.Match(dir+"/keep.log", false)
				if err != nil || d.Action != Include {
					t.Errorf("keep.log: %+v %v", d, err)
					return
				}
				if w == 0 {
					if err := collectWalkConcurrent(p); err != nil {
						t.Error(err)
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

func collectWalkConcurrent(p *Policy) error {
	visits := 0
	err := p.Walk("", func(_ string, _ bool, _ PolicyDecision) error {
		visits++
		return nil
	})
	if err == nil && visits == 0 {
		return errors.New("concurrent walk visited nothing")
	}
	return err
}

// TestPolicyModelEquivalence checks policy decisions against an independently
// specified cumulative matcher model: same scopes, ranks and source order,
// compiled directly through Compile.
func TestPolicyModelEquivalence(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"root-src":       "shared\n*.log\n!keep.log\n",
		"a/nested-src":   "!x.log\n",
		"a/b/nested-src": "y/\n",
		"deep/z":         "",
	})
	specs := []SourceSpec{
		{Filename: "root-src", Rank: RankGlobal, Optional: true},
		{Filename: "nested-src", Rank: RankPerDirectory, Optional: true, Nested: true},
	}
	p := testPolicy(t, root, specs)

	// Model: cumulative sources per directory, independently constructed.
	model := map[string][]Source{
		"":  {{Label: "root-src", Rank: RankGlobal}},
		"a": {{Label: "root-src", Rank: RankGlobal}, {Label: "a/nested-src", Scope: "a", Rank: RankPerDirectory}},
		"a/b": {
			{Label: "root-src", Rank: RankGlobal},
			{Label: "a/nested-src", Scope: "a", Rank: RankPerDirectory},
			{Label: "a/b/nested-src", Scope: "a/b", Rank: RankPerDirectory},
		},
	}
	for dir, sources := range model {
		for i := range sources {
			full := filepath.Join(root, filepath.FromSlash(sources[i].Label))
			data, err := os.ReadFile(full)
			if err != nil {
				t.Fatal(err)
			}
			sources[i].Data = data
		}
		m, _, err := Compile(sources, Options{})
		if err != nil {
			t.Fatal(err)
		}
		paths := []string{dir + "/shared", dir + "/a.log", dir + "/keep.log", dir + "/x.log", dir + "/y", dir + "/z"}
		if dir == "" {
			paths = []string{"shared", "a.log", "keep.log", "x.log", "y", "z"}
		}
		for _, path := range paths {
			want, err := m.Match(path, false)
			if err != nil {
				t.Fatal(err)
			}
			got := policyAction(t, p, path, false)
			if got.Action != want.Action {
				t.Fatalf("dir %q path %q: policy %d model %d", dir, path, got.Action, want.Action)
			}
			if (got.Rule == nil) != (want.Rule == nil) {
				t.Fatalf("dir %q path %q: rule %+v vs %+v", dir, path, got.Rule, want.Rule)
			}
			if got.Rule != nil && got.Rule.Source() != want.Rule.Source() {
				t.Fatalf("dir %q path %q: source %q vs %q", dir, path, got.Rule.Source(), want.Rule.Source())
			}
		}
	}
}
