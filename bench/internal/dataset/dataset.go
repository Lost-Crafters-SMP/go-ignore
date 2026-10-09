// SPDX-License-Identifier: Apache-2.0

// Package dataset defines the benchmark inputs: labelled query sets, rule
// sets and filesystem trees, all generated deterministically by
// cmd/benchsetup and cached on disk.
package dataset

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Query is one benchmark probe. Ignored is the Git oracle's verdict for the
// materialized path, not the generator's intent.
type Query struct {
	Path    string
	IsDir   bool
	Ignored bool
}

// RuleSet is an in-memory, single-source benchmark input: the text of one
// ignore file plus a labelled query set. The oracle workspace containing
// .gitignore and the materialized query paths is kept for re-verification.
type RuleSet struct {
	Name      string  `json:"name"`
	Rules     string  `json:"rules"`
	RuleCount int     `json:"ruleCount"`
	Queries   []Query `json:"queries"`
	RepoDir   string  `json:"repoDir"`
	Notes     string  `json:"notes,omitempty"`
}

// Tree is a filesystem benchmark input: a real checkout or a generated
// synthetic repository with nested ignore files.
type Tree struct {
	Name        string  `json:"name"`
	Root        string  `json:"root"`
	Queries     []Query `json:"queries"`
	Files       int     `json:"files"`
	Dirs        int     `json:"dirs"`
	IgnoreFiles int     `json:"ignoreFiles"`
	Rules       int     `json:"rules"`
	Real        bool    `json:"real,omitempty"`
	Notes       string  `json:"notes,omitempty"`
}

// Manifest describes every generated dataset in the cache directory.
type Manifest struct {
	CacheDir   string    `json:"cacheDir"`
	GoVersion  string    `json:"goVersion"`
	GitVersion string    `json:"gitVersion"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	CPU        int       `json:"cpu"`
	RuleSets   []RuleSet `json:"ruleSets"`
	Trees      []Tree    `json:"trees"`
}

// RuleSet returns the named rule set, or an error naming the available ones.
func (m *Manifest) RuleSet(name string) (RuleSet, error) {
	for _, rs := range m.RuleSets {
		if rs.Name == name {
			return rs, nil
		}
	}
	return RuleSet{}, fmt.Errorf("rule set %q not found (have %v)", name, m.RuleSetNames())
}

// Tree returns the named tree, or an error naming the available ones.
func (m *Manifest) Tree(name string) (Tree, error) {
	for _, t := range m.Trees {
		if t.Name == name {
			return t, nil
		}
	}
	return Tree{}, fmt.Errorf("tree %q not found (have %v)", name, m.TreeNames())
}

// RuleSetNames lists rule set names in manifest order.
func (m *Manifest) RuleSetNames() []string {
	out := make([]string, len(m.RuleSets))
	for i, rs := range m.RuleSets {
		out[i] = rs.Name
	}
	return out
}

// TreeNames lists tree names in manifest order.
func (m *Manifest) TreeNames() []string {
	out := make([]string, len(m.Trees))
	for i, t := range m.Trees {
		out[i] = t.Name
	}
	return out
}

// CacheDir resolves the dataset cache: $IGNORE_BENCH_CACHE when set, otherwise
// <user cache dir>/go-ignore-bench.
func CacheDir() (string, error) {
	if dir := os.Getenv("IGNORE_BENCH_CACHE"); dir != "" {
		return filepath.Abs(dir)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "go-ignore-bench"), nil
}

// Load reads the manifest from the cache directory.
func Load(cache string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(cache, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("benchmark datasets are missing; run `go run ./cmd/benchsetup` first: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m.CacheDir = cache
	return &m, nil
}

// Environment describes the generating toolchain, recorded for reproducibility.
func Environment(gitVersion string) Manifest {
	return Manifest{
		GoVersion:  runtime.Version(),
		GitVersion: gitVersion,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		CPU:        runtime.NumCPU(),
	}
}

// Save writes the manifest to the cache directory.
func (m *Manifest) Save() error {
	m.GoVersion = runtime.Version()
	m.OS = runtime.GOOS
	m.Arch = runtime.GOARCH
	m.CPU = runtime.NumCPU()
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.CacheDir, "manifest.json"), raw, 0o644)
}
