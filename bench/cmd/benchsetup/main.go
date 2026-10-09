// SPDX-License-Identifier: Apache-2.0

// Command benchsetup materializes every benchmark dataset into the cache
// directory: synthetic rule sets, synthetic trees and the pinned moby/moby
// checkout, each oracle-labelled with Git's own ignore verdicts. Output is
// deterministic for a given commit of this module; the moby tree is pinned
// by commit and cached across runs.
package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.lostcrafters.com/ignore/bench/internal/dataset"
	"go.lostcrafters.com/ignore/bench/internal/oracle"
)

// ruleSetNames lists the generated single-source sets in manifest order.
var ruleSetNames = []string{
	"mixed-10", "mixed-100", "mixed-1000", "mixed-10000",
	"lit-100", "wild-100", "anchored-100", "dir-100", "wild-complex",
}

func main() {
	cache, err := dataset.CacheDir()
	must(err)
	must(os.MkdirAll(cache, 0o755))
	gitVersion, err := oracle.Version()
	must(err)
	m := dataset.Environment(gitVersion)
	m.CacheDir = cache

	fmt.Printf("cache: %s\n", cache)
	fmt.Printf("%s / %s\n", gitVersion, m.GoVersion)

	// --- Synthetic rule sets -------------------------------------------
	must(os.RemoveAll(filepath.Join(cache, "rulesets")))
	var ruleSets []dataset.RuleSet
	for _, name := range ruleSetNames {
		rs, err := buildRuleSet(cache, name)
		must(err)
		ruleSets = append(ruleSets, rs)
		fmt.Printf("ruleset %-14s rules=%-6d queries=%-4d ignored=%d\n",
			rs.Name, rs.RuleCount, len(rs.Queries), countIgnored(rs.Queries))
	}

	// --- Synthetic trees -------------------------------------------------
	must(os.RemoveAll(filepath.Join(cache, "trees")))
	var trees []dataset.Tree
	for _, spec := range dataset.TreeSpecs() {
		t, err := buildTree(cache, spec)
		must(err)
		trees = append(trees, t)
		fmt.Printf("tree    %-12s dirs=%-5d files=%-6d ignoreFiles=%-3d rules=%-4d queries=%-4d ignored=%d\n",
			t.Name, t.Dirs, t.Files, t.IgnoreFiles, t.Rules, len(t.Queries), countIgnored(t.Queries))
	}

	// --- Real-world tree --------------------------------------------------
	mobyRoot := filepath.Join(cache, "repos", "moby")
	skipped, err := dataset.PrepareMoby(mobyRoot)
	must(err)
	if skipped > 0 {
		fmt.Printf("moby: skipped %d files unsupported on this platform\n", skipped)
	}
	mobyTree, err := buildMobyTree(cache, mobyRoot)
	must(err)
	trees = append(trees, mobyTree)
	fmt.Printf("tree    %-12s dirs=%-5d files=%-6d ignoreFiles=%-3d rules=%-4d queries=%-4d ignored=%d [%s@%s]\n",
		mobyTree.Name, mobyTree.Dirs, mobyTree.Files, mobyTree.IgnoreFiles, mobyTree.Rules,
		len(mobyTree.Queries), countIgnored(mobyTree.Queries), dataset.MobyRepo, dataset.MobyCommit[:12])

	// --- Realistic rule text from the moby root file ----------------------
	rs, err := buildMobyRootRuleSet(cache, mobyRoot, mobyTree.Queries)
	must(err)
	ruleSets = append(ruleSets, rs)
	fmt.Printf("ruleset %-14s rules=%-6d queries=%-4d ignored=%d [moby root .gitignore]\n",
		rs.Name, rs.RuleCount, len(rs.Queries), countIgnored(rs.Queries))

	m.RuleSets = ruleSets
	m.Trees = trees
	must(m.Save())
	fmt.Printf("manifest written: %s\n", filepath.Join(cache, "manifest.json"))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "benchsetup:", err)
		os.Exit(1)
	}
}

func countIgnored(qs []dataset.Query) int {
	n := 0
	for _, q := range qs {
		if q.Ignored {
			n++
		}
	}
	return n
}

// dedupe keeps the first query per (path, isDir), preserving order.
func dedupe(qs []dataset.Query) []dataset.Query {
	seen := make(map[string]bool, len(qs))
	out := qs[:0]
	for _, q := range qs {
		key := fmt.Sprintf("%s|%v", q.Path, q.IsDir)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, q)
	}
	return out
}

