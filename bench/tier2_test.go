// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"io/fs"
	"path/filepath"
	"testing"

	gitpkgs "github.com/git-pkgs/gitignore"
	sabhiram "github.com/sabhiram/go-gitignore"

	"go.lostcrafters.com/ignore"
	"go.lostcrafters.com/ignore/bench/internal/dataset"
)

// Tier 2 — filesystem integration. go-ignore discovers lazily per directory
// chain; git-pkgs loads the whole tree eagerly (NewFromDirectory); sabhiram
// has no discovery and is measured on its supported subset only: compiling
// the root ignore file. Cold shapes therefore differ by design and are
// reported separately from the "cold + full query sweep" shape, which
// equalizes the total work per library.

func BenchmarkTier2Cold(b *testing.B) {
	for _, name := range []string{"nested-d8", "moby"} {
		tree := mustTreeB(b, name)
		deep := deepestQuery(tree.Queries)
		b.Run("tree="+tree.Name+"/driver=go-ignore", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				p, err := treePolicy(tree, true)
				if err != nil {
					b.Fatal(err)
				}
				sinkBool = policyMatch(p, deep)
			}
		})
		b.Run("tree="+tree.Name+"/driver=git-pkgs", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				m := gitpkgs.NewFromDirectory(tree.Root)
				sinkBool = m.MatchPath(deep.Path, deep.IsDir)
			}
		})
		b.Run("tree="+tree.Name+"/driver=sabhiram(root-file-only)", func(b *testing.B) {
			root := filepath.Join(tree.Root, ".gitignore")
			b.ReportAllocs()
			for b.Loop() {
				m, err := sabhiram.CompileIgnoreFile(root)
				if err != nil {
					b.Fatal(err)
				}
				sinkBool = m.MatchesPath(deep.Path)
			}
		})
	}
}

// BenchmarkTier2ColdSweep measures each library's total cost to serve the
// whole labelled query set from scratch: construction plus every query once.
func BenchmarkTier2ColdSweep(b *testing.B) {
	for _, name := range []string{"nested-d8", "moby"} {
		tree := mustTreeB(b, name)
		queries := tree.Queries
		b.Run("tree="+tree.Name+"/driver=go-ignore", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				p, err := treePolicy(tree, true)
				if err != nil {
					b.Fatal(err)
				}
				for _, q := range queries {
					sinkBool = policyMatch(p, q)
				}
			}
			b.ReportMetric(float64(len(queries))*float64(b.N)/b.Elapsed().Seconds(), "queries/s")
		})
		b.Run("tree="+tree.Name+"/driver=git-pkgs", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				m := gitpkgs.NewFromDirectory(tree.Root)
				for _, q := range queries {
					sinkBool = m.MatchPath(q.Path, q.IsDir)
				}
			}
			b.ReportMetric(float64(len(queries))*float64(b.N)/b.Elapsed().Seconds(), "queries/s")
		})
		b.Run("tree="+tree.Name+"/driver=sabhiram(root-file-only)", func(b *testing.B) {
			root := filepath.Join(tree.Root, ".gitignore")
			b.ReportAllocs()
			for b.Loop() {
				m, err := sabhiram.CompileIgnoreFile(root)
				if err != nil {
					b.Fatal(err)
				}
				for _, q := range queries {
					sinkBool = m.MatchesPath(q.Path)
				}
			}
			b.ReportMetric(float64(len(queries))*float64(b.N)/b.Elapsed().Seconds(), "queries/s")
		})
	}
}

// BenchmarkTier2Warm measures repeated matching on a fully loaded, warm
// matcher: one op is one query.
func BenchmarkTier2Warm(b *testing.B) {
	for _, name := range []string{"nested-d8", "moby"} {
		tree := mustTreeB(b, name)
		queries := tree.Queries
		n := len(queries)
		b.Run("tree="+tree.Name+"/driver=go-ignore", func(b *testing.B) {
			p := warmPolicyOnce(b, tree)
			b.ReportAllocs()
			b.ResetTimer()
			i := 0
			for b.Loop() {
				q := queries[i%n]
				i++
				sinkBool = policyMatch(p, q)
			}
		})
		b.Run("tree="+tree.Name+"/driver=git-pkgs", func(b *testing.B) {
			m := gitpkgs.NewFromDirectory(tree.Root)
			b.ReportAllocs()
			b.ResetTimer()
			i := 0
			for b.Loop() {
				q := queries[i%n]
				i++
				sinkBool = m.MatchPath(q.Path, q.IsDir)
			}
		})
		b.Run("tree="+tree.Name+"/driver=sabhiram(root-file-only)", func(b *testing.B) {
			m, err := sabhiram.CompileIgnoreFile(filepath.Join(tree.Root, ".gitignore"))
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			i := 0
			for b.Loop() {
				q := queries[i%n]
				i++
				sinkBool = m.MatchesPath(q.Path)
			}
		})
	}
}

// BenchmarkTier2Walk compares full-tree traversal with pruning. sabhiram
// has no traversal API; go-ignore and git-pkgs walk identical trees with
// minimal callbacks (path assignment plus a counter) so matcher and
// traversal cost dominate.
func BenchmarkTier2Walk(b *testing.B) {
	for _, name := range []string{"nested-d8", "moby"} {
		tree := mustTreeB(b, name)
		b.Run("tree="+tree.Name+"/driver=go-ignore", func(b *testing.B) {
			p := warmPolicyOnce(b, tree)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				n := 0
				err := p.Walk("", func(rel string, isDir bool, d ignore.PolicyDecision) error {
					sinkString = rel
					n++
					return nil
				})
				if err != nil {
					b.Fatal(err)
				}
				sinkInt = n
			}
			b.ReportMetric(float64(sinkInt)*float64(b.N)/b.Elapsed().Seconds(), "entries/s")
		})
		b.Run("tree="+tree.Name+"/driver=git-pkgs", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				n := 0
				err := gitpkgs.Walk(tree.Root, func(path string, d fs.DirEntry) error {
					sinkString = path
					n++
					return nil
				})
				if err != nil {
					b.Fatal(err)
				}
				sinkInt = n
			}
			b.ReportMetric(float64(sinkInt)*float64(b.N)/b.Elapsed().Seconds(), "entries/s")
		})
	}
}

func mustTreeB(b *testing.B, name string) dataset.Tree {
	b.Helper()
	tree, err := manifest.Tree(name)
	if err != nil {
		b.Fatal(err)
	}
	return tree
}
