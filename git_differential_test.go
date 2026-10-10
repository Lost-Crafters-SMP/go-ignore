// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

// TestLiveGitWalkerPruning checks the walker end to end against real Git
// filesystem traversal: the files the walker discovers equal the files
// `git status --porcelain --ignored=matching -uall` lists, directories are
// pruned exactly where Git collapses them to a `!! dir/` entry, every
// reported decision agrees with Git's check-ignore verdict for that path,
// and a matcher precompiled from the same sources returns matching
// Descend verdicts for every directory.
func TestLiveGitWalkerPruning(t *testing.T) {
	requireLiveGit(t)
	files := map[string]string{
		".gitignore":     "*.log\n!keep.log\ntmp/\nvendor/\n/wild/*\n!/wild/keep.txt\n!deny\ndeny/*\n!deny/keep.txt\n/locked/\n!/locked/keep.txt\n/span/**\n!/span/keep\nout/\n",
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
		// wild/ and deny/ are excluded only in canonical slash form
		// (their contents, not themselves): Git traverses them and their
		// re-included keep.txt files must be discovered. Nested sources
		// beneath wild/ stay effective: !special outranks the root glob
		// by depth and shrine/ prunes one level deeper.
		"wild/keep.txt":   "",
		"wild/drop.txt":   "",
		"wild/.gitignore": "!special\nshrine/\n",
		"wild/special":    "",
		"wild/shrine/x":   "",
		"deny/keep.txt":   "",
		"deny/drop.txt":   "",
		"locked/keep.txt": "",
		"locked/drop.txt": "",
		"span/keep":       "",
		"span/sub/g":      "",
		"a/.gitignore":    "!out/\n",
		"a/b/.gitignore":  "out/\n",
		"a/top":           "",
		"a/out/f":         "",
		"a/b/out/f":       "",
	}
	o := newLayeredOracle(t, files, "*.bak\n", "secret-*\n", false)
	checkLiveGitWalker(t, o)
}