func buildRuleSet(cache, name string) (dataset.RuleSet, error) {
	text, count, queries := dataset.GenerateRuleSet(name)
	dir := filepath.Join(cache, "rulesets", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dataset.RuleSet{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(text), 0o644); err != nil {
		return dataset.RuleSet{}, err
	}
	if err := oracle.Init(dir, cache); err != nil {
		return dataset.RuleSet{}, err
	}
	queries = dedupe(queries)
	if err := oracle.Label(dir, cache, queries); err != nil {
		return dataset.RuleSet{}, err
	}
	return dataset.RuleSet{
		Name:      name,
		Rules:     text,
		RuleCount: count,
		Queries:   queries,
		RepoDir:   dir,
		Notes:     "seeded generator, category profile " + name,
	}, nil
}

func buildTree(cache string, spec dataset.TreeSpec) (dataset.Tree, error) {
	root := filepath.Join(cache, "trees", spec.Name)
	t, queries, err := dataset.BuildTree(root, spec)
	if err != nil {
		return dataset.Tree{}, err
	}
	if err := oracle.Init(root, cache); err != nil {
		return dataset.Tree{}, err
	}
	queries = dedupe(queries)
	if err := oracle.Label(root, cache, queries); err != nil {
		return dataset.Tree{}, err
	}
	// Recount after oracle materialization so file/dir totals match the
	// tree the benchmarks traverse.
	st, err := dataset.ScanTree(root)
	if err != nil {
		return dataset.Tree{}, err
	}
	t.Files = st.Files
	t.Dirs = st.Dirs
	t.IgnoreFiles = st.IgnoreFiles
	t.Rules = st.Rules
	t.Queries = queries
	return t, nil
}

// buildMobyTree samples tracked paths, synthesizes untracked candidates,
// labels everything with the oracle and composes the final query mix.
func buildMobyTree(cache, root string) (dataset.Tree, error) {
	if err := oracle.Init(root, cache); err != nil {
		return dataset.Tree{}, err
	}
	tracked, err := dataset.TrackedPaths(root)
	if err != nil {
		return dataset.Tree{}, err
	}
	sort.Strings(tracked)
	rng := rand.New(rand.NewPCG(900, 901))
	// Deterministic tracked sample (every ~30th path, offset by seed draw).
	offset := rng.IntN(30)
	var queries []dataset.Query
	for i := offset; i < len(tracked); i += 30 {
		queries = append(queries, dataset.Query{Path: tracked[i], IsDir: false})
		if len(queries) >= 500 {
			break
		}
	}
	// Untracked candidates derived from the first 350 sampled paths.
	var candidates []dataset.Query
	for _, q := range queries[:min(350, len(queries))] {
		base := q.Path
		for _, suf := range []string{".orig", ".rej", ".exe", ".log", ".tmp", ".swp", ".bak"} {
			candidates = append(candidates, dataset.Query{Path: base + suf, IsDir: false})
		}
		for _, d := range []string{"bin", "dist", "build"} {
			candidates = append(candidates, dataset.Query{Path: childOf(parentDir(base), d+"/tool"), IsDir: false})
		}
		candidates = append(candidates, dataset.Query{Path: childOf(parentDir(base), "untracked-artifact.md"), IsDir: false})
	}
	all := append(append([]dataset.Query{}, queries...), candidates...)
	all = dedupe(all)
	if err := oracle.Label(root, cache, all); err != nil {
		return dataset.Tree{}, err
	}
	// Compose the final mix: keep every ignored candidate plus the tracked
	// sample, then top up with clean candidates to 1600 queries.
	var ignored, clean []dataset.Query
	for _, q := range all {
		if q.Ignored {
			ignored = append(ignored, q)
		} else {
			clean = append(clean, q)
		}
	}
	if len(ignored) > 700 {
		rng.Shuffle(len(ignored), func(a, b int) { ignored[a], ignored[b] = ignored[b], ignored[a] })
		ignored = ignored[:700]
	}
	if len(clean) > 1600-len(ignored) {
		rng.Shuffle(len(clean), func(a, b int) { clean[a], clean[b] = clean[b], clean[a] })
		clean = clean[:1600-len(ignored)]
	}
	final := append(ignored, clean...)
	sort.Slice(final, func(a, b int) bool { return final[a].Path < final[b].Path })
	st, err := dataset.ScanTree(root)
	if err != nil {
		return dataset.Tree{}, err
	}
	hist := make([]string, 0, len(st.DepthHist))
	for d := 0; d <= st.MaxPathDepth; d++ {
		if n, ok := st.DepthHist[d]; ok {
			hist = append(hist, fmt.Sprintf("depth %d: %d", d, n))
		}
	}
	return dataset.Tree{
		Name:        "moby",
		Root:        root,
		Queries:     final,
		Files:       st.Files,
		Dirs:        st.Dirs,
		IgnoreFiles: st.IgnoreFiles,
		Rules:       st.Rules,
		Real:        true,
		Notes:       fmt.Sprintf("%s@%s; ignore files by depth: %s", dataset.MobyRepo, dataset.MobyCommit, strings.Join(hist, ", ")),
	}, nil
}

func parentDir(path string) string {
	if at := strings.LastIndexByte(path, '/'); at >= 0 {
		return path[:at]
	}
	return ""
}

func childOf(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// buildMobyRootRuleSet copies the moby root .gitignore into an isolated
// repo and labels the moby tree's queries against it, giving Tier 1 a
// realistic rule text over realistic path names.
func buildMobyRootRuleSet(cache, mobyRoot string, queries []dataset.Query) (dataset.RuleSet, error) {
	raw, err := os.ReadFile(filepath.Join(mobyRoot, ".gitignore"))
	if err != nil {
		return dataset.RuleSet{}, err
	}
	dir := filepath.Join(cache, "rulesets", "moby-root")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dataset.RuleSet{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), raw, 0o644); err != nil {
		return dataset.RuleSet{}, err
	}
	if err := oracle.Init(dir, cache); err != nil {
		return dataset.RuleSet{}, err
	}
	// Deterministic sample of the tree queries (every second one, capped).
	sampled := make([]dataset.Query, 0, 512)
	for i := 0; i < len(queries) && len(sampled) < 512; i += 2 {
		sampled = append(sampled, dataset.Query{Path: queries[i].Path, IsDir: queries[i].IsDir})
	}
	if err := oracle.Label(dir, cache, sampled); err != nil {
		return dataset.RuleSet{}, err
	}
	text := string(raw)
	return dataset.RuleSet{
		Name:      "moby-root",
		Rules:     text,
		RuleCount: strings.Count(strings.TrimSpace(text), "\n") + 1,
		Queries:   sampled,
		RepoDir:   dir,
		Notes:     "moby root .gitignore over sampled moby paths",
	}, nil
}
