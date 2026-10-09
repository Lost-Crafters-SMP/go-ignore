// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Fuzz policy composition over adversarial query paths, and
// compilation of arbitrary source bytes into multi-source matchers. Both
// targets assert invariants (no panics, determinism, validation, model
// equivalence) rather than fixed outputs.

// fuzzTree is built once per process: fuzz workers each trigger the same
// sync.Once and share the snapshot policy and its independent models.
var (
	fuzzOnce   sync.Once
	fuzzRoot   string
	fuzzPolicy *Policy
	fuzzModels map[string]*Matcher
	fuzzSetup  error
	// fuzzDotNormalizes reports whether the host filesystem normalizes
	// trailing dots and spaces in path components away (Win32 semantics);
	// such query paths are unanswerable against the independent models.
	fuzzDotNormalizes bool
)

const fuzzReserved = ".git"

func fuzzPolicySetup() {
	fuzzRoot, fuzzSetup = os.MkdirTemp("", "ignore-fuzz-")
	if fuzzSetup != nil {
		return
	}
	files := map[string]string{
		".custom":     "*.log\n!keep.log\nbuild/\n[[:digit:]]?\n",
		"a/.custom":   "!x.log\nlit\n",
		"a/b/.custom": "**/w*\n",
		"a/res/":      "",
	}
	for name, data := range files {
		full := filepath.Join(fuzzRoot, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			fuzzSetup = os.MkdirAll(full, 0o755)
		} else {
			fuzzSetup = os.MkdirAll(filepath.Dir(full), 0o755)
			if fuzzSetup == nil {
				fuzzSetup = os.WriteFile(full, []byte(data), 0o644)
			}
		}
		if fuzzSetup != nil {
			return
		}
	}
	fuzzPolicy, fuzzSetup = NewPolicy(PolicyOptions{
		Root:     fuzzRoot,
		Sources:  []SourceSpec{{Filename: ".custom", Rank: RankPerDirectory, Optional: true, Nested: true}},
		Reserved: []string{fuzzReserved, "a/res"},
	})
	if fuzzSetup != nil {
		return
	}
	// Probe Win32 trailing-dot normalization: "a./.custom" resolves to
	// a/.custom only when the filesystem strips trailing dots and spaces.
	if _, err := os.ReadFile(filepath.Join(fuzzRoot, "a.", ".custom")); err == nil {
		fuzzDotNormalizes = true
	}
	// Independent cumulative models, compiled directly with explicit scopes
	// and labels, mirroring what lazy discovery must produce.
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(fuzzRoot, filepath.FromSlash(name)))
		if err != nil {
			fuzzSetup = err
			return nil
		}
		return data
	}
	root := read(".custom")
	under := read("a/.custom")
	deep := read("a/b/.custom")
	if fuzzSetup != nil {
		return
	}
	build := func(sources ...Source) *Matcher {
		m, _, err := Compile(sources, Options{})
		if err != nil {
			fuzzSetup = err
			return nil
		}
		return m
	}
	fuzzModels = map[string]*Matcher{
		"": build(Source{Label: ".custom", Data: root}),
	}
	if fuzzSetup != nil {
		return
	}
	fuzzModels["a"] = build(
		Source{Label: ".custom", Data: root},
		Source{Label: "a/.custom", Scope: "a", Data: under},
	)
	if fuzzSetup != nil {
		return
	}
	fuzzModels["a/b"] = build(
		Source{Label: ".custom", Data: root},
		Source{Label: "a/.custom", Scope: "a", Data: under},
		Source{Label: "a/b/.custom", Scope: "a/b", Data: deep},
	)
}

