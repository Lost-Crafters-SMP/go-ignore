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

// TestWalkerEquivalence verifies the walker's included-file discovery
// against Git's own traversal on the walk benchmarks' trees, with the
// competitor walker as a secondary cross-check.
//
// The authoritative oracle is `git ls-files --cached --others
// --exclude-standard` minus `git ls-files --cached --ignored
// --exclude-standard`: every file Git's traversal discovers as not
// ignored (tracked files plus untracked files surviving the exclude
// stack, less tracked files the rules themselves exclude, which git keeps
// by index precedence but a rule-matching walker has no index for), minus
// the reserved .git namespace. The walker must discover
// exactly that set: it prunes precisely where Git prunes (reserved
// paths, excluded strict ancestors and bare-directory self-exclusions)
// and descends through directories excluded only in canonical slash form
// (contents globs such as bin/*), exactly as git status traversal does. A
// wrongly pruned directory therefore shows up as missing files. The
// slash-form corner this test once accepted (go-ignore pruning
// directories that git status descends into) fails this oracle now.
//
// Directory entries are deliberately not compared. go-ignore reports
// every traversable directory, including report-then-prune for blocked
// ones, with the canonical slash-form decision of its documented API;
// git-pkgs silently skips ignored directories and has no canonical
// directory semantics. Those callback semantics are verified against Git
// itself (check-ignore and git status) by the main module's differential
// tests, not by cross-library entry-set equality.
func TestWalkerEquivalence(t *testing.T) {
	for _, name := range []string{"nested-d8", "moby"} {
		t.Run(name, func(t *testing.T) {
			tree := mustTree(t, name)
			goFiles := map[string]bool{}
			err := warmPolicyOnce(t, tree).Walk("", func(rel string, isDir bool, d ignore.PolicyDecision) error {
				if !isDir && !d.Ignored() && rel != ".git" {
					goFiles[rel] = true
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			cache, err := dataset.CacheDir()
			if err != nil {
				t.Fatal(err)
			}
			gitFiles, err := oracle.ListNonIgnored(tree.Root, cache)
			if err != nil {
				t.Fatal(err)
			}
			if onlyGo := diffSets(goFiles, gitFiles); len(onlyGo) > 0 {
				t.Errorf("walker discovered %d files Git traversal did not: %s", len(onlyGo), firstN(onlyGo, 5))
			}
			if missed := diffSets(gitFiles, goFiles); len(missed) > 0 {
				t.Errorf("walker missed %d files Git traversal discovered: %s", len(missed), firstN(missed, 5))
			}
			t.Logf("included files go-ignore=%d git=%d", len(goFiles), len(gitFiles))

			// Secondary cross-check: the competitor walker discovers the
			// same included files. Files have no directory-form split, so
			// library-specific directory conventions cannot mask a real
			// discovery divergence here.
			pkgFiles := map[string]bool{}
			err = gitpkgs.Walk(tree.Root, func(path string, d fs.DirEntry) error {
				if !d.IsDir() {
					pkgFiles[filepath.ToSlash(path)] = true
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if onlyGo := diffSets(goFiles, pkgFiles); len(onlyGo) > 0 {
				t.Errorf("go-ignore discovered %d files git-pkgs did not: %s", len(onlyGo), firstN(onlyGo, 5))
			}
			if missed := diffSets(pkgFiles, goFiles); len(missed) > 0 {
				t.Errorf("go-ignore missed %d files git-pkgs discovered: %s", len(missed), firstN(missed, 5))
			}
		})
	}
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
