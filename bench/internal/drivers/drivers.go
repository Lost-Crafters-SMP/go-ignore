// SPDX-License-Identifier: Apache-2.0

// Package drivers wraps the three compared libraries behind one minimal
// adapter surface. Adapters use only public APIs and perform no extra work
// beyond what each library's ordinary usage requires.
package drivers

// Driver adapts one library to the benchmark harness. Match returns the
// library's ignored-verdict for one path; libraries without an isDir
// parameter (DirHint false) answer from the path alone and are reported as
// semantically limited rather than wrong.
type Driver interface {
	Name() string
	DirHint() bool
	// CompileRules builds one matcher from a single ignore file's text.
	CompileRules(text []byte) (any, error)
	// Match evaluates one path against a matcher from CompileRules.
	Match(c any, path string, isDir bool) bool
}

// All lists the drivers in stable comparison order.
func All() []Driver {
	return []Driver{GoIgnore{}, GitPkgs{}, Sabhiram{}}
}

// ByName resolves a driver by name.
func ByName(name string) (Driver, error) {
	for _, d := range All() {
		if d.Name() == name {
			return d, nil
		}
	}
	return nil, errUnknown{name}
}

type errUnknown struct{ name string }

func (e errUnknown) Error() string { return "unknown driver " + e.name }
