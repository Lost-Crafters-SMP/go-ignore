// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// builderDecisionKey renders a decision's observable outcome, including
// provenance, so builder results compare against one-shot compilation without
// pointer identity.
func builderDecisionKey(d Decision) string {
	rule := d.Rule
	prefix := fmt.Sprint(d.Action)
	if d.Block != nil {
		prefix += ":block:" + d.Block.Directory
		rule = d.Block.Rule
	}
	if rule != nil {
		prefix += fmt.Sprintf(":%s:%s:%d:%s:%s:%d:%v:%v", rule.Source(), rule.Scope(), rule.Line(), rule.Raw(), rule.Pattern(), rule.Rank(), rule.Negated(), rule.DirOnly())
	}
	return prefix
}

// builderCasePaths returns the fixed probe paths plus paths inside every
// scope a case uses.
func builderCasePaths(sources []Source) []string {
	paths := []string{
		"cache", "cache/sub", "cache/sub/out", "cache/sub/out/file", "cache/keep.tmp",
		"cache/sub/keep.tmp", "other", "other/keep.tmp", "other/file.log", "else/file.log",
		"blocked/file", "a/b/keep.tmp", "A/B/out", "keep.tmp", "cachee/keep.tmp",
		"out", "OUT", "", "bad//path",
	}
	for _, s := range sources {
		if s.Scope == "" {
			continue
		}
		paths = append(paths, s.Scope, s.Scope+"/keep.tmp", s.Scope+"/out", s.Scope+"/file.log")
	}
	return paths
}

