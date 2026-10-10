// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// scopeOrderSources builds a nested-scope workload: root rules plus one source
// per level of a d/d/... chain, each mixing literal, wildcard, negated and
// slash-form rules per kind.
func scopeOrderSources(depth int, kind string) []Source {
	rules := func() []byte {
		switch kind {
		case "literal":
			return []byte("hit\n!keep\ncache/\n/sub-lit\n")
		case "wildcard":
			return []byte("h*t\n!k*p\n*.tmp\n/sub/**\n")
		default:
			return []byte("hit\n!keep\n*.tmp\ncache/\n/sub-lit\n/sub/**\n[\n")
		}
	}
	sources := []Source{{Label: "root", Rank: RankGlobal, Data: rules()}}
	for i := range depth {
		sources = append(sources, Source{
			Label: fmt.Sprintf("nested-%d", i),
			Scope: strings.Repeat("d/", i) + "d",
			Rank:  RankPerDirectory,
			Data:  rules(),
		})
	}
	return sources
}

// adversarialSources appends scopes that stress index boundaries: equal-length
// siblings, fold-equivalent aliases, rank overrides and very long scopes.
func adversarialSources(depth int, kind string) []Source {
	s := scopeOrderSources(depth, kind)
	s = append(s,
		Source{Scope: "d/D", Rank: RankCommand, Data: []byte("/\n!keep\n[\n")},
		Source{Scope: "d/dx", Data: []byte("*\n")},
		Source{Scope: "sib", Data: []byte("*\n")},
		Source{Scope: "D/d", Rank: RankCommand + 1, Data: []byte("/hit\n!keep\n")},
		Source{Scope: strings.Repeat("A", 60), Data: []byte("hit\n/cache/*\n!cache/keep\n")},
		Source{Scope: strings.Repeat("B", 61), Data: []byte("absent\n")},
	)
	return s
}

// refDecide is the exhaustive-reference decision including the bare-exclusion
// verdict, mirroring refMatch's control flow over scanWinner with naive
// eligibility.
func refDecide(m *Matcher, path string, isDir bool) (Decision, bool) {
	var scratch evalScratch
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if r := m.scanWinner(path[:i], true, false, &scratch, false, naiveEligibility(m, &scratch, path[:i])); r != nil && !r.negated {
				return Decision{Block: &Block{Directory: path[:i], Rule: r}}, false
			}
		}
	}
	r := m.scanWinner(path, isDir, false, &scratch, false, naiveEligibility(m, &scratch, path))
	bare := isDir && r != nil && !r.negated
	if isDir && !bare {
		r = m.scanWinner(path, true, true, &scratch, false, naiveEligibility(m, &scratch, path))
	}
	if r == nil {
		return Decision{}, bare
	}
	action := Exclude
	if r.negated {
		action = Include
	}
	return Decision{Action: action, Rule: r}, bare
}

// checkScopeOrderEquivalence asserts the indexed evaluation (Match, Descend)
// agrees with the exhaustive reference on one path.
func checkScopeOrderEquivalence(t *testing.T, m *Matcher, path string, isDir bool) {
	t.Helper()
	got, err := m.Match(path, isDir)
	if err != nil {
		t.Fatalf("match %q: %v", path, err)
	}
	want, refErr := refMatch(m, path, isDir)
	if refErr != nil {
		t.Fatalf("reference rejected %q: %v", path, refErr)
	}
	if !sameDecision(got, want) {
		t.Fatalf("indexed divergence path=%q dir=%v got=%+v want=%+v", path, isDir, got, want)
	}
	if isDir {
		ref, refBare := refDecide(m, path, true)
		if !sameDecision(ref, got) {
			t.Fatalf("reference divergence path=%q got=%+v ref=%+v", path, got, ref)
		}
		gd, gdescend, derr := m.Descend(path)
		if derr != nil {
			t.Fatalf("descend %q: %v", path, derr)
		}
		if !sameDecision(gd, got) {
			t.Fatalf("descend decision divergence path=%q got=%+v match=%+v", path, gd, got)
		}
		if wantDescend := ref.Block == nil && !refBare; gdescend != wantDescend {
			t.Fatalf("descend verdict divergence path=%q got=%v want=%v", path, gdescend, wantDescend)
		}
	}
}