// statusTraversal runs a real `git status --porcelain --ignored=matching
// -uall -z` traversal and splits its output into the files Git discovered
// and the directories Git refused to descend into (Git collapses those to a
// single trailing-slash entry). This is the traversal oracle for the walker;
// check-ignore remains the oracle for per-path decisions.
func (o *oracle) statusTraversal(t *testing.T) (files, prunedDirs map[string]bool) {
	t.Helper()
	data, _ := o.command(t, "", false, "status", "--porcelain", "--ignored=matching", "-uall", "-z")
	files = make(map[string]bool)
	prunedDirs = make(map[string]bool)
	for _, rec := range strings.Split(strings.TrimRight(string(data), "\x00"), "\x00") {
		if rec == "" {
			continue
		}
		if len(rec) < 4 || rec[2] != ' ' || rec[0] == '?' && rec[1] != '?' || rec[0] == '!' && rec[1] != '!' {
			t.Fatalf("unexpected status record %q", rec)
		}
		path := rec[3:]
		if strings.HasSuffix(path, "/") {
			prunedDirs[strings.TrimSuffix(path, "/")] = true
		} else {
			files[path] = true
		}
	}
	if len(files) == 0 {
		t.Fatal("empty status traversal")
	}
	return files, prunedDirs
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

	// The traversal oracle: which files Git really discovers, and which
	// directories it refuses to descend into.
	gitFiles, gitPruned := o.statusTraversal(t)

	visitedFiles := map[string]bool{}
	visitedDirs := map[string]bool{}
	decisions := map[string]PolicyDecision{}
	err = p.Walk("", func(rel string, isDir bool, d PolicyDecision) error {
		if isDir {
			visitedDirs[rel] = true
		} else {
			visitedFiles[rel] = true
		}
		decisions[rel] = d
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The walker discovers exactly the files Git's traversal discovers:
	// nothing less (re-includable children must not be missed) and nothing
	// more (pruned subtrees must not be entered).
	if len(visitedFiles) != len(gitFiles) {
		t.Fatalf("walker visited %d files, git status discovered %d:\nwalker=%v\ngit=%v",
			len(visitedFiles), len(gitFiles), sortedKeys(visitedFiles), sortedKeys(gitFiles))
	}
	for path := range gitFiles {
		if !visitedFiles[path] {
			t.Fatalf("git status discovered %q but the walker did not", path)
		}
	}

	// Directory pruning matches Git's descent decisions exactly: every
	// pruned directory is reported and then pruned, and no directory below
	// a pruned one is entered. Remaining directories are structural and
	// must all be traversable. wantAll additionally keeps the visited
	// files, for the per-entry decision comparisons below.
	wantDirs := map[string]bool{".git": true}
	wantAll := map[string]bool{".git": true}
	for _, e := range all {
		blocked := false
		rest := e.rel
		for {
			at := strings.LastIndexByte(rest, '/')
			if at < 0 {
				break
			}
			parent := rest[:at]
			if parent == ".git" || gitPruned[parent] {
				blocked = true
				break
			}
			rest = parent
		}
		if blocked {
			continue
		}
		wantAll[e.rel] = true
		if e.isDir {
			wantDirs[e.rel] = true
		}
	}
	if len(visitedDirs) != len(wantDirs) {
		t.Fatalf("walker visited %d directories want %d:\nwalker=%v\nwant=%v\ngit-pruned=%v",
			len(visitedDirs), len(wantDirs), sortedKeys(visitedDirs), sortedKeys(wantDirs), sortedKeys(gitPruned))
	}
	for rel := range wantDirs {
		if !visitedDirs[rel] {
			t.Fatalf("expected directory visit %q", rel)
		}
	}
	for rel := range gitPruned {
		if rel == ".git" {
			continue
		}
		if !visitedDirs[rel] {
			t.Fatalf("git-pruned directory %q not reported", rel)
		}
	}
	if !decisions[".git"].Reserved {
		t.Fatalf("reserved .git decision %+v", decisions[".git"])
	}

	// Every reported decision agrees with Git's check-ignore verdict for
	// that path (canonical slash-form queries for directories).
	for _, e := range all {
		if !wantAll[e.rel] {
			continue
		}
		d := decisions[e.rel]
		query := e.rel
		if e.isDir {
			query += "/"
		}
		if d.Ignored() != o.query(t, query).Ignored {
			t.Fatalf("decision %q library=%+v git-mismatch", e.rel, d)
		}
		if e.rel == ".git" {
			continue
		}
		checkPolicyRecord(t, o, d, o.query(t, query))
	}
	// The traversal root itself is never reported.
	for rel := range visitedFiles {
		if rel == "" {
			t.Fatal("traversal root reported")
		}
	}
	for rel := range visitedDirs {
		if rel == "" {
			t.Fatal("traversal root reported")
		}
	}

	// Matcher-level descent verdicts agree with Git's traversal for
	// consumers that precompile every source and own their walking: a
	// directory Git collapsed to `!! dir/` is pruned by its own rules
	// (bare self-exclusion: Exclude with a rule and no block), while a
	// directory below a collapsed one is unreachable only because an
	// ancestor is excluded (Block names a Git-collapsed ancestor).
	m := cumulativeLayeredMatcher(t, o)
	for _, e := range all {
		if !e.isDir || e.rel == ".git" || strings.HasPrefix(e.rel, ".git/") {
			continue // reserved namespace, outside the matcher's rules
		}
		d, descend, err := m.Descend(e.rel)
		if err != nil {
			t.Fatal(err)
		}
		blockedAncestor := false
		rest := e.rel
		for {
			at := strings.LastIndexByte(rest, '/')
			if at < 0 {
				break
			}
			parent := rest[:at]
			if parent == ".git" || gitPruned[parent] {
				blockedAncestor = true
				break
			}
			rest = parent
		}
		switch {
		case blockedAncestor:
			if descend || d.Block == nil || !gitPruned[d.Block.Directory] {
				t.Fatalf("%q lies under a git-pruned ancestor: descend=%v decision=%+v block=%v",
					e.rel, descend, d, d.Block)
			}
		case gitPruned[e.rel]:
			if descend || d.Block != nil || d.Action != Exclude || d.Rule == nil {
				t.Fatalf("%q is git-pruned by its own rules: descend=%v decision=%+v",
					e.rel, descend, d)
			}
		default:
			if !descend {
				t.Fatalf("git traverses %q but descend=false: %+v", e.rel, d)
			}
		}
	}
}

// cumulativeLayeredMatcher precompiles every layered source of the oracle
// into one matcher: global excludes and info/exclude at their fixed root
// ranks, plus every nested .gitignore scoped to its directory. This is the
// precompiled-rules shape of the walker's predicate; same-rank ties across
// scopes are decided by scope depth, so nested order cannot change results
// and lexicographic order keeps compilation deterministic.
func cumulativeLayeredMatcher(t *testing.T, o *layeredOracle) *Matcher {
	t.Helper()
	read := func(full string) []byte {
		data, err := os.ReadFile(full)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	sources := []Source{
		{Label: "global-excludes", Rank: RankGlobal, Data: read(o.globalPath)},
		{Label: ".git/info/exclude", Rank: RankInfoExclude, Data: read(filepath.Join(o.dir, ".git", "info", "exclude"))},
	}
	var nested []string
	err := filepath.WalkDir(o.dir, func(full string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(o.dir, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			return filepath.SkipDir
		}
		if !e.IsDir() && e.Name() == ".gitignore" {
			nested = append(nested, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(nested)
	for _, rel := range nested {
		scope := ""
		if at := strings.LastIndexByte(rel, '/'); at >= 0 {
			scope = rel[:at]
		}
		sources = append(sources, Source{
			Label: rel,
			Scope: scope,
			Rank:  RankPerDirectory,
			Data:  read(filepath.Join(o.dir, filepath.FromSlash(rel))),
		})
	}
	m, _, err := Compile(sources, Options{CaseFold: o.fold})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
