// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"

	"go.lostcrafters.com/ignore"
)

// TestRetainedHeapProfile is opt-in: run one workload per process with
// IGNORE_HEAP_DIR and IGNORE_HEAP_RULESET set. Profile at rate 1 from process
// startup (-memprofilerate=1), then subtract empty.prof from live.prof to
// separate the live matcher from harness/runtime storage. Neither profile
// includes a competitor matcher.
func TestRetainedHeapProfile(t *testing.T) {
	dir := os.Getenv("IGNORE_HEAP_DIR")
	if dir == "" {
		t.Skip("set IGNORE_HEAP_DIR to write retained-heap profiles")
	}
	name := os.Getenv("IGNORE_HEAP_RULESET")
	var data []byte
	if name == "literal-short" || name == "literal-long" || name == "class-heavy" {
		var text strings.Builder
		for i := range 1000 {
			fmt.Fprintf(&text, "%04d", i)
			switch name {
			case "literal-long":
				text.WriteString(strings.Repeat("x", 1020))
			case "literal-short":
				text.WriteString("xxxx")
			case "class-heavy":
				text.WriteString(strings.Repeat("[a-z]", 8))
			}
			text.WriteByte('\n')
		}
		data = []byte(text.String())
	} else {
		rs, err := manifest.RuleSet(name)
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(rs.Rules)
	}
	write := func(name string) {
		t.Helper()
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.WriteHeapProfile(f); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	runtime.GC()
	write("empty.prof")
	runtime.GC()
	runtime.GC()
	var before, compiled, after runtime.MemStats
	runtime.ReadMemStats(&before)
	m, diagnostics, err := ignore.Compile([]ignore.Source{{Label: name, Data: data}}, ignore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&compiled)
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	write("live.prof")
	t.Logf("%s: retained=%dB objects=%d compile=%dB allocations=%d diagnostics=%d",
		name, int64(after.HeapAlloc)-int64(before.HeapAlloc),
		int64(after.HeapObjects)-int64(before.HeapObjects),
		compiled.TotalAlloc-before.TotalAlloc, compiled.Mallocs-before.Mallocs, len(diagnostics))
	runtime.KeepAlive(m)
	runtime.KeepAlive(data)
}