func FuzzPolicyComposition(f *testing.F) {
	seeds := []struct {
		path  string
		isDir bool
	}{
		{"a/b/x.log", false}, {"keep.log", true}, {"5", false}, {"a/lit", false},
		{"a/b/wild.txt", false}, {"a/res/f", false}, {".git/config", false},
		{"a/b/build/x", false}, {"", false}, {"/abs", false}, {"a//b", false},
		{"..", false}, {"a/./b", false}, {"a/", true}, {"a\x00b", false},
		{"A/B/X.LOG", false}, {"a/b/", true}, {"\\back\\slash", false},
		{"a/b/😀.tmp", false}, {"a/b/café.log", false}, {"a/#name", false},
	}
	for _, s := range seeds {
		f.Add(s.path, s.isDir)
	}
	f.Cleanup(func() {
		if fuzzRoot != "" {
			_ = os.RemoveAll(fuzzRoot)
		}
	})
	f.Fuzz(func(t *testing.T, path string, isDir bool) {
		// Discovery follows host open semantics: on case-insensitive
		// filesystems, querying "A/0/x" reads a/.custom for scope "A".
		// Normalising the case aligns the independent models with
		// whichever case sensitivity the host filesystem has.
		path = strings.ToLower(path)
		// Documented query form is slash-relative. Backslash components are
		// legal POSIX bytes, but Windows resolves them as separators during
		// discovery, so composition fuzzing stays within the documented
		// form; the in-memory fuzz targets cover backslash bytes.
		if strings.ContainsRune(path, '\\') {
			t.Skip()
		}
		invalid := strings.IndexByte(path, 0) >= 0 || !validRelPath(path)
		if !invalid && fuzzDotNormalizes {
			for part := range strings.SplitSeq(path, "/") {
				if last := part[len(part)-1]; last == '.' || last == ' ' {
					t.Skip()
				}
			}
		}
		fuzzOnce.Do(fuzzPolicySetup)
		if fuzzSetup != nil {
			t.Skipf("setup: %v", fuzzSetup)
		}
		first, err := fuzzPolicy.Match(path, isDir)
		second, err2 := fuzzPolicy.Match(path, isDir)
		// Rule pointers are stable (they point into matcher storage); Block
		// values are allocated per call, so only their content is compared.
		sameBlock := (first.Block == nil) == (second.Block == nil) &&
			(first.Block == nil || first.Block.Directory == second.Block.Directory && first.Block.Rule == second.Block.Rule)
		if (err == nil) != (err2 == nil) || err == nil && (first.Action != second.Action || first.Rule != second.Rule || !sameBlock || first.Reserved != second.Reserved) {
			t.Fatalf("nondeterministic: (%+v,%v) vs (%+v,%v)", first, err, second, err2)
		}
		if strings.IndexByte(path, 0) >= 0 || !validRelPath(path) {
			if err == nil {
				t.Fatalf("accepted invalid path %q", path)
			}
			return
		}
		if err != nil {
			t.Fatalf("valid path %q: %v", path, err)
		}
		if reservedRelPath(path) {
			if !first.Reserved || first.Action != Exclude || first.Rule != nil {
				t.Fatalf("reserved %q: %+v", path, first)
			}
			return
		}
		// Map the query's directory onto the deepest model: sources exist
		// only at "", "a" and "a/b", so deeper directories accumulate
		// nothing and the "a/b" model covers them.
		dir := ""
		if at := strings.LastIndexByte(path, '/'); at >= 0 {
			dir = path[:at]
		}
		model := fuzzModels[""]
		if dir == "a" || strings.HasPrefix(dir, "a/") {
			model = fuzzModels["a"]
			if dir == "a/b" || strings.HasPrefix(dir, "a/b/") {
				model = fuzzModels["a/b"]
			}
		}
		want, err := model.Match(path, isDir)
		if err != nil {
			t.Fatal(err)
		}
		if first.Action != want.Action || first.Ignored() != want.Ignored() {
			t.Fatalf("path=%q dir=%q policy=%+v model=%+v", path, dir, first, want)
		}
		if (first.Rule == nil) != (want.Rule == nil) {
			t.Fatalf("rule presence path=%q: %+v vs %+v", path, first.Rule, want.Rule)
		}
		if first.Rule != nil && (first.Rule.Source() != want.Rule.Source() || first.Rule.Line() != want.Rule.Line() || verboseRule(first.Rule) != verboseRule(want.Rule)) {
			t.Fatalf("provenance path=%q: %+v vs %+v", path, first.Rule, want.Rule)
		}
		if (first.Block == nil) != (want.Block == nil) {
			t.Fatalf("block presence path=%q: %+v vs %+v", path, first.Block, want.Block)
		}
		if first.Block != nil && (first.Block.Directory != want.Block.Directory || verboseRule(first.Block.Rule) != verboseRule(want.Block.Rule)) {
			t.Fatalf("block path=%q: %+v vs %+v", path, first.Block, want.Block)
		}
	})
}

