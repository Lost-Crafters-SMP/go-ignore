// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkClassStorage exercises class-set reads directly, without literal
// head/tail rejection concealing the cost of moving bitsets out of line.
func BenchmarkClassStorage(b *testing.B) {
	for _, n := range []int{8, 64} {
		m, _, err := Compile([]Source{{Data: []byte(strings.Repeat("[a-z]", n))}}, Options{})
		if err != nil {
			b.Fatal(err)
		}
		for _, hit := range []bool{true, false} {
			b.Run(fmt.Sprintf("classes=%d/hit=%v", n, hit), func(b *testing.B) {
				path := strings.Repeat("a", n)
				if !hit {
					path = strings.Repeat("a", n-1) + "0"
				}
				b.ReportAllocs()
				for b.Loop() {
					d, err := m.Match(path, false)
					if err != nil || d.Ignored() != hit {
						b.Fatalf("decision=%v err=%v", d, err)
					}
				}
			})
		}
	}
}
