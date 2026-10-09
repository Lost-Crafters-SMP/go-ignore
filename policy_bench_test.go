// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// BenchmarkPolicyDeepChain profiles cumulative matcher recompilation at
// increasing nesting depths: each directory's matcher recompiles every
// ancestor source, so cold discovery scales quadratically in depth. The
// Raw benchmark records capture the measured cold-discovery scaling.
func BenchmarkPolicyDeepChain(b *testing.B) {
	for _, depth := range []int{8, 16, 32, 64, 128} {
		b.Run(fmt.Sprintf("depth=%d/cold", depth), func(b *testing.B) {
			root := b.TempDir()
			dir := root
			for range depth {
				dir = filepath.Join(dir, "d")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					b.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".custom"), []byte("/lit-a\n*.gen\ncache/\n**/w*.gen\nsub-thing\n"), 0o644); err != nil {
					b.Fatal(err)
				}
			}
			deepest := strings.Repeat("d/", depth) + "plain.txt"
			specs := benchSpecs()
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
	}
	b.Run("depth=128/warm", func(b *testing.B) {
		root := b.TempDir()
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
		p, err := NewPolicy(PolicyOptions{Root: root, Sources: benchSpecs()})
		if err != nil {
			b.Fatal(err)
		}
		deepest := strings.Repeat("d/", 128) + "plain.txt"
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if _, err := p.Match(deepest, false); err != nil {
				b.Fatal(err)
			}
		}
	})
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
