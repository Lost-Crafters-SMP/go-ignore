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

func (Sabhiram) Name() string  { return "sabhiram" }
func (Sabhiram) DirHint() bool { return false }

func (Sabhiram) CompileRules(text []byte) (any, error) {
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(text), "\r\n", "\n"), "\n"), "\n")
	return sabhiram.CompileIgnoreLines(lines...), nil
}

func (Sabhiram) Match(c any, path string, isDir bool) bool {
	return c.(*sabhiram.GitIgnore).MatchesPath(path)
}