// scopeOrderPaths enumerates query paths exercising hits, misses, negations,
// ancestor blocks, canonical slash endpoints, fold aliases and boundaries.
func scopeOrderPaths(depth int) []string {
	paths := []string{
		"d", "d/hit", "d/keep", "d/miss", "d/cache", "d/cache/x", "d/sub-lit",
		"d/sub/x", "d/x.tmp", "d/d", "d/d/hit", "D/d/hit", "d/D", "d/dx/hit",
		"sib/x", strings.Repeat("A", 60), strings.Repeat("A", 60) + "/hit",
		strings.Repeat("A", 60) + "/cache", strings.Repeat("A", 60) + "/cache/keep",
		strings.Repeat("B", 61), strings.Repeat("B", 61) + "/absent",
	}
	prefix := ""
	for range depth {
		prefix += "d/"
		paths = append(paths,
			prefix[:len(prefix)-1], // the scope directory itself (canonical endpoint)
			prefix+"hit", prefix+"keep", prefix+"miss", prefix+"cache", prefix+"x.tmp",
		)
	}
	return paths
}

// TestScopeOrderEquivalence runs the indexed evaluation against the exhaustive
// reference over nested, sibling, equal-length and folded scopes, for one-shot
// compilation and Builder insertion orders.
func TestScopeOrderEquivalence(t *testing.T) {
	for _, fold := range []bool{false, true} {
		for _, kind := range []string{"literal", "wildcard", "mixed"} {
			sources := adversarialSources(6, kind)
			check := func(t *testing.T, m *Matcher) {
				t.Helper()
				for _, path := range scopeOrderPaths(6) {
					query := path
					if fold {
						query = strings.ToUpper(path)
					}
					for _, isDir := range []bool{false, true} {
						checkScopeOrderEquivalence(t, m, query, isDir)
					}
				}
				for _, invalid := range []string{"", "d//x", "d/../x", "d/./x"} {
					if _, err := m.Match(invalid, true); err == nil {
						t.Fatalf("invalid path %q accepted", invalid)
					}
				}
			}
			t.Run(fmt.Sprintf("fold=%t/kind=%s/compile", fold, kind), func(t *testing.T) {
				m, _, err := Compile(sources, Options{CaseFold: fold})
				if err != nil {
					t.Fatal(err)
				}
				check(t, m)
			})
			t.Run(fmt.Sprintf("fold=%t/kind=%s/builder", fold, kind), func(t *testing.T) {
				b, err := NewBuilder(Options{CaseFold: fold})
				if err != nil {
					t.Fatal(err)
				}
				for i := range sources {
					if err := b.Add(sources[i : i+1]); err != nil {
						t.Fatal(err)
					}
					if i == 0 || i == 3 || i == len(sources)-1 {
						check(t, b.s.m)
					}
				}
				m, _ := b.Build()
				check(t, m)
			})
			t.Run(fmt.Sprintf("fold=%t/kind=%s/reversed-builder", fold, kind), func(t *testing.T) {
				// Deepest-first insertion: new scopes arrive with both smaller
				// and larger lengths than already-indexed ones.
				reversed := slices.Clone(sources)
				slices.Reverse(reversed)
				b, err := NewBuilder(Options{CaseFold: fold})
				if err != nil {
					t.Fatal(err)
				}
				for i := range reversed {
					if err := b.Add(reversed[i : i+1]); err != nil {
						t.Fatal(err)
					}
				}
				m, _ := b.Build()
				check(t, m)
			})
		}
	}
}

// TestScopeOrderBuilderMatchesCompile proves incremental index maintenance
// converges to one-shot construction for out-of-order same-length siblings.
func TestScopeOrderBuilderMatchesCompile(t *testing.T) {
	// All 96 scopes share one byte length, so ID ties dominate the order.
	sources := make([]Source, 96)
	for i := range sources {
		sources[i] = Source{Scope: fmt.Sprintf("b%02d/x%02d", i%8, i), Data: []byte("hit\n*.w\n")}
	}
	one, _, err := Compile(sources, Options{CaseFold: true})
	if err != nil {
		t.Fatal(err)
	}
	patterns := []string{"in-order", "stride-012", "stride-10", "stride-201"}
	for _, name := range patterns {
		t.Run(name, func(t *testing.T) {
			seq := make([]Source, 0, len(sources))
			seen := make([]bool, len(sources))
			switch name {
			case "stride-012":
				for start := range 3 {
					for i := start; i < len(sources); i += 3 {
						seen[i] = true
					}
				}
				for start := range 3 {
					for i := start; i < len(sources); i += 3 {
						seq = append(seq, sources[i])
					}
				}
			case "stride-10":
				for _, start := range []int{1, 0} {
					for i := start; i < len(sources); i += 3 {
						if !seen[i] {
							seen[i] = true
							seq = append(seq, sources[i])
						}
					}
				}
			case "stride-201":
				for _, start := range []int{2, 0, 1} {
					for i := start; i < len(sources); i += 3 {
						if !seen[i] {
							seen[i] = true
							seq = append(seq, sources[i])
						}
					}
				}
			}
			for i := range sources {
				if !seen[i] {
					seq = append(seq, sources[i])
				}
			}
			if len(seq) != len(sources) {
				t.Fatal("insertion pattern lost sources")
			}
			b, err := NewBuilder(Options{CaseFold: true})
			if err != nil {
				t.Fatal(err)
			}
			for i := range seq {
				if err := b.Add(seq[i : i+1]); err != nil {
					t.Fatal(err)
				}
			}
			incremental, _ := b.Build()
			if !slices.Equal(one.scopeOrder, incremental.scopeOrder) {
				// Every scope shares one length, so both ID spaces sort to the
				// identity permutation; a mismatch means a merge defect.
				t.Fatalf("insertion pattern %s produced a different scope order", name)
			}
			for i := range sources {
				path := sources[i].Scope + "/hit"
				a, _ := one.Match(path, false)
				c, _ := incremental.Match(path, false)
				if builderDecisionKey(a) != builderDecisionKey(c) {
					t.Fatalf("decision divergence for %q", path)
				}
			}
		})
	}
}

