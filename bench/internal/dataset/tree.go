// SPDX-License-Identifier: Apache-2.0

package dataset

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
)

// TreeSpec fixes one synthetic filesystem tree. Parameters are recorded in
// the manifest Notes so results can be tied back to exact inputs.
type TreeSpec struct {
	Name        string
	Depth       int  // main chain depth
	Branch      int  // directories per level (main chain plus branch-1 side dirs)
	RulesPerDir int  // rule lines per directory's ignore file
	FilesPerDir int  // regular files per directory
	BareOnly    bool // every directory carries the identical bare-rule block
	Flat        bool // rules live only in the root file
	Seed        uint64
}

// TreeSpecs lists the generated trees in manifest order.
func TreeSpecs() []TreeSpec {
	return []TreeSpec{
		{Name: "nested-d2", Depth: 2, Branch: 3, RulesPerDir: 8, FilesPerDir: 4, Seed: 201},
		{Name: "nested-d8", Depth: 8, Branch: 2, RulesPerDir: 8, FilesPerDir: 4, Seed: 202},
		{Name: "nested-d32", Depth: 32, Branch: 1, RulesPerDir: 6, FilesPerDir: 3, Seed: 203},
		{Name: "pair-nested", Depth: 8, Branch: 2, RulesPerDir: 6, FilesPerDir: 4, BareOnly: true, Seed: 204},
		{Name: "pair-single", Depth: 8, Branch: 2, RulesPerDir: 6, FilesPerDir: 4, BareOnly: true, Flat: true, Seed: 204},
	}
}

// sharedBlock is the identical bare-rule block placed in every directory of
// the bareOnly trees. Bare patterns apply at any depth below their file, so
// one copy at the root (pair-single) decides every query exactly like the
// per-directory copies (pair-nested): for any path the deepest applicable
// file carries the same block.
func sharedBlock(g *genState) []string {
	var lines []string
	for i := 0; i < 4; i++ {
		lines = append(lines, g.nextWord())
	}
	lines = append(lines, "*.gen", "!keep.gen", "cache/")
	return lines
}

// scopedBlock builds one directory's rules for the mixed trees and reports
// the anchored directory the block creates. Anchored patterns are scoped to
// their directory, exercising nested semantics.
func scopedBlock(g *genState, rules int) (lines []string, localDir string) {
	for i := 0; i < rules-5; i++ {
		lines = append(lines, g.nextWord())
	}
	localDir = "local-" + g.nextWord()
	lines = append(lines,
		"*.gen",
		"!keep-"+g.nextWord()+".gen",
		"cache/",
		"/"+localDir+"/",
	)
	return lines, localDir
}

// BuildTree materializes one synthetic tree under root and returns its
// statistics and candidate queries (labels come from the oracle later).
func BuildTree(root string, spec TreeSpec) (Tree, []Query, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Tree{}, nil, err
	}
	g := &genState{rng: rand.New(rand.NewPCG(spec.Seed, spec.Seed^0x5eed))}
	t := Tree{Name: spec.Name, Root: root, Notes: fmt.Sprintf(
		"depth=%d branch=%d rulesPerDir=%d filesPerDir=%d bareOnly=%v flat=%v seed=%d",
		spec.Depth, spec.Branch, spec.RulesPerDir, spec.FilesPerDir, spec.BareOnly, spec.Flat, spec.Seed)}

	// Directory layout: a main chain d0/.../d(depth-1), with branch-1 side
	// directories hanging off each chain prefix.
	chain := make([]string, spec.Depth)
	for d := 0; d < spec.Depth; d++ {
		chain[d] = fmt.Sprintf("d%d-%s", d, words[(int(spec.Seed)+d*7)%len(words)])
	}
	dirs := []string{""}
	for d := 0; d < spec.Depth; d++ {
		dirs = append(dirs, strings.Join(chain[:d+1], "/"))
		prefix := strings.Join(chain[:d], "/")
		for b := 1; b < spec.Branch; b++ {
			side := fmt.Sprintf("side%d-%s", b, words[(int(spec.Seed)+d*5+b*11)%len(words)])
			if prefix == "" {
				dirs = append(dirs, side)
			} else {
				dirs = append(dirs, prefix+"/"+side)
			}
		}
	}
	// One rule block per directory, generated in directory order. bareOnly
	// trees share a single block everywhere so pair-nested and pair-single
	// decide identically on every query; mixed trees scope unique blocks.
	var shared []string
	if spec.BareOnly {
		shared = sharedBlock(g)
	}
	blocks := make([][]string, len(dirs))
	locals := make([]string, len(dirs))
	for i := range dirs {
		if spec.BareOnly {
			blocks[i] = shared
		} else {
			blocks[i], locals[i] = blockFor(g, spec)
		}
	}
	// Materialize directories, ignore files and content.
	var queries []Query
	for i, rel := range dirs {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return Tree{}, nil, err
		}
		t.Dirs++
		if !spec.Flat {
			if err := os.WriteFile(filepath.Join(abs, ".gitignore"), []byte(strings.Join(blocks[i], "\n")+"\n"), 0o644); err != nil {
				return Tree{}, nil, err
			}
			t.IgnoreFiles++
			t.Rules += len(blocks[i])
		}
		// The anchored local directory gives scoped rules real content.
		if locals[i] != "" {
			localRel := join(rel, locals[i])
			if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(localRel)), 0o755); err != nil {
				return Tree{}, nil, err
			}
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(join(localRel, "y.bin"))), []byte("x"), 0o644); err != nil {
				return Tree{}, nil, err
			}
			t.Dirs++
			t.Files++
			queries = append(queries, Query{Path: join(localRel, "y.bin"), IsDir: false})
		}
		// Cache subdirectory gives the dir-only rule real content.
		cacheRel := join(rel, "cache")
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(cacheRel)), 0o755); err != nil {
			return Tree{}, nil, err
		}
		t.Dirs++
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(join(cacheRel, "x.bin"))), []byte("x"), 0o644); err != nil {
			return Tree{}, nil, err
		}
		t.Files++
		queries = append(queries, Query{Path: join(cacheRel, "x.bin"), IsDir: false})
		// Regular files per directory.
		for f := 0; f < spec.FilesPerDir; f++ {
			name := fmt.Sprintf("%s%d.go", words[(int(spec.Seed)+i*3+f)%len(words)], f)
			p := join(rel, name)
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p)), []byte("package p\n"), 0o644); err != nil {
				return Tree{}, nil, err
			}
			t.Files++
			queries = append(queries,
				Query{Path: p, IsDir: false},
				Query{Path: join(rel, fmt.Sprintf("gen%d.gen", f)), IsDir: false},
				Query{Path: join(rel, "keep.gen"), IsDir: false},
			)
		}
		if i%3 == 0 && rel != "" {
			queries = append(queries, Query{Path: rel, IsDir: true})
		}
		queries = append(queries, Query{Path: join(rel, fmt.Sprintf("clean%d.md", i)), IsDir: false})
	}
	if spec.Flat {
		if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(strings.Join(blocks[0], "\n")+"\n"), 0o644); err != nil {
			return Tree{}, nil, err
		}
		t.IgnoreFiles = 1
		t.Rules = len(blocks[0])
	}
	return t, queries, nil
}

// blockFor returns one directory's rule lines (and anchored directory when
// present), honouring the tree's bareOnly flag.
func blockFor(g *genState, spec TreeSpec) ([]string, string) {
	if spec.BareOnly {
		return sharedBlock(g), ""
	}
	return scopedBlock(g, spec.RulesPerDir)
}