// TestBuilderCumulativeEquivalence checks every builder state against a
// one-shot Compile of the same cumulative source list after each successful
// Add: decisions, descent verdicts, diagnostics and error parity included.
func TestBuilderCumulativeEquivalence(t *testing.T) {
	cases := [][]Source{
		{
			{Label: "root", Rank: RankPerDirectory, Data: []byte("/cache/*\nblocked/\n*.tmp\n")},
			{Label: "cache", Scope: "cache", Rank: RankPerDirectory, Data: []byte("!sub/\n!keep.tmp\n")},
			{Label: "sub", Scope: "cache/sub", Rank: RankPerDirectory, Data: []byte("!keep.tmp\nout/\n")},
			{Label: "sib", Scope: "other", Rank: RankPerDirectory, Data: []byte("!keep.tmp\n*.log\n")},
			{Label: "command", Rank: RankCommand, Data: []byte("!cache/sub/out/\n")},
			{Label: "late-low", Rank: -500, Data: []byte("*.log\n")},
		},
		{
			{Label: "dup", Rank: 500, Data: []byte("*.tmp\nbad[\n")},
			{Label: "dup", Rank: 500, Data: []byte("!keep.tmp\nbad[\n")},
			{Label: "deep", Scope: "A/B", Rank: -800, Data: []byte("out/\n!keep.tmp\n")},
			{Label: "empty", Data: nil},
			{Label: "oversize", Data: []byte(strings.Repeat("x", 65))},
			{Label: "later", Rank: 500, Data: []byte("keep.tmp\n")},
		},
		{
			{Label: "first", Rank: 500, Data: []byte("keep.tmp\nkeep.tmp/\n")},
			{Label: "high", Rank: 1000, Data: []byte("!keep.tmp\n!keep.tmp/\n")},
			{Label: "low", Rank: -500, Data: []byte("keep.tmp\nkeep.tmp/\n")},
			{Label: "middle", Rank: 700, Data: []byte("keep.tmp\n*.tmp\n")},
			{Label: "top", Rank: 1500, Data: []byte("keep.tmp/\n!*.tmp\n")},
		},
		{
			{Label: "uppercase", Rank: 1000, Data: []byte("KEEP.TMP\nOUT/\n")},
			{Label: "insert", Rank: -500, Data: []byte("!keep.tmp\n")},
			{Label: "reopen", Rank: 1000, Data: []byte("!Keep.Tmp\n!Out/\n")},
		},
		{
			// BOM, CRLF, escaped trailing spaces and empty rules beside
			// duplicate-label oversize diagnostics that tie on every sort key.
			{Label: "bomcrlf", Rank: 600, Data: []byte("\xef\xbb\xbfkeep.tmp\r\n!OUT/\r\nspaced \\ \r\n!\r\n")},
			{Label: "oversize", Data: []byte(strings.Repeat("x", 65))},
			{Label: "oversize", Data: []byte(strings.Repeat("y", 66))},
			{Label: "later", Rank: 600, Data: []byte("!keep.tmp\r\n")},
			{Label: "empty", Data: []byte("\r\n")},
		},
	}
	for ci, sources := range cases {
		paths := builderCasePaths(sources)
		for _, fold := range []bool{false, true} {
			for _, batchSize := range []int{1, 2, len(sources)} {
				opts := Options{CaseFold: fold, MaxSourceBytes: 64}
				b, err := NewBuilder(opts)
				if err != nil {
					t.Fatal(err)
				}
				for n := 0; n < len(sources); {
					end := min(n+batchSize, len(sources))
					if err := b.Add(sources[n:end]); err != nil {
						t.Fatal(err)
					}
					want, diag, err := Compile(sources[:end], opts)
					if err != nil {
						t.Fatalf("compile case=%d: %v", ci, err)
					}
					if got := b.Diagnostics(); !reflect.DeepEqual(got, diag) {
						t.Fatalf("diagnostics case=%d fold=%v batch=%d end=%d:\n got %v\nwant %v", ci, fold, batchSize, end, got, diag)
					}
					for _, path := range paths {
						for _, dir := range []bool{false, true} {
							got, ge := b.Match(path, dir)
							ref, re := want.Match(path, dir)
							if (ge != nil) != (re != nil) || builderDecisionKey(got) != builderDecisionKey(ref) {
								t.Fatalf("case=%d fold=%v batch=%d end=%d path=%q dir=%v got=%s want=%s err=%v/%v", ci, fold, batchSize, end, path, dir, builderDecisionKey(got), builderDecisionKey(ref), ge, re)
							}
						}
						got, gd, ge := b.Descend(path)
						ref, rd, re := want.Descend(path)
						if (ge != nil) != (re != nil) || gd != rd || (ge == nil && builderDecisionKey(got) != builderDecisionKey(ref)) {
							t.Fatalf("descend case=%d path=%q got=%s/%v want=%s/%v err=%v/%v", ci, path, builderDecisionKey(got), gd, builderDecisionKey(ref), rd, ge, re)
						}
					}
					n = end
				}
				before := builderDecisionKey(func() Decision { d, _ := b.Match("keep.tmp", false); return d }())
				if err := b.Add([]Source{{Data: []byte("!keep.tmp\n")}, {Scope: "bad//scope"}}); !errors.Is(err, ErrInvalidInput) {
					t.Fatal("invalid batch accepted")
				}
				if b.s.count != len(sources) {
					t.Fatal("invalid batch changed source count")
				}
				if err := b.Add([]Source{{Data: []byte("valid\n")}, {Data: []byte("\x00" + strings.Repeat("x", 65))}}); !errors.Is(err, ErrInvalidInput) || b.s.count != len(sources) {
					t.Fatal("NUL-in-oversize batch validation changed state")
				}
				if got, _ := b.Match("keep.tmp", false); builderDecisionKey(got) != before {
					t.Fatal("invalid batch changed matcher")
				}
				if _, _ = b.Build(); b.s.count != len(sources) {
					t.Fatal("build changed source accounting")
				}
				if err := b.Add(nil); !errors.Is(err, ErrInvalidInput) {
					t.Fatal("sealed builder accepted Add(nil)")
				}
			}
		}
	}
}

// builderMatcherCopy deep-copies matcher storage so atomicity tests can
// detect any change after a failed Add.
func builderMatcherCopy(m *Matcher) *Matcher {
	out := *m
	out.rules = make([]*Rule, len(m.rules))
	for i, r := range m.rules {
		rule := *r
		rule.program = slices.Clone(r.program)
		out.rules[i] = &rule
	}
	out.scopes = slices.Clone(m.scopes)
	for i := range out.scopes {
		out.scopes[i].base = maps.Clone(out.scopes[i].base)
		out.scopes[i].anchored = maps.Clone(out.scopes[i].anchored)
	}
	out.wild = slices.Clone(m.wild)
	out.scopeOrder = slices.Clone(m.scopeOrder)
	out.diagnostics = slices.Clone(m.diagnostics)
	return &out
}

