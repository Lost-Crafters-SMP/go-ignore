// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// buildNestedPolicies materialises a realistic nested-policy tree: every
// directory carries one small mixed source (anchored literal, basename
// wildcard, directory-only and cross-star rules) plus candidate files.
func buildNestedPolicies(tb testing.TB, root string, depth, breadth int) {
	tb.Helper()
	var rec func(dir string, level int)
	rec = func(dir string, level int) {
		rules := fmt.Sprintf("/anchored-%d\n*.gen\ncache/\n**/w%d.gen\n", level, level)
		if err := os.WriteFile(filepath.Join(dir, ".custom"), []byte(rules), 0o644); err != nil {
			tb.Fatal(err)
		}
		for _, name := range []string{"plain.txt", "x.gen"} {
			if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
				tb.Fatal(err)
			}
		}
		cache := filepath.Join(dir, "cache")
		if err := os.MkdirAll(cache, 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cache, "inner"), nil, 0o644); err != nil {
			tb.Fatal(err)
		}
		if level == depth {
			return
		}
		for i := range breadth {
			child := filepath.Join(dir, fmt.Sprintf("n%d", i))
			if err := os.MkdirAll(child, 0o755); err != nil {
				tb.Fatal(err)
			}
			rec(child, level+1)
		}
	}
	rec(root, 0)
}

const benchDepth, benchBreadth = 4, 3 // 121 directories, 121 sources

func benchSpecs() []SourceSpec {
	return []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}}
}

func benchPath(parts ...string) string {
	out := ""
	for _, p := range parts {
		out += p + "/"
	}
	return out[:len(out)-1]
}

func BenchmarkPolicyMatch(b *testing.B) {
	root := b.TempDir()
	buildNestedPolicies(b, root, benchDepth, benchBreadth)
	deep := benchPath("n0", "n0", "n0", "n0")
	for _, bench := range []struct {
		name  string
		cold  bool
		path  string
		want  Action
		isDir bool
	}{
		{"cold-miss", true, deep + "/plain.txt", NoMatch, false},
		{"cold-hit", true, deep + "/x.gen", Exclude, false},
		{"warm-miss", false, deep + "/plain.txt", NoMatch, false},
		{"warm-hit", false, deep + "/x.gen", Exclude, false},
		{"warm-dir", false, deep + "/cache", Exclude, true},
	} {
		b.Run(bench.name, func(b *testing.B) {
			p, err := NewPolicy(PolicyOptions{Root: root, Sources: benchSpecs()})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if bench.cold {
					if p, err = NewPolicy(PolicyOptions{Root: root, Sources: benchSpecs()}); err != nil {
						b.Fatal(err)
					}
				}
				d, err := p.Match(bench.path, bench.isDir)
				if err != nil {
					b.Fatal(err)
				}
				if d.Action != bench.want {
					b.Fatalf("action %d want %d", d.Action, bench.want)
				}
			}
		})
	}
}

// deepChainOnce builds a 128-level chain, shared across benchmark
// invocations: b.Loop re-executes the surrounding function while ramping,
// so per-invocation setup would otherwise pollute allocs/op (this is how
// the pre-optimisation "warm" numbers were contaminated).
var deepChainOnce struct {
	sync.Once
	root string
}

func deepChainTree(b *testing.B) string {
	b.Helper()
	deepChainOnce.Do(func() {
		root, err := os.MkdirTemp("", "ignore-deepchain-")
		if err != nil {
			b.Fatal(err)
		}
		dir := root
		for range 128 {
			dir = filepath.Join(dir, "d")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".custom"), []byte("/lit-a\n*.gen\ncache/\n**/w*.gen\nsub-thing\n"), 0o644); err != nil {
				b.Fatal(err)
			}
		}
		deepChainOnce.root = root
	})
	return deepChainOnce.root
}

// BenchmarkPolicyDeepChain measures cold discovery and warm matching over a
// shared deep chain. Cold creates a fresh Policy per iteration (discovery
// plus per-directory compilation); warm reuses one Policy after one load.
// With per-directory matchers both scale linearly in depth; the former
// cumulative design measured quadratic cold cost.
func BenchmarkPolicyDeepChain(b *testing.B) {
	root := deepChainTree(b)
	for _, depth := range []int{8, 16, 32, 64, 128} {
		deepest := strings.Repeat("d/", depth) + "plain.txt"
		specs := benchSpecs()
		b.Run(fmt.Sprintf("depth=%d/cold", depth), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				p, err := NewPolicy(PolicyOptions{Root: root, Sources: specs})
				if err != nil {
					b.Fatal(err)
				}
				if _, err := p.Match(deepest, false); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("depth=%d/warm", depth), func(b *testing.B) {
			p, err := NewPolicy(PolicyOptions{Root: root, Sources: specs})
			if err != nil {
				b.Fatal(err)
			}
			if _, err := p.Match(deepest, false); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := p.Match(deepest, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPolicyWalk(b *testing.B) {
	root := b.TempDir()
	buildNestedPolicies(b, root, benchDepth, benchBreadth)
	for _, bench := range []struct {
		name string
		cold bool
	}{
		{"discover", true},
		{"cached", false},
	} {
		b.Run(bench.name, func(b *testing.B) {
			p, err := NewPolicy(PolicyOptions{Root: root, Sources: benchSpecs()})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if bench.cold {
					if p, err = NewPolicy(PolicyOptions{Root: root, Sources: benchSpecs()}); err != nil {
						b.Fatal(err)
					}
				}
				visits, excluded := 0, 0
				err := p.Walk("", func(_ string, _ bool, d PolicyDecision) error {
					visits++
					if d.Ignored() {
						excluded++
					}
					return nil
				})
				if err != nil {
					b.Fatal(err)
				}
				// 120 tree dirs + 121 cache dirs + 121 .custom +
				// 242 candidates; every cache/inner is pruned.
				// Ignored: 121 pruned cache dirs + 121 x.gen files.
				if visits != 120+121+121+242 || excluded != 121+121 {
					b.Fatalf("visits=%d excluded=%d", visits, excluded)
				}
			}
		})
	}
}
