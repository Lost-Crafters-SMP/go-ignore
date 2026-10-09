// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"fmt"
	"runtime"
	"testing"

	gitpkgs "github.com/git-pkgs/gitignore"

	"go.lostcrafters.com/ignore/bench/internal/drivers"
)

// TestRetainedMemory estimates heap retained per constructed matcher with
// GC-before/after HeapAlloc deltas, medians of five trials. It is an
// approximation: runtime internals and GC timing affect absolute numbers,
// so treat it as order-of-magnitude only. Run with -v to print the table.
func TestRetainedMemory(t *testing.T) {
	measure := func(build func() any) int64 {
		var samples []int64
		for i := 0; i < 5; i++ {
			sinkDecision = nil
			runtime.GC()
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			c := build()
			sinkDecision = c
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&after)
			samples = append(samples, int64(after.HeapAlloc)-int64(before.HeapAlloc))
		}
		sinkDecision = nil
		for i := 1; i < len(samples); i++ {
			for j := i; j > 0 && samples[j] < samples[j-1]; j-- {
				samples[j], samples[j-1] = samples[j-1], samples[j]
			}
		}
		return samples[len(samples)/2]
	}
	for _, rs := range manifest.RuleSets {
		line := fmt.Sprintf("ruleset %-12s", rs.Name)
		for _, d := range drivers.All() {
			text := []byte(rs.Rules)
			retained := measure(func() any {
				c, err := d.CompileRules(text)
				if err != nil {
					t.Fatal(err)
				}
				return c
			})
			line += fmt.Sprintf("  %s=%dB", d.Name(), retained)
		}
		t.Log(line)
	}
	// Eagerly loaded tree matchers.
	for _, name := range []string{"nested-d8", "moby"} {
		tree := mustTree(t, name)
		goRetained := measure(func() any {
			p, err := treePolicy(tree, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range tree.Queries {
				if _, err := p.Match(q.Path, q.IsDir); err != nil {
					t.Fatal(err)
				}
			}
			return p
		})
		pkgRetained := measure(func() any {
			return gitpkgs.NewFromDirectory(tree.Root)
		})
		t.Logf("tree     %-12s  go-ignore(policy,warm)=%dB  git-pkgs(eager)=%dB", name, goRetained, pkgRetained)
	}
}
