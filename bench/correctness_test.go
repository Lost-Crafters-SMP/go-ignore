// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	gitpkgs "github.com/git-pkgs/gitignore"

	"go.lostcrafters.com/ignore"
	"go.lostcrafters.com/ignore/bench/internal/dataset"
	"go.lostcrafters.com/ignore/bench/internal/drivers"
	"go.lostcrafters.com/ignore/bench/internal/oracle"
)

// TestCorrectness compares every library's verdicts with the Git oracle
// labels recorded at setup. go-ignore claims byte-exact Git semantics and
// any mismatch fails the suite; competitor divergences are reported, not
// hidden, and disqualify the affected comparison cells rather than the
// whole benchmark.
func TestCorrectness(t *testing.T) {
	for _, rs := range manifest.RuleSets {
		t.Run("ruleset/"+rs.Name, func(t *testing.T) {
			for _, d := range drivers.All() {
				c := compiledFor(t, d, rs)
				var mism []string
				for _, q := range rs.Queries {
					if got := d.Match(c, q.Path, q.IsDir); got != q.Ignored {
						if d.DirHint() || !q.IsDir {
							mism = append(mism, fmt.Sprintf("%s: isDir=%v want=%v got=%v", q.Path, q.IsDir, q.Ignored, got))
						}
					}
				}
				// Directory queries without a dir hint are API-limited and
				// counted separately.
				dirLimited := 0
				if !d.DirHint() {
					for _, q := range rs.Queries {
						if q.IsDir {
							dirLimited++
						}
					}
				}
				if d.Name() == "go-ignore" && len(mism) > 0 {
					t.Errorf("go-ignore diverged from Git on %d/%d queries; first: %s",
						len(mism), len(rs.Queries), firstN(mism, 3))
				}
				t.Logf("%-9s mismatches=%d/%d dirLimited=%d", d.Name(), len(mism), len(rs.Queries), dirLimited)
				if len(mism) > 0 {
					t.Logf("  examples: %s", firstN(mism, 5))
				}
			}
		})
	}
}

// TestCorrectnessTrees runs the same gate over filesystem trees with each
// library's native nested-discovery constructor. sabhiram has no discovery
// support and is reported as unsupported.
func TestCorrectnessTrees(t *testing.T) {
	for _, tree := range manifest.Trees {
		t.Run("tree/"+tree.Name, func(t *testing.T) {
			// go-ignore policy.
			p := warmPolicyOnce(t, tree)
			var mism []string
			for _, q := range tree.Queries {
				d, err := p.Match(q.Path, q.IsDir)
				if err != nil {
					t.Fatalf("match %q: %v", q.Path, err)
				}
				if d.Ignored() != q.Ignored {
					mism = append(mism, fmt.Sprintf("%s: isDir=%v want=%v got=%v", q.Path, q.IsDir, q.Ignored, d.Ignored()))
				}
			}
			if len(mism) > 0 {
				t.Errorf("go-ignore diverged from Git on %d/%d tree queries; first: %s",
					len(mism), len(tree.Queries), firstN(mism, 3))
			}
			t.Logf("go-ignore  mismatches=%d/%d", len(mism), len(tree.Queries))

			// git-pkgs eager directory matcher.
			gm := gitpkgs.NewFromDirectory(tree.Root)
			mism = mism[:0]
			for _, q := range tree.Queries {
				if gm.MatchPath(q.Path, q.IsDir) != q.Ignored {
					mism = append(mism, fmt.Sprintf("%s: isDir=%v want=%v", q.Path, q.IsDir, q.Ignored))
				}
			}
			t.Logf("git-pkgs   mismatches=%d/%d", len(mism), len(tree.Queries))
			if len(mism) > 0 {
				t.Logf("  examples: %s", firstN(mism, 5))
			}
			t.Logf("sabhiram   unsupported: no nested discovery API")
		})
	}
}

// TestPairEquivalence asserts the composition pair's defining property:
// identical per-query oracle labels and identical go-ignore decisions
// between the distributed and single-source configurations.
func TestPairEquivalence(t *testing.T) {
	nested := mustTree(t, "pair-nested")
	single := mustTree(t, "pair-single")
	if len(nested.Queries) != len(single.Queries) {
		t.Fatalf("pair query lists differ: %d vs %d", len(nested.Queries), len(single.Queries))
	}
	for i := range nested.Queries {
		a, b := nested.Queries[i], single.Queries[i]
		if a.Path != b.Path || a.IsDir != b.IsDir {
			t.Fatalf("pair query %d differs: %+v vs %+v", i, a, b)
		}
		if a.Ignored != b.Ignored {
			t.Errorf("oracle labels differ for %q: nested=%v single=%v", a.Path, a.Ignored, b.Ignored)
		}
	}
	pn, ps := warmPolicyOnce(t, nested), warmPolicyOnce(t, single)
	for _, q := range nested.Queries {
		dn, err := pn.Match(q.Path, q.IsDir)
		if err != nil {
			t.Fatal(err)
		}
		ds, err := ps.Match(q.Path, q.IsDir)
		if err != nil {
			t.Fatal(err)
		}
		if dn.Ignored() != ds.Ignored() {
			t.Errorf("decisions differ for %q: nested=%v single=%v", q.Path, dn.Ignored(), ds.Ignored())
		}
	}
}

