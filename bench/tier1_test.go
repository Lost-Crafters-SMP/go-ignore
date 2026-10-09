// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"testing"

	"go.lostcrafters.com/ignore/bench/internal/dataset"
	"go.lostcrafters.com/ignore/bench/internal/drivers"
)

// Tier 1 — shared in-memory functionality: compiling one ignore file and
// matching labelled queries against it. One op is one compilation or one
// query; every driver receives identical rule text and query order.

func BenchmarkTier1Compile(b *testing.B) {
	for _, rs := range manifest.RuleSets {
		for _, d := range drivers.All() {
			b.Run("rules="+rs.Name+"/driver="+d.Name(), func(b *testing.B) {
				text := []byte(rs.Rules)
				b.ReportAllocs()
				for b.Loop() {
					c, err := d.CompileRules(text)
					if err != nil {
						b.Fatal(err)
					}
					sinkDecision = c
				}
			})
		}
	}
}

func BenchmarkTier1Match(b *testing.B) {
	for _, rs := range manifest.RuleSets {
		for _, d := range drivers.All() {
			b.Run("rules="+rs.Name+"/driver="+d.Name(), func(b *testing.B) {
				c := compiledFor(b, d, rs)
				queries := rs.Queries
				n := len(queries)
				b.ReportAllocs()
				b.ResetTimer()
				i := 0
				for b.Loop() {
					q := queries[i%n]
					i++
					sinkBool = d.Match(c, q.Path, q.IsDir)
				}
			})
		}
	}
}

// BenchmarkTier1MatchSplit reports hit and miss subsets separately: rule
// evaluation often costs more when a rule actually fires (go-ignore also
// constructs provenance for hits).
func BenchmarkTier1MatchSplit(b *testing.B) {
	rs, err := manifest.RuleSet("mixed-100")
	if err != nil {
		b.Fatal(err)
	}
	split := func(want bool) []dataset.Query {
		var out []dataset.Query
		for _, q := range rs.Queries {
			if q.Ignored == want {
				out = append(out, q)
			}
		}
		return out
	}
	for _, sub := range []struct {
		name string
		qs   []dataset.Query
	}{{"hit", split(true)}, {"miss", split(false)}} {
		for _, d := range drivers.All() {
			b.Run("rules=mixed-100/"+sub.name+"/driver="+d.Name(), func(b *testing.B) {
				c := compiledFor(b, d, rs)
				queries := sub.qs
				n := len(queries)
				b.ReportAllocs()
				b.ResetTimer()
				i := 0
				for b.Loop() {
					q := queries[i%n]
					i++
					sinkBool = d.Match(c, q.Path, q.IsDir)
				}
			})
		}
	}
}