// TestBuilderAtomicity proves every Add error path leaves accumulated state
// untouched: validation failures, injected post-preparation failures, and the
// behaviour of later successful additions.
func TestBuilderAtomicity(t *testing.T) {
	b, err := NewBuilder(Options{MaxSourceBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	initial := []Source{
		{Label: "z", Data: []byte("keep\nbad[\n")},
		{Label: "a", Scope: "nested", Data: []byte("!keep\n")},
	}
	if err := b.Add(initial); err != nil {
		t.Fatal(err)
	}
	_ = b.Diagnostics() // sorted mid-stream; later appends must stay equivalent
	snapshot := func() (m *Matcher, scopes map[string]int32, count int, clean bool) {
		return builderMatcherCopy(b.s.m), maps.Clone(b.s.scopeOf), b.s.count, b.s.diagClean
	}
	unchanged := func(before *Matcher, scopes map[string]int32, count int, clean bool) {
		if m, sc, c, cl := snapshot(); !reflect.DeepEqual(m, before) || !maps.Equal(sc, scopes) || c != count || cl != clean {
			t.Fatal("failed batch changed accumulated state")
		}
	}
	failing := [][]Source{
		{{Label: "unpublished", Data: []byte("!keep\n[\n")}, {Scope: "bad//scope"}},
		{{Label: "unpublished", Data: []byte("!keep\n[\n")}, {Data: []byte("\x00")}},
		{{Label: "unpublished", Data: []byte("!keep\n[\n")}, {Data: []byte("\x00" + strings.Repeat("x", 65))}},
	}
	for i, batch := range failing {
		before, scopes, count, clean := snapshot()
		if err := b.Add(batch); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("case %d: expected invalid-input failure", i)
		}
		unchanged(before, scopes, count, clean)
	}
	// Cumulative error offsets: the second source of this batch is the
	// builder's fourth accepted-then-rejected position.
	err = b.Add([]Source{{Label: "ok", Data: []byte("x\n")}, {Scope: "bad//scope"}})
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "source 3 scope") {
		t.Fatalf("cumulative offset missing: %v", err)
	}
	// Injected failure after full preparation, immediately before commit.
	injected := errors.New("injected pre-commit failure")
	b.s.preCommit = func() error { return injected }
	before, scopes, count, clean := snapshot()
	good := []Source{{Label: "unpublished", Rank: -100, Data: []byte("!keep\n[\n")}}
	if err := b.Add(good); !errors.Is(err, injected) {
		t.Fatalf("injected failure not returned: %v", err)
	}
	unchanged(before, scopes, count, clean)
	b.s.preCommit = nil
	// The same batch succeeds once the fault is removed, and the result still
	// equals cumulative one-shot compilation.
	next := []Source{{Label: "later", Data: []byte("!keep\n[\n")}}
	if err := b.Add(next); err != nil {
		t.Fatal(err)
	}
	want, diag, err := Compile(append(slices.Clone(initial), next...), Options{MaxSourceBytes: 64})
	if err != nil || !reflect.DeepEqual(b.Diagnostics(), diag) {
		t.Fatalf("failed additions affected future diagnostics: %v", err)
	}
	for _, path := range []string{"keep", "nested/keep", "other/keep"} {
		got, ge := b.Match(path, false)
		ref, re := want.Match(path, false)
		if (ge != nil) != (re != nil) || builderDecisionKey(got) != builderDecisionKey(ref) {
			t.Fatalf("failed additions affected future decisions: %q", path)
		}
	}
	diags := b.Diagnostics()
	diags[0].Source = "mutated"
	if b.Diagnostics()[0].Source == "mutated" {
		t.Fatal("diagnostics alias builder state")
	}
}

