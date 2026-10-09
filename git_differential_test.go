// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Git-backed compatibility tests for the policy layer. The oracle
// is the external Git binary only, driven as a black box; library behaviour is
// compared for verdicts and provenance. Library-specific extensions (custom
// ranks beyond Git's three layers, reserved paths, subtree traversal) are
// deliberately kept out of these comparisons and covered by unit tests.

func requireLiveGit(t *testing.T) {
	t.Helper()
	if os.Getenv("IGNORE_LIVE_GIT") != "1" {
		t.Skip("set IGNORE_LIVE_GIT=1")
	}
	requireGit(t)
}

// layeredSpecs models Git's standard exclude stack: the global excludes file
// (lowest), .git/info/exclude, and nested .gitignore files (depth breaks ties
// within the per-directory rank only).
func layeredSpecs() []SourceSpec {
	return []SourceSpec{
		{Filename: "global-excludes", Rank: RankGlobal, Optional: true},
		{Filename: ".git/info/exclude", Rank: RankInfoExclude, Optional: true},
		{Filename: ".gitignore", Rank: RankPerDirectory, Optional: true, Nested: true},
	}
}

// layeredOracle is a scratch Git repository with a populated global excludes
// file. It shares the fixture oracle's isolated environment.
type layeredOracle struct {
	*oracle
	globalPath string
}