// TestScopeOrderIndexShape pins construction invariants: nil only without
// scopes, sorted by byte length with stable ID ties, in Compile and Builder
// alike.
func TestScopeOrderIndexShape(t *testing.T) {
	empty := &Matcher{}
	if empty.scopeOrder != nil {
		t.Fatal("zero matcher has a scope index")
	}
	single, _, err := Compile([]Source{{Data: []byte("a\nb/\n")}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(single.scopeOrder) != 1 || single.scopeOrder[0] != 0 {
		t.Fatalf("single-scope index %v", single.scopeOrder)
	}
	scoped, _, err := Compile([]Source{{Data: []byte("a\n")}, {Scope: "x", Data: []byte("b\n")}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.scopeOrder) != 2 || scoped.scopeOrder[0] != 0 || scoped.scopeOrder[1] != 1 {
		t.Fatalf("unexpected order %v", scoped.scopeOrder)
	}

	sources := []Source{
		{Scope: "zz", Data: []byte("a\n")},
		{Scope: "a/bb/cc", Data: []byte("a\n")},
		{Scope: "a/bb", Data: []byte("a\n")},
		{Scope: "a", Data: []byte("a\n")},
		{Data: []byte("a\n")},
		{Scope: "q", Data: []byte("a\n")},
		{Scope: "a/bb/dd", Data: []byte("a\n")},
	}
	one, _, err := Compile(sources, Options{})
	if err != nil {
		t.Fatal(err)
	}
	verify := func(t *testing.T, m *Matcher) {
		t.Helper()
		if len(m.scopes) == 0 {
			if m.scopeOrder != nil {
				t.Fatal("empty matcher has a scope index")
			}
			return
		}
		if len(m.scopeOrder) != len(m.scopes) {
			t.Fatalf("index length %d scopes %d", len(m.scopeOrder), len(m.scopes))
		}
		for i := 1; i < len(m.scopeOrder); i++ {
			a, b := m.scopeOrder[i-1], m.scopeOrder[i]
			la, lb := len(m.scopes[a].scope), len(m.scopes[b].scope)
			if la > lb || la == lb && a > b {
				t.Fatalf("order violated at %d: %d(%d) before %d(%d)", i, a, la, b, lb)
			}
		}
	}
	verify(t, one)
	// Every insertion order keeps the per-matcher invariant and reproduces
	// one-shot decisions. Compile and Builder number scopes differently (rule
	// priority vs Add order), so raw ID permutations are not comparable across
	// producers; equal-length ties legitimately resolve in either order.
	orders := [][]int{{0, 1, 2, 3, 4, 5, 6}, {6, 5, 4, 3, 2, 1, 0}, {3, 0, 6, 4, 1, 5, 2}, {4, 2, 6, 0, 5, 3, 1}}
	for _, order := range orders {
		b, err := NewBuilder(Options{})
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range order {
			if err := b.Add(sources[i : i+1]); err != nil {
				t.Fatal(err)
			}
			verify(t, b.s.m)
		}
		m, _ := b.Build()
		for _, scope := range []string{"zz", "a/bb/cc", "a/bb", "a", "", "q", "a/bb/dd"} {
			for _, leaf := range []string{"a", "other"} {
				path := leaf
				if scope != "" {
					path = scope + "/" + leaf
				}
				a, _ := one.Match(path, false)
				c, _ := m.Match(path, false)
				if builderDecisionKey(a) != builderDecisionKey(c) {
					t.Fatalf("decision divergence for %q under order %v", path, order)
				}
			}
		}
	}
	// Rules added to existing scopes leave the index untouched.
	b, err := NewBuilder(Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range sources {
		if err := b.Add(sources[i : i+1]); err != nil {
			t.Fatal(err)
		}
	}
	order := slices.Clone(b.s.m.scopeOrder)
	if err := b.Add([]Source{{Scope: "zz", Data: []byte("late\n")}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, b.s.m.scopeOrder) || len(b.s.m.scopes) != len(order) {
		t.Fatal("same-scope addition disturbed the index")
	}
}

// TestScopeOrderBuilderAtomicity proves a failed Add with new scopes leaves
// the published index and decisions unchanged, and later commits publish it.
func TestScopeOrderBuilderAtomicity(t *testing.T) {
	b, err := NewBuilder(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Add([]Source{{Data: []byte("root\n")}}); err != nil {
		t.Fatal(err)
	}
	before := builderMatcherCopy(b.s.m)
	b.s.preCommit = func() error { return fmt.Errorf("injected: %w", ErrInvalidInput) }
	batch := []Source{{Scope: "new/scope", Data: []byte("x\n")}, {Scope: "other", Data: []byte("y\n")}}
	if err := b.Add(batch); err == nil {
		t.Fatal("injected failure suppressed")
	}
	if now := builderMatcherCopy(b.s.m); !reflect.DeepEqual(now, before) {
		t.Fatal("failed batch changed the matcher")
	}
	b.s.preCommit = nil
	if err := b.Add(batch); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"new/scope/x", "other/y", "root"} {
		got, _ := b.Match(path, false)
		if got.Action != Exclude {
			t.Fatalf("committed scope rules missing for %q: %+v", path, got)
		}
	}
	want, _, err := Compile(append([]Source{{Data: []byte("root\n")}}, batch...), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(want.scopeOrder, b.s.m.scopeOrder) {
		t.Fatal("incremental index diverged from compiled index")
	}
}

// TestScopeOrderZeroAllocation keeps established zero-allocation query paths
// zero-allocation with the index active, including folded long scopes.
func TestScopeOrderZeroAllocation(t *testing.T) {
	m, _, err := Compile([]Source{
		{Scope: strings.Repeat("A", 60), Data: []byte("hit\n/cache/*\n!cache/keep\n")},
		{Scope: strings.Repeat("B", 61), Data: []byte("absent\n")},
		{Data: []byte("*.log\n")},
	}, Options{CaseFold: true})
	if err != nil {
		t.Fatal(err)
	}
	queries := []struct {
		path string
		dir  bool
	}{
		{strings.Repeat("a", 60) + "/miss", false},
		{strings.Repeat("a", 60) + "/cache", true},
		{"x.log", false},
		{"deep/x.log", true},
	}
	for _, q := range queries {
		if n := testing.AllocsPerRun(200, func() {
			scopeOrderDecisionSink, _ = m.Match(q.path, q.dir)
		}); n != 0 {
			t.Fatalf("Match(%q) allocated %v times", q.path, n)
		}
	}
}

var scopeOrderDecisionSink Decision

// TestScopeOrderConcurrentReads exercises published matchers under concurrent
// Match and Descend calls with the index active.
func TestScopeOrderConcurrentReads(t *testing.T) {
	sources := adversarialSources(24, "mixed")
	m, _, err := Compile(sources, Options{CaseFold: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBuilder(Options{CaseFold: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := range sources {
		if err := b.Add(sources[i : i+1]); err != nil {
			t.Fatal(err)
		}
	}
	incremental, _ := b.Build()
	paths := scopeOrderPaths(24)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for i := range 50 {
				path := strings.ToUpper(paths[i%len(paths)])
				d1, err := m.Match(path, i%2 == 0)
				if err != nil {
					t.Error(err)
					return
				}
				d2, err := incremental.Match(path, i%2 == 0)
				if err != nil || builderDecisionKey(d1) != builderDecisionKey(d2) {
					t.Errorf("divergence %q: %+v vs %+v (%v)", path, d1, d2, err)
					return
				}
				if _, _, err := m.Descend(path); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
}

// TestScopeOrderDeepChain covers depths beyond the fixed eligibility storage.
func TestScopeOrderDeepChain(t *testing.T) {
	for _, depth := range []int{8, 64, 130} {
		m, _, err := Compile(scopeOrderSources(depth, "mixed"), Options{})
		if err != nil {
			t.Fatal(err)
		}
		prefix := strings.Repeat("d/", depth-1)
		for _, path := range []string{prefix + "hit", prefix + "keep", prefix + "miss", prefix + "cache", prefix[:len(prefix)-1]} {
			for _, isDir := range []bool{false, true} {
				checkScopeOrderEquivalence(t, m, path, isDir)
			}
		}
		// An early ancestor block must short-circuit identically.
		blocked, _, err := Compile(append(scopeOrderSources(depth, "mixed"), Source{Data: []byte("d/\n")}), Options{})
		if err != nil {
			t.Fatal(err)
		}
		checkScopeOrderEquivalence(t, blocked, prefix+"hit", false)
	}
}
