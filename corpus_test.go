// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These snapshots contain ignore data only, never matcher implementations or
// upstream test tables. Source paths and licences are pinned alongside the data:
// https://github.com/golang/go/blob/go1.26.0/.gitignore (BSD-3-Clause, go/LICENSE)
// https://github.com/microsoft/vscode/blob/1.109.0/.gitignore (MIT, vscode/LICENSE.txt)
// https://github.com/rust-lang/cargo/blob/0.85.0/.gitignore (MIT option, cargo/LICENSE-MIT)
// https://github.com/rust-lang/cargo/blob/0.85.0/src/doc/.gitignore (same licence)
// Query paths and expected records below are independently authored, not claims
// about tracked files in the original repositories.
func repositoryFixtures(t *testing.T) []fixture {
	t.Helper()
	type query struct {
		path, source, pattern, block string
		line                         int
		dir                          bool
	}
	samples := []struct {
		name    string
		files   map[string]string
		queries []query
	}{
		{"go", map[string]string{".gitignore": "gitignore"}, []query{
			{path: "lib/a.a8", source: ".gitignore", line: 3, pattern: "*.a[56789o]"},
			{path: "lib/a.a4"},
			{path: "src/p/8.out", source: ".gitignore", line: 8, pattern: "[56789a].out"},
			{path: "src/p/4.out"},
			{path: "src/p/generated.cgo2.go", source: ".gitignore", line: 15, pattern: "*.cgo*.go"},
			{path: "src/local.test", dir: true, source: ".gitignore", line: 32, pattern: "/src/*.*/"},
			{path: "src/p/local.test"},
			{path: "bin/tool", source: ".gitignore", line: 23, pattern: "/bin/", block: "bin"},
			{path: "tools/bin/tool"},
			{path: "doc/articles/wiki/demo.bin", source: ".gitignore", line: 25, pattern: "/doc/articles/wiki/*.bin"},
			{path: "doc/articles/wiki/sub/demo.bin"},
		}},
		{"vscode", map[string]string{".gitignore": "gitignore"}, []query{
			{path: "extensions/demo/out", dir: true, source: ".gitignore", line: 11, pattern: "/extensions/**/out/"},
			{path: "extensions/group/demo/out/main.js", source: ".gitignore", line: 11, pattern: "/extensions/**/out/", block: "extensions/group/demo/out"},
			{path: "extensions/demo/dist/bundle.js", source: ".gitignore", line: 8, pattern: "extensions/**/dist/", block: "extensions/demo/dist"},
			{path: "extensions/demo/src/main.ts"},
			{path: "out-client/main.js", source: ".gitignore", line: 10, pattern: "/out*/", block: "out-client"},
			{path: "src/out-client/main.js"},
			{path: "src/node_modules/pkg/index.js", source: ".gitignore", line: 5, pattern: "node_modules/", block: "src/node_modules"},
			{path: "src/node_modules"},
			{path: "test/case.snap.actual", source: ".gitignore", line: 23, pattern: "*.snap.actual"},
			{path: "test/case.snap"},
		}},
		{"cargo", map[string]string{".gitignore": "gitignore", "src/doc/.gitignore": "doc-gitignore"}, []query{
			{path: "target/debug/cargo", source: ".gitignore", line: 1, pattern: "/target", block: "target"},
			{path: "crates/demo/target/debug/cargo"},
			{path: "Cargo.lock", source: ".gitignore", line: 2, pattern: "/Cargo.lock"},
			{path: "crates/demo/Cargo.lock"},
			{path: "src/etc/build.pyc", source: ".gitignore", line: 7, pattern: "/src/etc/*.pyc"},
			{path: "src/etc/sub/build.pyc"},
			{path: "src/doc/book", dir: true, source: "src/doc/.gitignore", line: 2, pattern: "book/"},
			{path: "src/doc/book/index.html", source: "src/doc/.gitignore", line: 2, pattern: "book/", block: "src/doc/book"},
			{path: "src/doc/book"},
			{path: "src/other/book/index.html"},
			{path: "src/doc/chapter/book/index.html", source: "src/doc/.gitignore", line: 2, pattern: "book/", block: "src/doc/chapter/book"},
		}},
	}
	var result []fixture
	for _, sample := range samples {
		files := make(map[string]string, len(sample.files))
		for label, name := range sample.files {
			data, err := os.ReadFile(filepath.Join("testdata", "repositories", sample.name, name))
			if err != nil {
				t.Fatal(err)
			}
			files[label] = string(data)
		}
		for i, q := range sample.queries {
			c := fixture{
				ID: fmt.Sprintf("%s-%02d", sample.name, i), Files: files,
				Query:    fixtureQuery{Path: q.path, IsDir: q.dir},
				Expected: oracleRecord{Ignored: q.line > 0, Source: q.source, Line: q.line, Pattern: q.pattern},
			}
			if q.block != "" {
				c.Block = &fixtureBlock{Directory: q.block, Source: q.source, Line: q.line, Pattern: q.pattern}
			}
			result = append(result, c)
		}
	}
	return result
}

func compatibilityFixtures(t *testing.T) []fixture {
	t.Helper()
	var data struct {
		Schema int
		Origin string
		Cases  []fixture
	}
	readJSON(t, "testdata/compatibility.json", &data)
	if data.Schema != 1 || len(data.Cases) != 24 {
		t.Fatal("compatibility corpus accounting")
	}
	return append(data.Cases, repositoryFixtures(t)...)
}

