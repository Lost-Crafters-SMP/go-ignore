// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAdversaryWorker(t *testing.T) {
	mode := os.Getenv("IGNORE_ADVERSARY")
	if mode == "" {
		t.Skip("child-only scaling probe")
	}
	stars, _ := strconv.Atoi(os.Getenv("IGNORE_STARS"))
	n, _ := strconv.Atoi(os.Getenv("IGNORE_LENGTH"))
	pattern, path := strings.Repeat("*a", stars)+"b", strings.Repeat("a", n)
	if mode == "class" {
		pattern = strings.Repeat("*[a-c]", stars) + "z"
	}
	if mode == "directory" {
		pattern = strings.Repeat("**/a/", stars) + "z"
		path = strings.Repeat("a/", n/2) + "a"
	}
	program, err := compilePattern(pattern, false)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for range 2000 {
		if runProgram(program, path, false, false) {
			t.Fatal("near miss matched")
		}
	}
	t.Logf("mode=%s stars=%d length=%d iterations=2000 elapsed=%s", mode, stars, n, time.Since(start))
}

func TestKillableScaling(t *testing.T) {
	if os.Getenv("IGNORE_RESOURCE") != "1" {
		t.Skip("set IGNORE_RESOURCE=1")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"star", "class", "directory"} {
		for _, stars := range []int{8, 16, 24} {
			for _, n := range []int{64, 256, 1024} {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				cmd := exec.CommandContext(ctx, exe, "-test.run=^TestAdversaryWorker$", "-test.v")
				cmd.Env = append(os.Environ(), "IGNORE_ADVERSARY="+mode, "IGNORE_STARS="+strconv.Itoa(stars), "IGNORE_LENGTH="+strconv.Itoa(n))
				out, err := cmd.CombinedOutput()
				cancel()
				if err != nil {
					t.Fatalf("killable child %s %d %d: %v %s", mode, stars, n, err, out)
				}
				t.Log(strings.TrimSpace(string(out)))
			}
		}
	}
}

func TestRetainedLiteralMemory(t *testing.T) {
	if os.Getenv("IGNORE_RESOURCE") != "1" {
		t.Skip("set IGNORE_RESOURCE=1")
	}
	for _, escaped := range []bool{false, true} {
		data := []byte(strings.Repeat("a", 1<<20))
		if escaped {
			data = []byte(strings.Repeat("\\a", 1<<19))
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		m, _, err := Compile([]Source{{Data: data}}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
		if retained > int64(8*len(data)+64*1024) {
			t.Fatalf("literal retention=%d limit=%d", retained, 8*len(data)+64*1024)
		}
		if len(m.rules) != 1 || len(m.rules[0].program) != 1 {
			t.Fatal("literal byte amplification")
		}
		t.Logf("1MiB source escaped=%v retained-heap=%d total-allocation=%d mallocs=%d units=%d", escaped, retained, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs, len(m.rules[0].program))
		runtime.KeepAlive(m)
		runtime.KeepAlive(data)
	}
}

func TestMeasuredAllocations(t *testing.T) {
	for _, c := range []struct {
		text, path string
		dir        bool
	}{{"*.log", "a/b.log", false}, {"!keep", "a/keep", false}, {"x", "a/b", false}, {"d/**", "d", true}, {"d/", "d/file", false}} {
		m := mustCompile(t, c.text, Options{})
		allocations := testing.AllocsPerRun(1000, func() {
			_, err := m.Match(c.path, c.dir)
			if err != nil {
				panic(err)
			}
		})
		t.Logf("pattern=%q path=%q dir=%v allocs/op=%g", c.text, c.path, c.dir, allocations)
	}
}

func TestDefaultCapBoundaries(t *testing.T) {
	if os.Getenv("IGNORE_RESOURCE") != "1" {
		t.Skip("set IGNORE_RESOURCE=1")
	}
	data := make([]byte, defaultSourceBytes+1)
	for i := range data {
		data[i] = '#'
	}
	for _, n := range []int64{defaultSourceBytes - 1, defaultSourceBytes, defaultSourceBytes + 1} {
		m, diags, err := Compile([]Source{{Label: "cap", Data: data[:n]}, {Label: "active", Data: []byte("live")}}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if n > defaultSourceBytes {
			want = 1
		}
		if len(diags) != want || len(m.rules) != 1 {
			t.Fatalf("cap=%d rules=%d diagnostics=%+v", n, len(m.rules), diags)
		}
		d, err := m.Match("live", false)
		if err != nil || d.Action != Exclude {
			t.Fatal(d, err)
		}
		t.Logf("default cap boundary bytes=%d wholly-skipped=%v subsequent-source-active=true", n, want == 1)
	}
}

func TestRetainedRuleSets(t *testing.T) {
	if os.Getenv("IGNORE_RESOURCE") != "1" {
		t.Skip("set IGNORE_RESOURCE=1")
	}
	for _, count := range []int{10, 100, 1000, 10000, 100000} {
		data := benchmarkData(count)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		m, _, err := Compile([]Source{{Data: data}}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		t.Logf("rules=%d input=%d retained-heap=%d total-allocation=%d mallocs=%d", count, len(data), int64(after.HeapAlloc)-int64(before.HeapAlloc), after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs)
		runtime.KeepAlive(m)
		runtime.KeepAlive(data)
	}
}

func benchmarkData(n int) []byte {
	var text strings.Builder
	for i := range n {
		fmt.Fprintf(&text, "/literal-%d\n", i)
	}
	return []byte(text.String())
}

func BenchmarkCompile(b *testing.B) {
	for _, n := range []int{10, 100, 1000, 10000, 100000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			data := benchmarkData(n)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				m, _, err := Compile([]Source{{Data: data}}, Options{})
				if err != nil {
					b.Fatal(err)
				}
				runtime.KeepAlive(m)
			}
		})
	}
}

func BenchmarkMatch(b *testing.B) {
	for _, n := range []int{10, 100, 1000, 10000} {
		for _, depth := range []int{2, 16, 128, 512} {
			for _, outcome := range []string{"miss", "exclude", "include", "block", "directory", "cross"} {
				b.Run(fmt.Sprintf("rules=%d/depth=%d/%s", n, depth, outcome), func(b *testing.B) {
					data := benchmarkData(n)
					path := strings.Repeat("a/", depth-1) + "hit"
					dir := false
					switch outcome {
					case "exclude":
						data = append(data, []byte("hit\n")...)
					case "include":
						data = append(data, []byte("!hit\n")...)
					case "block":
						data = append(data, []byte("a/\n")...)
					case "directory":
						data = append(data, []byte(path+"/*\n")...)
						dir = true
					case "cross":
						data = append(data, []byte("**/hit\n")...)
					}
					m, _, err := Compile([]Source{{Data: data}}, Options{})
					if err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						d, err := m.Match(path, dir)
						if err != nil {
							b.Fatal(err)
						}
						runtime.KeepAlive(d)
					}
				})
			}
		}
	}
}

func BenchmarkMultipleSources(b *testing.B) {
	var sources []Source
	for i := range 100 {
		sources = append(sources, Source{Label: strconv.Itoa(i), Scope: strings.Repeat("a/", i) + "a", Rank: RankPerDirectory, Data: benchmarkData(100)})
	}
	for b.Loop() {
		m, _, err := Compile(sources, Options{})
		if err != nil {
			b.Fatal(err)
		}
		runtime.KeepAlive(m)
	}
}