// TestBuilderLifecycle covers nil and zero values, option validation,
// sealing, repeated builds, copied handles and post-seal reads.
func TestBuilderLifecycle(t *testing.T) {
	var nilBuilder *Builder
	if err := nilBuilder.Add(nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("nil Add(nil)")
	}
	if err := nilBuilder.Add([]Source{{Data: []byte("x\n")}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("nil Add")
	}
	if d, err := nilBuilder.Match("x", false); err == nil || !errors.Is(err, ErrInvalidInput) || d != (Decision{}) {
		t.Fatal("nil Match")
	}
	if _, _, err := nilBuilder.Descend("x"); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("nil Descend")
	}
	if diags := nilBuilder.Diagnostics(); diags != nil {
		t.Fatal("nil Diagnostics")
	}
	if m, diags := nilBuilder.Build(); m != nil || diags != nil {
		t.Fatal("nil Build")
	}
	if b, err := NewBuilder(Options{MaxSourceBytes: -1}); b != nil || !errors.Is(err, ErrInvalidInput) {
		t.Fatal("NewBuilder accepted invalid options")
	}
	if b, err := NewBuilder(Options{MaxSourceBytes: defaultSourceBytes + 1}); b != nil || !errors.Is(err, ErrInvalidInput) {
		t.Fatal("NewBuilder accepted oversized cap")
	}

	// A zero-value builder adopts default options and stays usable.
	var zero Builder
	if err := zero.Add(nil); err != nil {
		t.Fatal(err)
	}
	if d, err := zero.Match("x", false); err != nil || d.Action != NoMatch {
		t.Fatal("zero-value match", err)
	}
	if err := zero.Add([]Source{{
		Label: ".gitignore", Rank: RankPerDirectory, Data: []byte("*.log\n"),
	}}); err != nil {
		t.Fatal(err)
	}
	if d, err := zero.Match("a.log", false); err != nil || d.Action != Exclude {
		t.Fatal("zero-value added rule invisible")
	}
	zeroMatcher, zeroDiags := zero.Build()
	if zeroMatcher == nil || zeroDiags != nil {
		t.Fatal("zero-value build")
	}

	// Sealing: repeated builds, detached diagnostics, post-seal reads.
	b, err := NewBuilder(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Add(nil); err != nil {
		t.Fatal("open Add(nil) must be a no-op")
	}
	if err := b.Add([]Source{{Label: "root", Data: []byte("x\nbad[\n")}}); err != nil {
		t.Fatal(err)
	}
	if b.s.count != 1 {
		t.Fatal("Add(nil) consumed a sequence position")
	}
	ruleStore := &b.s.m.rules[0]
	first, diags1 := b.Build()
	second, diags2 := b.Build()
	if first == nil || first != second || &first.rules[0] != ruleStore {
		t.Fatal("Build copied or recompiled matcher storage")
	}
	if !slices.Equal(diags1, diags2) || len(diags1) != 1 || &diags1[0] == &diags2[0] {
		t.Fatal("repeated Build diagnostics not independently detached")
	}
	diags1[0].Source = "mutated"
	if second.Diagnostics()[0].Source == "mutated" {
		t.Fatal("Build diagnostics alias matcher state")
	}
	if d, err := b.Match("x", false); err != nil || d.Action != Exclude {
		t.Fatal("post-seal builder read")
	}
	if _, descend, err := b.Descend("d"); err != nil || !descend {
		t.Fatal("post-seal builder descend")
	}
	if got := b.Diagnostics(); len(got) != 1 || got[0].Source != "root" {
		t.Fatal("post-seal builder diagnostics")
	}
	if err := b.Add(nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("sealed Add(nil)")
	}
	if err := b.Add([]Source{{Data: []byte("y\n")}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("sealed Add")
	}
	if d, err := first.Match("x", false); err != nil || d.Rule == nil || d.Rule.Raw() != "x" {
		t.Fatal("published matcher altered by sealed Add")
	}

	// Copies of a handle share the seal.
	copied, err := NewBuilder(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := copied.Add([]Source{{Data: []byte("q\n")}}); err != nil {
		t.Fatal(err)
	}
	alias := *copied
	if m, diags := alias.Build(); m == nil || diags != nil {
		t.Fatal("alias build")
	}
	if err := copied.Add([]Source{{Data: []byte("r\n")}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("sealed through original handle")
	}
	if err := alias.Add(nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("sealed through copied handle")
	}

	// Empty builders build empty matchers.
	empty, err := NewBuilder(Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, diags := empty.Build()
	if m == nil || diags != nil {
		t.Fatal("empty build")
	}
	if d, err := m.Match("x", false); err != nil || d.Action != NoMatch {
		t.Fatal("empty matcher match")
	}
	if _, descend, err := m.Descend("a"); err != nil || !descend {
		t.Fatal("empty matcher descend")
	}
}

// TestBuilderOwnership proves previously published rules never change, caller
// byte mutation cannot alter rules, and Build publishes existing storage.
func TestBuilderOwnership(t *testing.T) {
	b, err := NewBuilder(Options{})
	if err != nil {
		t.Fatal(err)
	}
	sources := []Source{
		{Label: "initial", Rank: 1000, Data: []byte("keep\nblocked/\n*.tmp\n")},
		{Label: "low", Rank: -1000, Data: []byte("!keep\n")},
		{Label: "same", Rank: 1000, Data: []byte("!keep\n")},
		{Label: "wild", Rank: 0, Data: []byte("k*ep\n")},
	}
	if err := b.Add(sources[:1]); err != nil {
		t.Fatal(err)
	}
	ptrs := slices.Clone(b.s.m.rules)
	snapshot := make([]Rule, len(ptrs))
	for i, r := range ptrs {
		snapshot[i] = *r
	}
	sources[0].Data[0] = 'X'
	if ptrs[0].Raw() != "keep" {
		t.Fatal("source bytes retained")
	}
	for _, batch := range [][]Source{sources[1:2], sources[2:3], sources[3:]} {
		if err := b.Add(batch); err != nil {
			t.Fatal(err)
		}
		for i, r := range ptrs {
			if !reflect.DeepEqual(*r, snapshot[i]) {
				t.Fatalf("published rule %d mutated", i)
			}
		}
	}
	wildStore := &b.s.m.wild[0]
	ruleStore := &b.s.m.rules[0]
	m, diags := b.Build()
	if m != b.s.m || &m.rules[0] != ruleStore || &m.wild[0] != wildStore {
		t.Fatal("build copied matcher storage")
	}
	if len(diags) != 0 {
		t.Fatal("unexpected diagnostics")
	}
	if d, _ := m.Match("keep", false); d.Action != Include {
		t.Fatal("final matcher altered")
	}
}

// builderMixedSources visits four sibling branches, each traversed
// depth-first through eight scopes, so later batches interleave both across
// branches and depths.
func builderMixedSources() []Source {
	s := []Source{{Label: "root", Rank: RankPerDirectory, Data: scopedData("root", 1000)}}
	for i := range 32 {
		branch, depth := i/8, i%8
		scope := fmt.Sprintf("branch%d", branch) + strings.Repeat("/d", depth)
		s = append(s, Source{Label: fmt.Sprint(i), Scope: scope, Rank: RankPerDirectory, Data: scopedData(fmt.Sprint(i), 20)})
	}
	return s
}

// TestBuilderMixedDepthDescent differentially checks match and descent
// decisions for every scope prefix after every incremental addition.
func TestBuilderMixedDepthDescent(t *testing.T) {
	s := builderMixedSources()
	b, err := NewBuilder(Options{})
	if err != nil {
		t.Fatal(err)
	}
	for n := range s {
		if err := b.Add(s[n : n+1]); err != nil {
			t.Fatal(err)
		}
		want, _, err := Compile(s[:n+1], Options{})
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(s); i++ {
			paths := []string{s[i].Scope, s[i].Scope + "/file.txt", s[i].Scope + fmt.Sprintf("/%d-literal-0", i-1)}
			for _, path := range paths {
				got, gd, ge := b.Descend(path)
				ref, rd, re := want.Descend(path)
				if (ge != nil) != (re != nil) || gd != rd || (ge == nil && builderDecisionKey(got) != builderDecisionKey(ref)) {
					t.Fatalf("descent mismatch after add %d: %q", n, path)
				}
				gm, gme := b.Match(path, false)
				rm, rme := want.Match(path, false)
				if (gme != nil) != (rme != nil) || builderDecisionKey(gm) != builderDecisionKey(rm) {
					t.Fatalf("match mismatch after add %d: %q", n, path)
				}
			}
		}
	}
}

// TestWarmArenaSizing pins the arena reuse contract: cold arenas (one-shot
// Compile and a builder's first batch) size storage exactly, warm program
// stores are reused capped at one normal batch, tiny histories keep
// proportionate sizing, and handed-out programs never gain spare capacity.
func TestWarmArenaSizing(t *testing.T) {
	// Cold single batch: chunks sized exactly to the source. The program
	// arena is marked warm only after the batch, so this batch's sizing was
	// cold and one-shot Compile behaviour is unchanged.
	c := newCompiler(false)
	rules, diagnostics := c.compile([]Source{{Data: []byte("a\nb\n[")}}, 0, defaultSourceBytes)
	if len(rules) != 3 || len(diagnostics) != 1 || len(c.rules.chunk) != 3 {
		t.Fatalf("cold chunk=%d rules=%d diagnostics=%d", len(c.rules.chunk), len(rules), len(diagnostics))
	}
	if !c.programs.warm {
		t.Fatal("program arena not warm after first batch")
	}

	// Rule chunks keep exact sizing even when warm.
	for range 4 {
		next, _ := c.compile([]Source{{Data: []byte("x\n")}}, 1, defaultSourceBytes)
		if len(next) != 1 || len(c.rules.chunk) != 1 {
			t.Fatalf("rule chunk=%d rules=%d", len(c.rules.chunk), len(next))
		}
	}
	if got := cap(c.programs.store); got > min(ruleBatch, c.programs.stored) {
		t.Fatalf("warm store capacity=%d oversized for tiny history %d", got, c.programs.stored)
	}

	// A large history warms stores to at most one full batch.
	big := newCompiler(false)
	big.compile([]Source{{Data: []byte(strings.Repeat("r\n", 300))}}, 0, defaultSourceBytes)
	if big.programs.stored != 300 {
		t.Fatalf("stored=%d", big.programs.stored)
	}
	warm, _ := big.compile([]Source{{Data: []byte("z\n")}}, 1, defaultSourceBytes)
	if got := cap(big.programs.store); got != min(ruleBatch, 300) {
		t.Fatalf("warm store capacity=%d want=%d", got, min(ruleBatch, 300))
	}

	// Warm stores stay within one batch, and every handed-out program keeps
	// exact capacity whatever its backing store.
	compiled, _ := big.compile([]Source{{Data: []byte("[a-c]*.log\n**/deep/[x-z]/\n")}}, 2, defaultSourceBytes)
	if cap(big.programs.store) > ruleBatch {
		t.Fatalf("warm store capacity=%d exceeds one batch", cap(big.programs.store))
	}
	for _, batch := range [][]*Rule{rules, warm, compiled} {
		for _, r := range batch {
			if r.invalid {
				continue
			}
			if cap(r.program) != len(r.program) {
				t.Fatalf("program capacity=%d length=%d", cap(r.program), len(r.program))
			}
		}
	}

	// Replacing a warm store must not relocate or overwrite published programs.
	a := newInstrArena(2)
	first := a.add([]instruction{{text: "first"}})
	a.warm = true
	oversized := a.add(make([]instruction, ruleBatch+1))
	if cap(a.store) != len(oversized) || first[0].text != "first" {
		t.Fatal("warm oversized store changed earlier storage or gained spare capacity")
	}
}

// TestBuilderSealedMatcherConcurrency exercises concurrent reads of the
// published matcher alongside post-seal builder reads.
func TestBuilderSealedMatcherConcurrency(t *testing.T) {
	b, err := NewBuilder(Options{CaseFold: true})
	if err != nil {
		t.Fatal(err)
	}
	sources := []Source{
		{Label: "root", Rank: RankPerDirectory, Data: []byte("/cache/*\nblocked/\n*.tmp\nOUT/\n")},
		{Label: "cache", Scope: "cache", Rank: RankPerDirectory, Data: []byte("!keep.tmp\nout/\n")},
		{Label: "deep", Scope: "cache/sub", Rank: RankPerDirectory, Data: []byte("*.log\n!keep.log\n")},
	}
	for i := range sources {
		if err := b.Add(sources[i : i+1]); err != nil {
			t.Fatal(err)
		}
	}
	m, diags := b.Build()
	if len(diags) != 0 {
		t.Fatal("unexpected diagnostics")
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 200 {
				path := []string{"cache", "cache/keep.tmp", "cache/sub/keep.log", "blocked/x", "out", "OUT"}[(g+i)%6]
				d, err := m.Match(path, i%2 == 0)
				if err != nil {
					t.Error(err)
					return
				}
				_, _, err = m.Descend(path)
				if err != nil {
					t.Error(err)
					return
				}
				if d.Rule != nil {
					_ = d.Rule.Raw() + d.Rule.Pattern()
				}
				_ = m.Diagnostics()
			}
		}(g)
	}
	wg.Wait()
	if d, err := b.Match("cache/keep.tmp", false); err != nil || d.Action != Include {
		t.Fatal("post-seal builder read damaged", err)
	}
}