func checkCorpusDecision(t *testing.T, d Decision, c fixture) {
	t.Helper()
	if d.Ignored() != c.Expected.Ignored {
		t.Fatalf("decision %+v expected %+v", d, c.Expected)
	}
	if c.Block != nil {
		if d.Action != NoMatch || d.Rule != nil || d.Block == nil || d.Block.Directory != c.Block.Directory {
			t.Fatalf("block got %+v want %+v", d, c.Block)
		}
		checkRecord(t, d.Block.Rule, c.Expected)
		return
	}
	want := NoMatch
	if c.Expected.Line > 0 {
		want = Exclude
		if !c.Expected.Ignored {
			want = Include
		}
	}
	if d.Block != nil || d.Action != want {
		t.Fatalf("decision got %+v want action %v without block", d, want)
	}
	checkRecord(t, d.Rule, c.Expected)
}

func requireCorpusFilesystem(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" && (strings.ContainsAny(path, "\t\n") || strings.HasSuffix(path, " ")) {
		t.Skip("Win32 cannot faithfully materialize this filename; in-memory case still runs")
	}
}

func TestCompatibilityCorpus(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range compatibilityFixtures(t) {
		if c.ID == "" || seen[c.ID] {
			t.Fatalf("missing or duplicate corpus identity %q", c.ID)
		}
		seen[c.ID] = true
		t.Run(c.ID, func(t *testing.T) {
			m, diags, err := Compile(fixtureSources(c), Options{})
			if err != nil || len(diags) != 0 {
				t.Fatalf("compile: %v diagnostics=%+v", err, diags)
			}
			for range 3 {
				d, err := m.Match(c.Query.Path, c.Query.IsDir)
				if err != nil {
					t.Fatal(err)
				}
				checkCorpusDecision(t, d, c)
			}
			t.Run("filesystem", func(t *testing.T) {
				requireCorpusFilesystem(t, c.Query.Path)
				root := t.TempDir()
				writeTree(t, root, c.Files)
				p := testPolicy(t, root, layeredSpecs())
				checkCorpusDecision(t, policyAction(t, p, c.Query.Path, c.Query.IsDir).Decision, c)
				d, err := p.MatchPath(filepath.Join(root, filepath.FromSlash(c.Query.Path)), c.Query.IsDir)
				if err != nil {
					t.Fatal(err)
				}
				checkCorpusDecision(t, d.Decision, c)
			})
		})
	}
}

func TestLiveGitCompatibilityCorpus(t *testing.T) {
	requireLiveGit(t)
	for _, c := range compatibilityFixtures(t) {
		for _, fold := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-fold-%v", c.ID, fold), func(t *testing.T) {
				requireCorpusFilesystem(t, c.Query.Path)
				o := newOracle(t, c, fold)
				query := c.Query.Path
				if c.Query.IsDir {
					query += "/"
				}
				observed := o.query(t, query)
				if !fold && observed != c.Expected {
					t.Fatalf("Git %+v fixture %+v", observed, c.Expected)
				}
				blocked := ""
				for at := range len(c.Query.Path) {
					if c.Query.Path[at] == '/' && o.query(t, c.Query.Path[:at]).Ignored {
						blocked = c.Query.Path[:at]
						break
					}
				}
				p, err := NewPolicy(PolicyOptions{Root: o.dir, Sources: layeredSpecs(), Options: Options{CaseFold: fold}})
				if err != nil {
					t.Fatal(err)
				}
				m, _, err := Compile(fixtureSources(c), Options{CaseFold: fold})
				if err != nil {
					t.Fatal(err)
				}
				d, err := m.Match(c.Query.Path, c.Query.IsDir)
				if err != nil {
					t.Fatal(err)
				}
				for _, decision := range []Decision{d, policyAction(t, p, c.Query.Path, c.Query.IsDir).Decision} {
					if (decision.Block == nil) != (blocked == "") || decision.Block != nil && decision.Block.Directory != blocked {
						t.Fatalf("library block %+v Git blocking ancestor %q", decision.Block, blocked)
					}
					if decision.Ignored() != observed.Ignored {
						t.Fatalf("library %+v Git %+v", decision, observed)
					}
					r := decision.Rule
					if decision.Block != nil {
						r = decision.Block.Rule
					}
					checkRecord(t, r, observed)
				}
			})
		}
	}
}

func TestLiveGitRepositoryWalk(t *testing.T) {
	requireLiveGit(t)
	trees := []struct {
		name  string
		paths []string
	}{
		{"go", []string{"bin/tool", "tools/bin/tool", "src/local.test/cache", "src/p/local.test", "src/p/generated.cgo2.go", "doc/articles/wiki/demo.bin", "doc/articles/wiki/sub/demo.bin"}},
		{"vscode", []string{"extensions/demo/out/main.js", "extensions/group/demo/out/main.js", "extensions/demo/dist/bundle.js", "extensions/demo/src/main.ts", "out-client/main.js", "src/out-client/main.js", "src/node_modules/pkg/index.js", "test/case.snap.actual", "test/case.snap"}},
		{"cargo", []string{"target/debug/cargo", "crates/demo/target/debug/cargo", "Cargo.lock", "crates/demo/Cargo.lock", "src/doc/book/index.html", "src/doc/chapter/book/index.html", "src/other/book/index.html", "src/etc/build.pyc", "src/etc/sub/build.pyc"}},
	}
	corpus := repositoryFixtures(t)
	for _, tree := range trees {
		for _, fold := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-fold-%v", tree.name, fold), func(t *testing.T) {
				files := map[string]string{"root.local": "", "root.secret": "", "case.LOCAL": "", "case.SECRET": ""}
				for _, c := range corpus {
					if strings.HasPrefix(c.ID, tree.name+"-") {
						for name, data := range c.Files {
							files[name] = data
						}
						break
					}
				}
				for _, path := range tree.paths {
					files[path] = ""
				}
				o := newLayeredOracle(t, files, "*.local\n", "*.secret\n", fold)
				checkLiveGitWalker(t, o)
			})
		}
	}
}
