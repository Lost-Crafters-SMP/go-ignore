// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkLiteralIndexTradeoff isolates index lookup from exhaustive matching
// on the same matcher and hit/miss sequence; no competing implementation is
// involved. These paths have no strict ancestor, so both paths allocate zero.
func BenchmarkLiteralIndexTradeoff(b *testing.B) {
	for _, n := range []int{100, 1000} {
		var text strings.Builder
		for i := range n {
			fmt.Fprintf(&text, "file-%04d.log\n", i)
		}
		m, _, err := Compile([]Source{{Data: []byte(text.String())}}, Options{})
		if err != nil {
			b.Fatal(err)
		}
		paths := []string{"missing.log", "file-0000.log", fmt.Sprintf("file-%04d.log", n-1)}
		for _, indexed := range []bool{true, false} {
			b.Run(fmt.Sprintf("rules=%d/indexed=%v", n, indexed), func(b *testing.B) {
				b.ReportAllocs()
				i := 0
				for b.Loop() {
					path := paths[i%len(paths)]
					i++
					if indexed {
						_, err = m.Match(path, false)
					} else {
						_, err = refMatch(m, path, false)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestStableInstructionBatches(t *testing.T) {
	a := newInstrArena(1)
	var rules []*Rule
	var original []*instruction
	for _, pattern := range []string{"one", "two*three", "", "four[0-9]five", "six**seven", strings.Repeat("*a", ruleBatch+5)} {
		prog, err := compilePatternBuf(nil, pattern, false)
		if err != nil {
			t.Fatal(err)
		}
		previous := a.store
		stored := a.add(prog)
		if len(prog) > cap(previous)-len(previous) && len(a.store) != len(prog) {
			t.Fatal("new batch copied an earlier program prefix")
		}
		if len(prog) > 0 {
			want := stored[0]
			prog[0] = instruction{kind: anyByte}
			if stored[0] != want {
				t.Fatal("stored program aliases compilation scratch")
			}
		}
		rules = append(rules, &Rule{program: stored})
		if len(stored) > 0 {
			original = append(original, &stored[0])
		} else {
			original = append(original, nil)
		}
	}
	if len(a.store) <= ruleBatch || cap(a.store) != len(a.store) {
		t.Fatal("oversized program did not get an exact-size batch")
	}
	rules = append(rules, &Rule{invalid: true})
	for i, r := range rules {
		if r.invalid {
			if r.program != nil {
				t.Fatal("invalid rule acquired a program")
			}
			continue
		}
		if cap(r.program) != len(r.program) {
			t.Fatal("program has spare capacity")
		}
		if len(r.program) > 0 && &r.program[0] != original[i] {
			t.Fatal("handed-out program moved")
		}
	}
	for i, path := range []string{"one", "twoXthree", "", "four7five", "sixXXseven", strings.Repeat("a", ruleBatch+5)} {
		if !runProgram(rules[i].program, path, false, false, nil) {
			t.Fatalf("batched program %d does not match %q", i, path)
		}
	}
}

func TestInstructionBatchesAcrossSources(t *testing.T) {
	for _, fold := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "fold"}[fold], func(t *testing.T) {
			m, diagnostics, err := Compile([]Source{
				{Label: "high", Rank: 900, Data: []byte("[a-c].txt\n[a][\n!\n")},
				{Label: "low", Rank: -900, Data: []byte(strings.Repeat("other*\n", 100) + "[x-z].txt\n")},
			}, Options{CaseFold: fold})
			if err != nil || len(diagnostics) != 1 {
				t.Fatalf("compile: diagnostics=%v err=%v", diagnostics, err)
			}
			for _, path := range []string{"b.txt", "y.txt"} {
				if fold {
					path = strings.ToUpper(path)
				}
				d, err := m.Match(path, false)
				if err != nil || !d.Ignored() {
					t.Fatalf("match %q: %v %v", path, d, err)
				}
			}
		})
	}
}