func newLayeredOracle(t *testing.T, files map[string]string, global, info string, fold bool) *layeredOracle {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(dir, "global-excludes")
	env := []string{}
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "GIT_") || upper == "HOME" || upper == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(root, "empty"), "HOME="+home, "XDG_CONFIG_HOME="+home, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	if err := os.WriteFile(filepath.Join(root, "empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalPath, []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(info), 0o600); err != nil {
		t.Fatal(err)
	}
	o := &oracle{dir: dir, env: env, fold: fold}
	o.command(t, "", false, "init", "--quiet", "--template=")
	o.command(t, "", false, "config", "core.ignoreCase", strconv.FormatBool(fold))
	o.command(t, "", false, "config", "core.excludesFile", globalPath)
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(p, 0o700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &layeredOracle{oracle: o, globalPath: globalPath}
}

// label maps an oracle-reported source path onto the policy source label.
func (l *layeredOracle) label(source string) string {
	if source == l.globalPath {
		return "global-excludes"
	}
	return source
}

func checkPolicyRecord(t *testing.T, l *layeredOracle, d PolicyDecision, observed oracleRecord) {
	t.Helper()
	if d.Reserved {
		t.Fatalf("reserved decision in Git-only comparison: %+v", d)
	}
	r := d.Rule
	if d.Block != nil {
		r = d.Block.Rule
	}
	if r == nil {
		if observed.Source != "" || observed.Line != 0 || observed.Pattern != "" {
			t.Fatalf("missing rule: %+v", observed)
		}
		return
	}
	if r.Source() != l.label(observed.Source) || r.Line() != observed.Line || verboseRule(r) != observed.Pattern {
		t.Fatalf("provenance got %s:%d:%q want source=%q line=%d pattern=%q", r.Source(), r.Line(), verboseRule(r), l.label(observed.Source), observed.Line, observed.Pattern)
	}
}

type layeredCase struct {
	id      string
	global  string
	info    string
	files   map[string]string
	queries []struct {
		path  string
		isDir bool
	}
}

func TestLiveGitLayeredPolicy(t *testing.T) {
	requireLiveGit(t)
	cases := []layeredCase{
		{
			id:     "rank-vs-depth",
			global: "!keep.log\nplain\n!special\n",
			info:   "*.log\nspecial\nnotes\n",
			files: map[string]string{
				".gitignore":     "!keep.log\n*.tmp\n",
				"sub/.gitignore": "!keep.log\n!important.tmp\n",
				"sub/":           "",
			},
			queries: []struct {
				path  string
				isDir bool
			}{
				// Rank 200 info/exclude beats the rank 100 global negation
				// ("special") and the rank 100 global rule still applies at
				// depth ("plain").
				{"special", false},
				{"sub/plain", false},
				{"notes", false},
				// Rank 300 .gitignore negations beat both lower layers; the
				// deeper file wins at equal rank.
				{"keep.log", false},
				{"sub/keep.log", false},
				{"sub/x.log", false},
				{"sub/important.tmp", false},
				{"sub/y.tmp", false},
			},
		},
		{
			id:     "blocking-and-negation",
			global: "*.bak\n",
			info:   "secret-*\n",
			files: map[string]string{
				".gitignore":       "build/\n*.o\n!lib.so\n",
				"build/.gitignore": "!reopen\n",
				"vendor/":          "",
				"build/":           "",
				"gen/":             "",
			},
			queries: []struct {
				path  string
				isDir bool
			}{
				{"build", true},
				{"build/reopen", false},
				{"build/out/f", false},
				{"a.o", false},
				{"lib.so", false},
				{"old.bak", false},
				{"secret-key", false},
				{"vendor", true},
			},
		},
		{
			id:     "escape-and-classes",
			global: "",
			info:   "",
			files: map[string]string{
				"sub/.gitignore": "[[:digit:]]*.log\n\\#hash\na b\\ c\n[!x]z\n",
				"sub/":           "",
			},
			queries: []struct {
				path  string
				isDir bool
			}{
				{"sub/7a.log", false},
				{"sub/7.log", false},
				{"sub/#hash", false},
				{"sub/a b c", false},
				{"sub/yz", false},
				{"sub/xz", false},
			},
		},
		{
			id:     "directory-endpoints",
			global: "",
			info:   "",
			files: map[string]string{
				".gitignore":     "gen/\nsub/gen/\n!sub/gen/\n",
				"sub/.gitignore": "",
				"gen/":           "",
				"sub/gen/":       "",
			},
			queries: []struct {
				path  string
				isDir bool
			}{
				{"gen", true},
				{"gen/inner", false},
				{"sub/gen", true},
			},
		},
		{
			id:     "fold-layered",
			global: "Readme\n",
			info:   "*.LOG\n",
			files: map[string]string{
				".gitignore": "Temp/\n",
				"sub/":       "",
			},
			queries: []struct {
				path  string
				isDir bool
			}{
				{"readme", false},
				{"sub/x.log", false},
				{"temp", true},
			},
		},
		{
			id:     "lower-layer-directory-reopened",
			global: "cache/\n*.log\n",
			info:   "!cache/\ncache/*\n!cache/keep/\n",
			files: map[string]string{
				".gitignore":            "!cache/keep/*.log\n",
				"cache/keep/.gitignore": "*.tmp\n!important.tmp\n",
				"cache/drop/.gitignore": "!rescue\n",
			},
			queries: []struct {
				path  string
				isDir bool
			}{
				{"cache", true}, {"cache/keep", true},
				{"cache/keep/output.log", false}, {"cache/keep/output.tmp", false},
				{"cache/keep/important.tmp", false}, {"cache/drop/rescue", false},
			},
		},
		{
			id:     "three-directory-scopes",
			global: "*.dat\n",
			info:   "!keep.dat\n",
			files: map[string]string{
				".gitignore":       "*.dat\n",
				"a/.gitignore":     "!keep.dat\n",
				"a/b/.gitignore":   "/keep.dat\n!/local.dat\n",
				"other/.gitignore": "!*.dat\n",
			},
			queries: []struct {
				path  string
				isDir bool
			}{
				{"keep.dat", false}, {"a/keep.dat", false}, {"a/b/keep.dat", false},
				{"a/b/deep/keep.dat", false}, {"a/b/local.dat", false},
				{"a/b/deep/local.dat", false}, {"other/keep.dat", false},
			},
		},
	}
	for _, fold := range []bool{false, true} {
		for _, c := range cases {
			t.Run(fmt.Sprintf("%s-fold-%v", c.id, fold), func(t *testing.T) {
				materialized := make(map[string]string, len(c.files)+len(c.queries)+1)
				for k, v := range c.files {
					materialized[k] = v
				}
				for _, q := range c.queries {
					key := q.path
					if q.isDir {
						key += "/"
					}
					if _, ok := materialized[key]; !ok {
						materialized[key] = ""
					}
				}
				o := newLayeredOracle(t, materialized, c.global, c.info, fold)
				p, err := NewPolicy(PolicyOptions{
					Root:    o.dir,
					Sources: layeredSpecs(),
					Options: Options{CaseFold: fold},
				})
				if err != nil {
					t.Fatal(err)
				}
				for _, q := range c.queries {
					query := q.path
					if q.isDir {
						query += "/"
					}
					observed := o.query(t, query)
					d, err := p.Match(q.path, q.isDir)
					if err != nil {
						t.Fatalf("match %q: %v", q.path, err)
					}
					if d.Ignored() != observed.Ignored {
						t.Fatalf("verdict %q fold=%v library=%+v oracle=%+v", q.path, fold, d, observed)
					}
					checkPolicyRecord(t, o, d, observed)
				}
			})
		}
	}
}

// TestLiveGitWalkerPruning checks the walker end to end: the visited set
// equals the full enumeration minus descendants of Git-ignored directories,
// and every reported decision agrees with Git for that path.
func TestLiveGitWalkerPruning(t *testing.T) {
	requireLiveGit(t)
	files := map[string]string{
		".gitignore":     "*.log\n!keep.log\ntmp/\nvendor/\n",
		"src/.gitignore": "!local.log\n",
		"docs/guide.md":  "",
		"src/main.go":    "",
		"src/local.log":  "",
		"src/a.log":      "",
		"src/keep.log":   "",
		"src/tmp/x":      "",
		"vendor/lib.a":   "",
		"vendor/sub/y":   "",
		"secret-keys":    "",
		"plain.bak":      "",
		"top.log":        "",
		"deep/a/b/c.log": "",
	}
	o := newLayeredOracle(t, files, "*.bak\n", "secret-*\n", false)
	checkLiveGitWalker(t, o)
}

func checkLiveGitWalker(t *testing.T, o *layeredOracle) {
	t.Helper()
	p, err := NewPolicy(PolicyOptions{
		Root:     o.dir,
		Sources:  layeredSpecs(),
		Reserved: []string{".git"},
		Options:  Options{CaseFold: o.fold},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Enumerate every entry without pruning; ask Git about each one.
	type entry struct {
		rel   string
		isDir bool
	}
	var all []entry
	root := o.dir
	err = filepath.WalkDir(root, func(full string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if full == root {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			// Git's own namespace: the policy reserves it, so the walker
			// reports and prunes exactly this entry.
			return filepath.SkipDir
		}
		all = append(all, entry{rel, e.IsDir()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("empty enumeration")
	}

	ignored := make(map[string]bool, len(all))
	for _, e := range all {
		query := e.rel
		if e.isDir {
			query += "/"
		}
		ignored[e.rel] = o.query(t, query).Ignored
	}
	want := map[string]bool{".git": true}
	for _, e := range all {
		visited := true
		rest := e.rel
		for {
			at := strings.LastIndexByte(rest, '/')
			if at < 0 {
				break
			}
			parent := rest[:at]
			if parent == ".git" || ignored[parent] {
				visited = false
				break
			}
			rest = parent
		}
		if visited {
			want[e.rel] = true
		}
	}

	var visited []string
	decisions := map[string]PolicyDecision{}
	err = p.Walk("", func(rel string, _ bool, d PolicyDecision) error {
		visited = append(visited, rel)
		decisions[rel] = d
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(visited) != len(want) {
		t.Fatalf("walker visited %d want %d: visited=%v want=%v", len(visited), len(want), visited, boolKeys(want))
	}
	for _, rel := range visited {
		if !want[rel] {
			t.Fatalf("unexpected visit %q", rel)
		}
	}
	if !decisions[".git"].Reserved {
		t.Fatalf("reserved .git decision %+v", decisions[".git"])
	}
	for _, e := range all {
		if !want[e.rel] {
			continue
		}
		d := decisions[e.rel]
		if d.Ignored() != ignored[e.rel] {
			t.Fatalf("decision %q library=%+v git-ignored=%v", e.rel, d, ignored[e.rel])
		}
		if e.rel == ".git" {
			continue
		}
		query := e.rel
		if e.isDir {
			query += "/"
		}
		checkPolicyRecord(t, o, d, o.query(t, query))
	}
	// The traversal root itself is never reported.
	for _, rel := range visited {
		if rel == "" {
			t.Fatal("traversal root reported")
		}
	}
}

func boolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
