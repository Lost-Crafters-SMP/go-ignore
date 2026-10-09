// Package ignore implements Git-compatible ignore matching: gitignore-style
// pattern parsing and matching with winning-rule provenance, blocking-
// ancestor reporting, explicitly ranked policy layers, nested per-directory
// rules. It performs no filesystem I/O; traversal is outside the matching core.
//
// Compile owns its source bytes and returns an immutable Matcher suitable for
// concurrent use. Match accepts clean slash-relative byte paths. Directory
// endpoints use canonical slash-form semantics; only bare strict-ancestor
// exclusions establish a Block. A directory endpoint exclusion is not a pruning
// decision for a future walker. CaseFold performs ASCII-only folding.
//
// Compile builds a per-scope literal index and a priority-ordered wildcard
// list; ordinary short matches allocate nothing. The behavioural
// specification and regression fixtures are in docs/spec/.

package ignore
