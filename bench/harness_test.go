// SPDX-License-Identifier: Apache-2.0

// Package bench hosts the comparative benchmark suite. Run
// `mise exec -- go run ./cmd/benchsetup` once to materialize datasets, then use the
// documented go test -bench commands (see bench/README.md).
package bench

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"go.lostcrafters.com/ignore"
	"go.lostcrafters.com/ignore/bench/internal/dataset"
	"go.lostcrafters.com/ignore/bench/internal/drivers"
	"go.lostcrafters.com/ignore/bench/internal/oracle"
)

var manifest *dataset.Manifest

// TestMain applies the same git configuration hygiene the oracle used (so
// git-pkgs' filesystem constructors see an isolated global config, not the
// host user's) and loads the dataset manifest.
func TestMain(m *testing.M) {
	cache, err := dataset.CacheDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
	if _, err := oracle.HygieneFiles(cache); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	empty := filepath.Join(cache, "empty-config")
	os.Setenv("GIT_CONFIG_GLOBAL", empty)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(cache, "xdg"))
	manifest, err = dataset.Load(cache)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// Sinks prevent dead-code elimination across all timed loops.
var (
	sinkBool     bool
	sinkDecision any
	sinkString   string
	sinkInt      int
	sinkErr      error
)

// policyMatch reduces a policy decision to the comparable ignored verdict.
func policyMatch(p *ignore.Policy, q dataset.Query) bool {
	d, err := p.Match(q.Path, q.IsDir)
	return err == nil && d.Ignored()
}

// compiled caches one matcher per (driver, rule set), built outside any
// timed region.
var compiled sync.Map // "driver/set" -> any

func compiledFor(t testing.TB, d drivers.Driver, rs dataset.RuleSet) any {
	t.Helper()
	key := d.Name() + "/" + rs.Name
	if v, ok := compiled.Load(key); ok {
		return v
	}
	c, err := d.CompileRules([]byte(rs.Rules))
	if err != nil {
		t.Fatalf("compile %s/%s: %v", d.Name(), rs.Name, err)
	}
	compiled.Store(key, c)
	return c
}

// treePolicy builds the standard filesystem policy for a tree: nested
// .gitignore discovery with .git reserved for walker parity with libraries
// that always skip it.
func treePolicy(t dataset.Tree, reserved bool) (*ignore.Policy, error) {
	opts := ignore.PolicyOptions{
		Root: t.Root,
		Sources: []ignore.SourceSpec{
			{Filename: ".gitignore", Rank: ignore.RankPerDirectory, Optional: true, Nested: true},
		},
	}
	if reserved {
		opts.Reserved = []string{".git"}
	}
	return ignore.NewPolicy(opts)
}

// warmPolicy returns a policy with discovery already performed over the
// tree's queries.
func warmPolicy(t testing.TB, tree dataset.Tree) *ignore.Policy {
	t.Helper()
	p, err := treePolicy(tree, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range tree.Queries {
		if _, err := p.Match(q.Path, q.IsDir); err != nil {
			t.Fatalf("warm %s %q: %v", tree.Name, q.Path, err)
		}
	}
	return p
}

var warmPolicies sync.Map // tree name -> *ignore.Policy

func warmPolicyOnce(t testing.TB, tree dataset.Tree) *ignore.Policy {
	t.Helper()
	if v, ok := warmPolicies.Load(tree.Name); ok {
		return v.(*ignore.Policy)
	}
	p := warmPolicy(t, tree)
	warmPolicies.Store(tree.Name, p)
	return p
}

// deepestQuery returns the query with the most path components (ties broken
// by order), used to drive cold chain discovery with one match.
func deepestQuery(queries []dataset.Query) dataset.Query {
	best := queries[0]
	for _, q := range queries[1:] {
		if countSlash(q.Path) > countSlash(best.Path) {
			best = q
		}
	}
	return best
}

func countSlash(p string) int { return strings.Count(p, "/") }

// cumulativeMatcher compiles every ignore file of a tree into one Matcher
// with explicit scopes: the pre-composition architecture shape.
func cumulativeMatcher(t testing.TB, tree dataset.Tree) *ignore.Matcher {
	t.Helper()
	type src struct {
		scope string
		label string
		data  []byte
	}
	var srcs []src
	err := filepath.WalkDir(tree.Root, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(tree.Root, full)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		if d.IsDir() {
			if slash == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != ".gitignore" {
			return nil
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		scope := filepath.ToSlash(filepath.Dir(rel))
		if scope == "." {
			scope = ""
		}
		srcs = append(srcs, src{scope: scope, label: slash, data: raw})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(srcs, func(a, b int) bool {
		da, db := strings.Count(srcs[a].scope, "/"), strings.Count(srcs[b].scope, "/")
		if da != db {
			return da < db
		}
		return srcs[a].scope < srcs[b].scope
	})
	sources := make([]ignore.Source, len(srcs))
	for i, s := range srcs {
		sources[i] = ignore.Source{Label: s.label, Scope: s.scope, Rank: ignore.RankPerDirectory, Data: s.data}
	}
	m, _, err := ignore.Compile(sources, ignore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
