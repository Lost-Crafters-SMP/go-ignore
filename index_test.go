// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// eligible reports whether a non-slash candidate path lies strictly inside
// the scope. Production evaluation derives the same answer from boundary
// activation; this direct predicate keeps the differential oracle
// independent of that machinery.
func (g *scopeGroup) eligible(path string, fold bool) bool {
	return g.scope == "" || scopeEligible(path, g.scope, fold)
}

// scopeEligible is the per-rule equivalent of scopeGroup.eligible.
func scopeEligible(path, scope string, fold bool) bool {
	return len(path) > len(scope) && path[len(scope)] == '/' && equalBytes(path[:len(scope)], scope, fold)
}

// naiveEligibility computes per-scope containment directly, without the
// production length-ordered index or activation cursor, so the differential
// oracle checks boundary activation by construction rather than reusing it.
// A nil scratch is allowed and allocates, which suits test callers.
func naiveEligibility(m *Matcher, scratch *evalScratch, path string) []bool {
	var elig []bool
	if scratch != nil {
		elig = scratch.eligibility(len(m.scopes))
	} else {
		elig = make([]bool, len(m.scopes))
	}
	for gi := range m.scopes {
		elig[gi] = m.scopes[gi].eligible(path, m.fold)
	}
	return elig
}

// refMatch is the exhaustive-scan reference for Match: identical control flow,
// but every winner lookup uses scanWinner with naively computed eligibility
// instead of the boundary-driven flags.
func refMatch(m *Matcher, path string, isDir bool) (Decision, error) {
	if err := validatePath(path, false); err != nil {
		return Decision{}, err
	}
	var scratch evalScratch
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if r := m.scanWinner(path[:i], true, false, &scratch, false, naiveEligibility(m, &scratch, path[:i])); r != nil && !r.negated {
				return Decision{Block: &Block{Directory: path[:i], Rule: r}}, nil
			}
		}
	}
	r := m.scanWinner(path, isDir, false, &scratch, false, naiveEligibility(m, &scratch, path))
	if isDir && (r == nil || r.negated) {
		r = m.scanWinner(path, true, true, &scratch, false, naiveEligibility(m, &scratch, path))
	}
	if r == nil {
		return Decision{}, nil
	}
	action := Exclude
	if r.negated {
		action = Include
	}
	return Decision{Action: action, Rule: r}, nil
}

func sameDecision(a, b Decision) bool {
	if a.Action != b.Action || (a.Rule == nil) != (b.Rule == nil) || (a.Block == nil) != (b.Block == nil) {
		return false
	}
	if a.Rule != b.Rule {
		return false
	}
	if a.Block != nil && (a.Block.Directory != b.Block.Directory || a.Block.Rule != b.Block.Rule) {
		return false
	}
	return true
}

func checkEquivalence(t *testing.T, m *Matcher, path string, isDir bool) {
	t.Helper()
	got, err := m.Match(path, isDir)
	if err != nil {
		t.Fatalf("match %q: %v", path, err)
	}
	want, refErr := refMatch(m, path, isDir)
	if (err == nil) != (refErr == nil) {
		t.Fatalf("error divergence %q: %v vs %v", path, err, refErr)
	}
	if !sameDecision(got, want) {
		t.Fatalf("index divergence path=%q dir=%v indexed=%+v scan=%+v", path, isDir, got, want)
	}
}

