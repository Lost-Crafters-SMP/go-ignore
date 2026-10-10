// Package ignore implements Git-compatible ignore matching: gitignore-style
// pattern parsing and matching with winning-rule provenance, blocking-
// ancestor reporting, explicitly ranked policy layers, nested per-directory
// rules, plus an optional filesystem policy layer with reserved paths and a
// pruning walker.
//
// Compile owns its source bytes and returns an immutable Matcher suitable for
// concurrent use. Match accepts clean slash-relative byte paths. Directory
// endpoints use canonical slash-form semantics; only bare strict-ancestor
// exclusions establish a Block. A directory endpoint exclusion alone is not a
// pruning decision: Descend returns Match(path, true)'s decision together
// with Git's traversal verdict — false only for a blocking strict ancestor or
// the directory's own bare-directory self-exclusion — so consumers that own
// their walking prune exactly like Policy.Walk. CaseFold performs ASCII-only
// folding.
//
// Compile builds a per-scope literal index and a priority-ordered wildcard
// list; ordinary short matches allocate nothing.
//
// NewBuilder constructs an incremental Builder that compiles each newly
// discovered source once while Match, Descend and Diagnostics stay available
// between additions; after accepting sources s1..sn in Add order it behaves
// exactly like Compile on the concatenated list. Build seals the builder
// permanently and publishes its existing compiled state as an immutable,
// concurrently usable Matcher. Builders themselves require external
// serialisation.
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
