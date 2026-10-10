// Package ignore implements Git-compatible ignore matching: gitignore-style
// pattern parsing and matching with winning-rule provenance, blocking-
// ancestor reporting, explicitly ranked policy layers, nested per-directory
// rules, plus an optional filesystem policy layer with reserved paths and a
// pruning walker.
//
// Compile owns its source bytes and returns an immutable Matcher suitable for
// concurrent use. Match accepts clean slash-relative byte paths. Directory
// endpoints use canonical slash-form semantics; only bare strict-ancestor
// exclusions establish a Block. A directory endpoint exclusion is not a pruning
// decision for the core matcher. CaseFold performs ASCII-only folding.
//
// Compile builds a per-scope literal index and a priority-ordered wildcard
// list; ordinary short matches allocate nothing.
//
// NewPolicy layers named, ranked ignore sources over a fixed root with lazy
// per-directory discovery, caching and reserved exclusions; its Walk prunes
// the directories Git would not descend into (reserved paths, blocking
// ancestors and bare-directory self-exclusions, never a canonical
// slash-form contents exclusion alone) and never follows symlinks. The core
// matcher stays usable without policy or walker overhead.
//
// The behavioural specification and regression fixtures are in docs/spec/.
package ignore
