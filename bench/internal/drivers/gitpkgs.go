// SPDX-License-Identifier: Apache-2.0

package drivers

import (
	gitpkgs "github.com/git-pkgs/gitignore"
)

// GitPkgs adapts github.com/git-pkgs/gitignore. Programmatic construction
// uses an empty root (no filesystem or global excludes loading) plus
// AddPatterns, which is the documented way to build a matcher from rule
// text alone.
type GitPkgs struct{}

// Name identifies this driver in benchmark results.
func (GitPkgs) Name() string { return "git-pkgs" }

// DirHint reports support for directory endpoint queries.
func (GitPkgs) DirHint() bool { return true }

// CompileRules compiles rule text without filesystem discovery.
func (GitPkgs) CompileRules(text []byte) (any, error) {
	m := gitpkgs.New("")
	m.AddPatterns(text, "")
	return m, nil
}

// Match returns the ignored verdict for a compiled matcher.
func (GitPkgs) Match(c any, path string, isDir bool) bool {
	return c.(*gitpkgs.Matcher).MatchPath(path, isDir)
}
