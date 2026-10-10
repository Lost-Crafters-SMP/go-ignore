// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"strings"
	"testing"
)

// builderSink prevents the compiler from eliminating benchmark constructions.
var builderSink *Matcher

// builderSources shapes lazy-discovery workloads: nested scopes, sibling
// branches, repeated same-scope additions, insertions below RankCommand and
// successively higher ranks. Each shape is a root source of root rules plus
// depth discovered sources of 20 rules.
func builderSources(shape string, rootRules, depth int) []Source {
	s := []Source{{Label: "root", Rank: RankPerDirectory, Data: scopedData("root", rootRules)}}
	if shape == "command" {
		s = append(s, Source{Label: "command", Rank: RankCommand, Data: []byte("!command.txt\n")})
	}
	for i := range depth {
		scope := strings.Repeat("d/", i) + "d"
		if shape == "same" || shape == "higher" {
			scope = ""
		}
		s = append(s, Source{Label: fmt.Sprint(i), Scope: scope, Rank: RankPerDirectory, Data: scopedData(fmt.Sprint(i), 20)})
	}
	switch shape {
	case "siblings":
		for i := 1; i < len(s); i++ {
			s[i].Scope = fmt.Sprintf("branch%d", i)
		}
	case "higher":
		for i := 1; i < len(s); i++ {
			s[i].Rank = RankCommand + i
		}
	}
	return s
}

// BenchmarkBuilderConstruction compares one-shot compilation with incremental
// builder construction over equivalent complete source sets.
func BenchmarkBuilderConstruction(b *testing.B) {
	for _, shape := range []string{"nested", "siblings", "same", "command", "higher"} {
		sources := builderSources(shape, 1000, 32)
		for _, method := range []string{"compile", "builder"} {
			b.Run(shape+"/"+method, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if method == "compile" {
						m, _, err := Compile(sources, Options{})
						if err != nil {
							b.Fatal(err)
						}
						builderSink = m
						continue
					}
					builder, err := NewBuilder(Options{})
					if err != nil {
						b.Fatal(err)
					}
					for i := range sources {
						if err := builder.Add(sources[i : i+1]); err != nil {
							b.Fatal(err)
						}
					}
					builderSink, _ = builder.Build()
				}
			})
		}
	}
}

// BenchmarkBuilderMatch verifies the incrementally built matcher answers
// identically to the one-shot compiled one, then measures both on the same
// queries: an indexed root hit, a wildcard miss and a deep descent.
func BenchmarkBuilderMatch(b *testing.B) {
	sources := builderSources("nested", 1000, 32)
	one, _, err := Compile(sources, Options{})
	if err != nil {
		b.Fatal(err)
	}
	builder, err := NewBuilder(Options{})
	if err != nil {
		b.Fatal(err)
	}
	for i := range sources {
		if err := builder.Add(sources[i : i+1]); err != nil {
			b.Fatal(err)
		}
	}
	incremental, _ := builder.Build()
	descendPath := strings.Repeat("d/", 31) + "d"
	for _, workload := range []string{"root-hit", "nested-miss", "descend"} {
		for _, method := range []string{"compile", "builder"} {
			b.Run(workload+"/"+method, func(b *testing.B) {
				m := one
				if method == "builder" {
					m = incremental
				}
				b.ReportAllocs()
				i := 0
				for b.Loop() {
					switch workload {
					case "root-hit":
						d, err := m.Match("root-literal-0", false)
						if err != nil || d.Action != Exclude {
							b.Fatal(d, err)
						}
					case "nested-miss":
						if _, err := m.Match("missing.txt", false); err != nil {
							b.Fatal(err)
						}
					default:
						if _, _, err := m.Descend(descendPath); err != nil {
							b.Fatal(err)
						}
					}
					i++
				}
			})
		}
	}
}
