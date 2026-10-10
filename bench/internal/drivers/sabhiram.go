// SPDX-License-Identifier: Apache-2.0

package drivers

import (
	"strings"

	sabhiram "github.com/sabhiram/go-gitignore"
)

// Sabhiram adapts github.com/sabhiram/go-gitignore. The library compiles
// pattern lines to regular expressions and offers MatchesPath without an
// isDir parameter, so directory endpoints cannot be expressed; its answers
// for directory queries are reported as API-limited, not timed as
// equivalents.
type Sabhiram struct{}

// Name identifies this driver in benchmark results.
func (Sabhiram) Name() string { return "sabhiram" }

// DirHint reports that this driver's API cannot express directory endpoints.
func (Sabhiram) DirHint() bool { return false }

// CompileRules compiles rule text without filesystem discovery.
func (Sabhiram) CompileRules(text []byte) (any, error) {
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(text), "\r\n", "\n"), "\n"), "\n")
	return sabhiram.CompileIgnoreLines(lines...), nil
}

// Match returns the ignored verdict; directory hints cannot be passed to this API.
func (Sabhiram) Match(c any, path string, _ bool) bool {
	return c.(*sabhiram.GitIgnore).MatchesPath(path)
}