// validRelPath reports whether a slash-relative path passes input validation.
func validRelPath(path string) bool {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return false
	}
	for part := range strings.SplitSeq(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// reservedRelPath reports whether the path or an ancestor is reserved by the
// fuzz policy.
func reservedRelPath(path string) bool {
	if path == fuzzReserved || strings.HasPrefix(path, fuzzReserved+"/") {
		return true
	}
	if path == "a/res" || strings.HasPrefix(path, "a/res/") {
		return true
	}
	return false
}

// FuzzCompileArbitrary compiles arbitrary bytes as multiple ranked, scoped
// sources and checks compile determinism and match stability.
func FuzzCompileArbitrary(f *testing.F) {
	f.Add([]byte("*.log\n!keep\n"), "x.log", byte(0))
	f.Add([]byte("\x00\n"), "a", byte(255))
	f.Add([]byte("[a-\n]\\"), "]", byte(7))
	f.Add([]byte("/cache/*\n!/cache/keep/\n/cache/keep/*\n!/cache/keep/README.md\n"), "cache/keep/README.md", byte(0))
	f.Add([]byte("[[:digit:]A-F][![:digit:]].dat\n"), "Aq.dat", byte(4))
	f.Add([]byte("report\\[final\\].txt\ntail\\ \\ \n"), "report[final].txt", byte(8))
	f.Fuzz(func(t *testing.T, data []byte, path string, mode byte) {
		if len(data) > 4096 || len(path) > 512 {
			return
		}
		mid := len(data) / 2
		scope := ""
		if mode&4 != 0 {
			scope = "a"
		}
		rank := RankPerDirectory
		if mode&8 != 0 {
			rank = RankInfoExclude
		}
		sources := []Source{
			{Label: "one", Data: data[:mid]},
			{Label: "two", Scope: scope, Rank: rank, Data: data[mid:]},
		}
		opts := Options{CaseFold: mode&1 != 0}
		m1, diags1, err1 := Compile(sources, opts)
		m2, diags2, err2 := Compile(sources, opts)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("compile divergence: %v vs %v", err1, err2)
		}
		if err1 != nil {
			if m1 != nil || m2 != nil {
				t.Fatal("matcher alongside error")
			}
			return
		}
		if len(diags1) != len(diags2) {
			t.Fatal("diagnostic divergence")
		}
		for i := range diags1 {
			if diags1[i] != diags2[i] {
				t.Fatalf("diagnostic[%d] %+v vs %+v", i, diags1[i], diags2[i])
			}
		}
		isDir := mode&2 != 0
		d1, e1 := m1.Match(path, isDir)
		d2, e2 := m2.Match(path, isDir)
		// Separate matchers store separate rule objects; compare by value.
		ruleEqual := (d1.Rule == nil) == (d2.Rule == nil) &&
			(d1.Rule == nil || d1.Rule.Source() == d2.Rule.Source() && d1.Rule.Line() == d2.Rule.Line() && verboseRule(d1.Rule) == verboseRule(d2.Rule))
		blockEqual := (d1.Block == nil) == (d2.Block == nil) &&
			(d1.Block == nil || d1.Block.Directory == d2.Block.Directory && verboseRule(d1.Block.Rule) == verboseRule(d2.Block.Rule))
		if (e1 == nil) != (e2 == nil) || e1 == nil && (d1.Action != d2.Action || d1.Ignored() != d2.Ignored() || !ruleEqual || !blockEqual) {
			t.Fatalf("match divergence: (%+v,%v) vs (%+v,%v)", d1, e1, d2, e2)
		}
	})
}
