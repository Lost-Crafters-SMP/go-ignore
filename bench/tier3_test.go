// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"testing"

	"go.lostcrafters.com/ignore"
	"go.lostcrafters.com/ignore/bench/internal/dataset"
)

// Tier 3 — incremental feature overhead within go-ignore, measured with
// equivalent effective rules and identical query sets (the equivalence is
// asserted by TestPairEquivalence and TestRankAndReservedEquivalence).
// One op is one query.

// BenchmarkTier3MatcherVsPolicy compares the composed per-directory Policy
// against one cumulative Matcher holding the same ignore files with
// explicit scopes (the pre-composition architecture shape).
func BenchmarkTier3MatcherVsPolicy(b *testing.B) {
	for _, name := range []string{"nested-d8", "moby"} {
		tree := mustTreeB(b, name)
		queries := tree.Queries
		n := len(queries)
		b.Run("tree="+tree.Name+"/engine=cumulative-matcher", func(b *testing.B) {
			m := cumulativeMatcher(b, tree)
			b.ReportAllocs()
			b.ResetTimer()
			i := 0
			for b.Loop() {
				q := queries[i%n]
				i++
				d, err := m.Match(q.Path, q.IsDir)
				if err != nil {
					b.Fatal(err)
				}
				sinkBool = d.Ignored()
			}
		})
		b.Run("tree="+tree.Name+"/engine=composed-policy", func(b *testing.B) {
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
	}
}

// BenchmarkTier3SingleVsCompared isolates composition overhead: the same
// bare-rule block either duplicated in every directory (pair-nested) or
// placed once at the root (pair-single), with identical decisions on
// identical queries.
func BenchmarkTier3SingleVsComposed(b *testing.B) {
	for _, name := range []string{"pair-single", "pair-nested"} {
		tree := mustTreeB(b, name)
		queries := tree.Queries
		n := len(queries)
		b.Run("tree="+name, func(b *testing.B) {
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
	}
}

// BenchmarkTier3Ranks measures whether custom source ranks change warm
// matching cost. Rank values only order sources at compile time, so
// same-order custom ranks are expected to match standard-rank performance.
func BenchmarkTier3Ranks(b *testing.B) {
	tree := mustTreeB(b, "nested-d8")
	queries := tree.Queries
	n := len(queries)
	for _, spec := range []struct {
		name string
		rank int
	}{{"standard", ignore.RankPerDirectory}, {"custom", 283}} {
		b.Run("ranks="+spec.name, func(b *testing.B) {
			p, err := ignore.NewPolicy(ignore.PolicyOptions{
				Root: tree.Root,
				Sources: []ignore.SourceSpec{
					{Filename: ".gitignore", Rank: spec.rank, Optional: true, Nested: true},
				},
				Reserved: []string{".git"},
			})
			if err != nil {
				b.Fatal(err)
			}
			for _, q := range queries {
				if _, err := p.Match(q.Path, q.IsDir); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			i := 0
			for b.Loop() {
				q := queries[i%n]
				i++
				sinkBool = policyMatch(p, q)
			}
		})
	}
}

// BenchmarkTier3Reserved measures the reserved-path check: the moby query
// set contains no reserved paths, so any difference is pure check overhead.
func BenchmarkTier3Reserved(b *testing.B) {
	tree := mustTreeB(b, "moby")
	queries := tree.Queries
	n := len(queries)
	for _, spec := range []struct {
		name string
		on   bool
	}{{"reserved-on", true}, {"reserved-off", false}} {
		b.Run(spec.name, func(b *testing.B) {
			p, err := treePolicy(tree, spec.on)
			if err != nil {
				b.Fatal(err)
			}
			for _, q := range queries {
				if _, err := p.Match(q.Path, q.IsDir); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			i := 0
			for b.Loop() {
				q := queries[i%n]
				i++
				sinkBool = policyMatch(p, q)
			}
		})
	}
}

// BenchmarkTier3Depth scales the composed chain: identical rule density,
// chain depths 2/8/32.
func BenchmarkTier3Depth(b *testing.B) {
	for _, name := range []string{"nested-d2", "nested-d8", "nested-d32"} {
		tree := mustTreeB(b, name)
		queries := tree.Queries
		n := len(queries)
		b.Run("tree="+name, func(b *testing.B) {
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
	}
}

// BenchmarkTier3DecisionWorkload splits the same query set by decision
// shape on moby: ancestor-blocked, endpoint-decided and clean paths. The
// split shows where composed matching spends its time; provenance is
// intrinsic to go-ignore's hits and cannot be disabled.
func BenchmarkTier3DecisionWorkload(b *testing.B) {
	tree := mustTreeB(b, "moby")
	p := warmPolicyOnce(b, tree)
	var blocked, endpoint, clean []dataset.Query
	for _, q := range tree.Queries {
		d, err := p.Match(q.Path, q.IsDir)
		if err != nil {
			b.Fatal(err)
		}
		switch {
		case d.Block != nil:
			blocked = append(blocked, q)
		case d.Rule != nil:
			endpoint = append(endpoint, q)
		default:
			clean = append(clean, q)
		}
	}
	for _, sub := range []struct {
		name string
		qs   []dataset.Query
	}{
		{"workload=ancestor-blocked", blocked},
		{"workload=endpoint-decided", endpoint},
		{"workload=clean-miss", clean},
	} {
		queries := sub.qs
		n := len(queries)
		if n == 0 {
			continue
		}
		b.Run(sub.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			i := 0
			for b.Loop() {
				q := queries[i%n]
				i++
				sinkBool = policyMatch(p, q)
			}
		})
	}
}