func TestLiteralIndexSelection(t *testing.T) {
	for _, c := range []struct {
		name    string
		sources []Source
		fold    bool
	}{
		{"literal-outranks-earlier-wildcard", []Source{{Data: []byte("*.log\nz.log\n")}}, false},
		{"wildcard-outranks-earlier-literal", []Source{{Data: []byte("z.log\n*.log\n")}}, false},
		{"dir-only-literal", []Source{{Data: []byte("build/\nb*\n")}}, false},
		{"negated-literal", []Source{{Data: []byte("*.log\n!keep.log\n")}}, false},
		{"empty-pattern", []Source{{Data: []byte("/\na/\n")}}, false},
		{"escaped-literal", []Source{{Data: []byte("\\*literal\n\\?q\nx\\\\\n")}}, false},
		{"scoped-literals", []Source{
			{Label: "root", Data: []byte("/x\ny\n")},
			{Label: "deep", Scope: "a/b", Rank: RankPerDirectory, Data: []byte("/z\nw\n")},
		}, false},
		{"scoped-fold", []Source{
			{Label: "root", Data: []byte("/X\n")},
			{Label: "deep", Scope: "A/B", Data: []byte("/Y\n")},
		}, true},
		{"rank-over-depth", []Source{
			{Label: "global", Rank: RankGlobal, Scope: "a/b", Data: []byte("/g\n")},
			{Label: "per-dir", Rank: RankPerDirectory, Data: []byte("/p\n")},
		}, false},
		{"mixed-wildcards", []Source{{Data: []byte("**/deep.log\nsub/**\n?.tmp\n[ab]c\n!keep\n")}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, _, err := Compile(c.sources, Options{CaseFold: c.fold})
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{
				"z.log", "a.log", "keep.log", "build", "b", "bx", "x", "y", "a", "a/x", "a/y",
				"a/b", "a/b/z", "a/b/w", "a/b/g", "a/b/p", "a/b/deep.log", "deep.log",
				"sub", "sub/k", "sub/k/v", "k.tmp", "ac", "bc", "keep", "cc",
				"*literal", "?q", `x\`, "BUILD", "A", "A/X", "a/b/upper", "a/bb/z",
			}
			for _, path := range paths {
				for _, isDir := range []bool{false, true} {
					checkEquivalence(t, m, path, isDir)
				}
			}
		})
	}
}

func TestLiteralIndexOutcomes(t *testing.T) {
	// Exact outcomes for index-selected winners, independent of the differential.
	type selection struct {
		name          string
		text          string
		path          string
		dir           bool
		wantIgnored   bool
		wantLine      int
		wantBlockLine int
	}
	for _, c := range []selection{
		{"later-wildcard-beats-earlier-literal", "z.log\n*.log\n", "z.log", false, true, 2, 0},
		{"later-literal-beats-earlier-wildcard", "*.log\nz.log\n", "z.log", false, true, 2, 0},
		{"dir-only-skipped-for-file", "build/\nb*\n", "build", false, true, 2, 0},
		{"later-rule-wins-for-dir", "build/\nb*\n", "build", true, true, 2, 0},
		{"negation-via-index", "*.log\n!keep.log\n", "keep.log", false, false, 2, 0},
		{"ancestor-block-via-index", "a/sub/\n", "a/sub/file", false, true, 1, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := mustCompile(t, c.text, Options{})
			d, err := m.Match(c.path, c.dir)
			if err != nil {
				t.Fatal(err)
			}
			if d.Ignored() != c.wantIgnored {
				t.Fatalf("ignored=%v decision=%+v", d.Ignored(), d)
			}
			if d.Block != nil {
				if d.Block.Rule.Line() != c.wantBlockLine {
					t.Fatalf("block line %d want %d", d.Block.Rule.Line(), c.wantBlockLine)
				}
				return
			}
			if d.Rule == nil || d.Rule.Line() != c.wantLine {
				t.Fatalf("winner %+v want line %d", d.Rule, c.wantLine)
			}
		})
	}
}

func TestScratchReuseAcrossLengths(t *testing.T) {
	m := mustCompile(t, "**/w*.log\nsub/**\n?top\n", Options{})
	// Long paths cross the fixed-row threshold; one Match grows the shared
	// scratch, later evaluations in the same call reuse it.
	for depth := range []int{2, 60, 130, 300, 700} {
		path := strings.Repeat("d/", depth) + "w" + strings.Repeat("x", depth) + ".log"
		checkEquivalence(t, m, path, false)
		checkEquivalence(t, m, path, true)
	}
	var fresh evalScratch
	if fresh.rows[0] != nil || fresh.rows[1] != nil {
		t.Fatal("scratch must start empty")
	}
}

func TestConcurrentLongPaths(t *testing.T) {
	m := mustCompile(t, "**/w*.log\nsub/**\n!keep\n", Options{})
	depth := 300
	path := strings.Repeat("d/", depth) + "w" + strings.Repeat("x", depth) + ".log"
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for range 200 {
				d, err := m.Match(path, w%2 == 0)
				if err != nil || d.Action != Exclude {
					t.Error(d, err)
					return
				}
				_, err = m.Match("d/keep", false)
				if err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func FuzzWinnerIndexEquivalence(f *testing.F) {
	seeds := []struct {
		text string
		path string
		mode byte
	}{
		{"*.log\n!keep.log\n", "a/keep.log", 0},
		{"build/\nb*\n", "build", 2},
		{"/x\ny\n", "a/x", 4},
		{"[a-c]?z\n**/w\n", "abz/w", 1},
		{"A/\n!B/\n", "a/b", 3},
		{"/\n/\n", "a", 2},
		{"\\*literal\n", "*literal", 0},
		{"sub/**\n", "sub/a/b/c", 0},
	}
	for _, s := range seeds {
		f.Add(s.text, s.path, s.mode)
	}
	f.Fuzz(func(t *testing.T, text string, path string, mode byte) {
		if strings.ContainsRune(path, 0) || strings.ContainsRune(text, 0) {
			t.Skip()
		}
		for part := range strings.SplitSeq(path, "/") {
			if part == "" || part == "." || part == ".." {
				t.Skip()
			}
		}
		scope := ""
		if mode&4 != 0 {
			scope = "a/b"
		}
		sources := []Source{{Label: "s", Scope: scope, Data: []byte(text)}}
		if mode&8 != 0 {
			sources = append(sources, Source{Label: "root", Data: []byte("/lit\nw*\n!n\n")})
		}
		m, _, err := Compile(sources, Options{CaseFold: mode&1 != 0})
		if err != nil {
			t.Skip()
		}
		checkEquivalence(t, m, path, mode&2 != 0)
	})
}

func ExampleMatcher_scopedIndex() {
	m, _, err := Compile([]Source{
		{Label: "root", Data: []byte("/build\n")},
		{Label: "nested", Scope: "a/b", Rank: RankPerDirectory, Data: []byte("local\n")},
	}, Options{})
	if err != nil {
		fmt.Println(err)
		return
	}
	d, _ := m.Match("a/b/local", false)
	fmt.Println(d.Action == Exclude, d.Rule.Source())
	d, _ = m.Match("build", false)
	fmt.Println(d.Action == Exclude, d.Rule.Source())
	// Output:
	// true nested
	// true root
}