// TestWalkerEquivalence compares full-traversal entry sets on the walk
// benchmarks' trees. go-ignore reports ignored directories before pruning
// them and reports the reserved .git directory, so both are filtered for
// the comparison; git-pkgs never reports ignored or .git entries.
//
// One semantic corner is expected and verified against the oracle rather
// than failed: a directory excluded only through a non-dirOnly slash
// pattern (for example "bin/*") is pruned by go-ignore's walker, which
// follows check-ignore's trailing-slash directory verdict, while git-pkgs
// descends into it the way `git status` traversal does. For those
// directories Git itself is inconsistent between its two interfaces:
// check-ignore says "bin" is not ignored and "bin/" is ignored, and
// git status descends. Divergences outside this verified corner fail.
func TestWalkerEquivalence(t *testing.T) {
	for _, name := range []string{"nested-d8", "moby"} {
		t.Run(name, func(t *testing.T) {
			tree := mustTree(t, name)
			goSet := map[string]bool{}
			err := warmPolicyOnce(t, tree).Walk("", func(rel string, isDir bool, d ignore.PolicyDecision) error {
				if !d.Ignored() && rel != ".git" {
					goSet[rel] = isDir
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			pkgSet := map[string]bool{}
			err = gitpkgs.Walk(tree.Root, func(path string, d fs.DirEntry) error {
				pkgSet[filepath.ToSlash(path)] = d.IsDir()
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			onlyGo := diffSets(goSet, pkgSet)
			if len(onlyGo) > 0 {
				t.Errorf("go-ignore visited %d entries git-pkgs did not: %s", len(onlyGo), firstN(onlyGo, 5))
			}
			var corner, unexplained []string
			for _, p := range diffSets(pkgSet, goSet) {
				if isSlashFormCorner(t, tree, p) {
					corner = append(corner, p)
					continue
				}
				unexplained = append(unexplained, p)
			}
			t.Logf("entries go-ignore=%d git-pkgs=%d onlyGo=%d slashFormCorner=%d unexplained=%d",
				len(goSet), len(pkgSet), len(onlyGo), len(corner), len(unexplained))
			if len(corner) > 0 {
				t.Logf("  slash-form corner (go-ignore prunes, git-pkgs descends): %s", firstN(corner, 5))
			}
			if len(unexplained) > 0 {
				t.Errorf("unexplained walker divergences: %s", firstN(unexplained, 5))
			}
		})
	}
}

// isSlashFormCorner reports whether path is a directory that Git classifies
// as not-ignored without a trailing slash but ignored with one: the case
// where go-ignore's slash-form walker convention and git status traversal
// legitimately disagree.
func isSlashFormCorner(t *testing.T, tree dataset.Tree, path string) bool {
	t.Helper()
	cache, err := dataset.CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	plain, slash, err := oracle.CheckDirForms(tree.Root, cache, path)
	if err != nil {
		t.Fatal(err)
	}
	return !plain && slash
}

// TestRankAndReservedEquivalence verifies the Tier 3 configurations that
// must not change decisions: custom ranks preserving order, and reserved
// lists covering paths absent from the query set.
func TestRankAndReservedEquivalence(t *testing.T) {
	tree := mustTree(t, "nested-d8")
	standard, err := treePolicy(tree, true)
	if err != nil {
		t.Fatal(err)
	}
	custom, err := ignore.NewPolicy(ignore.PolicyOptions{
		Root: tree.Root,
		Sources: []ignore.SourceSpec{
			{Filename: ".gitignore", Rank: 283, Optional: true, Nested: true},
		},
		Reserved: []string{".git"},
	})
	if err != nil {
		t.Fatal(err)
	}
	unreserved, err := treePolicy(tree, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range tree.Queries {
		a, err := standard.Match(q.Path, q.IsDir)
		if err != nil {
			t.Fatal(err)
		}
		b, err := custom.Match(q.Path, q.IsDir)
		if err != nil {
			t.Fatal(err)
		}
		c, err := unreserved.Match(q.Path, q.IsDir)
		if err != nil {
			t.Fatal(err)
		}
		if a.Ignored() != b.Ignored() || a.Ignored() != c.Ignored() {
			t.Errorf("configuration changed decision for %q: %v %v %v", q.Path, a.Ignored(), b.Ignored(), c.Ignored())
		}
	}
}

func mustTree(t *testing.T, name string) dataset.Tree {
	t.Helper()
	tree, err := manifest.Tree(name)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func firstN(list []string, n int) string {
	sort.Strings(list)
	if len(list) > n {
		list = list[:n]
	}
	return strings.Join(list, "; ")
}

func diffSets(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}
