// SPDX-License-Identifier: Apache-2.0

package drivers

import (
	"go.lostcrafters.com/ignore"
)

// GoIgnore adapts the library under test. Its Match returns a full decision
// with provenance and an error; the adapter reduces that to the ignored
// verdict, which is the comparable unit across libraries. The richer return
// value is part of the library's real per-call cost and is not hidden.
type GoIgnore struct{}

func (GoIgnore) Name() string  { return "go-ignore" }
func (GoIgnore) DirHint() bool { return true }

func (GoIgnore) CompileRules(text []byte) (any, error) {
	m, _, err := ignore.Compile([]ignore.Source{{Label: "bench", Data: text}}, ignore.Options{})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (GoIgnore) Match(c any, path string, isDir bool) bool {
	m := c.(*ignore.Matcher)
	d, err := m.Match(path, isDir)
	return err == nil && d.Ignored()
}
