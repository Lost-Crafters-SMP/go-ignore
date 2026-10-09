// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestStorageLayout complements allocation-site heap profiles with exact
// structure counts. It is opt-in and never writes source or fixture data.
func TestStorageLayout(t *testing.T) {
	path := os.Getenv("IGNORE_STORAGE_RULES")
	if path == "" {
		t.Skip("set IGNORE_STORAGE_RULES to a rules file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := Compile([]Source{{Data: data}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	units, classes, keys := 0, 0, 0
	for _, r := range m.rules {
		units += len(r.program)
		for _, in := range r.program {
			if in.kind == byteClass {
				classes++
			}
		}
	}
	for _, g := range m.scopes {
		keys += len(g.base) + len(g.anchored)
	}
	t.Logf("source=%dB rules=%d rule-size=%dB instructions=%d instruction-size=%dB classes=%d scope-groups=%d literal-keys=%d wild=%d(cap=%d) rule-pointers=%d(cap=%d)",
		len(data), len(m.rules), reflect.TypeFor[Rule]().Size(), units,
		reflect.TypeFor[instruction]().Size(), classes, len(m.scopes), keys,
		len(m.wild), cap(m.wild), len(m.rules), cap(m.rules))
	// Replay the same single-source programs to count array capacity and
	// copied prefixes independently of allocator/page rounding. The test is
	// compatible with the baseline's growing store as well as stable batches.
	a := newInstrArena(len(m.rules))
	arrays, reserved, copies := 0, cap(a.store), 0
	if reserved > 0 {
		arrays++
	}
	for _, r := range m.rules {
		if r.invalid {
			continue
		}
		prog, err := compilePatternBuf(a.scratch[:0], strings.TrimPrefix(r.pattern, "/"), false)
		if err != nil {
			t.Fatal(err)
		}
		a.scratch = prog
		oldPointer, oldLength := reflect.ValueOf(a.store).Pointer(), len(a.store)
		a.add(prog)
		if reflect.ValueOf(a.store).Pointer() != oldPointer {
			arrays++
			reserved += cap(a.store)
			if len(a.store) == oldLength+len(prog) {
				copies += oldLength
			}
		}
	}
	t.Logf("program storage: arrays=%d reserved-units=%d copied-prefix-units=%d unused-units=%d unit-size=%dB",
		arrays, reserved, copies, reserved-units-copies, reflect.TypeFor[instruction]().Size())
}
