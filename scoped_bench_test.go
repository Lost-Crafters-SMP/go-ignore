// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"strings"
	"testing"
)

// scopedData mixes anchored literals, basename wildcards, directory-only rules
// and cross-star wildcards so both indexed and scanned candidates are exercised.
func scopedData(tag string, n int) []byte {
	var text strings.Builder
	for i := range n {
		switch i % 4 {
		case 0:
			fmt.Fprintf(&text, "/%s-literal-%d\n", tag, i)
		case 1:
			fmt.Fprintf(&text, "*.%s%d\n", tag, i)
		case 2:
			fmt.Fprintf(&text, "sub%s%d/\n", tag, i)
		case 3:
			fmt.Fprintf(&text, "**/%s-wild-%d\n", tag, i)
		}
	}
	return []byte(text.String())
}

// BenchmarkScopedMatch measures candidate selection with many nested scoped
// sources, the ancestor-heavy workload of walking a deep repository.
func BenchmarkScopedMatch(b *testing.B) {
	for _, scopes := range []int{5, 25} {
		for _, per := range []int{20, 200} {
			for _, outcome := range []string{"miss", "hit"} {
				b.Run(fmt.Sprintf("scopes=%d/rules=%d/%s", scopes, per, outcome), func(b *testing.B) {
					sources := []Source{{Label: "root", Data: scopedData("root", per)}}
					segments := make([]string, scopes)
					for i := range segments {
						segments[i] = fmt.Sprintf("s%d", i)
						scope := strings.Join(segments[:i+1], "/")
						sources = append(sources, Source{Label: scope, Scope: scope, Rank: RankPerDirectory, Data: scopedData(fmt.Sprintf("s%d", i), per)})
					}
					path := strings.Join(segments, "/") + "/file.log"
					if outcome == "hit" {
						sources = append(sources, Source{Label: "hit", Scope: strings.Join(segments, "/"), Rank: RankCommand, Data: []byte("file.log\n")})
					}
					m, _, err := Compile(sources, Options{})
					if err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						d, err := m.Match(path, false)
						if err != nil {
							b.Fatal(err)
						}
						if (outcome == "hit") != (d.Action == Exclude) {
							b.Fatalf("outcome=%s decision=%+v", outcome, d)
						}
					}
				})
			}
		}
	}
}
