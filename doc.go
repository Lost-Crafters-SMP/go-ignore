// Package ignore implements Git-compatible ignore matching: gitignore-style
// pattern parsing and matching with winning-rule provenance, blocking-
// ancestor reporting, explicitly ranked policy layers, nested per-directory
// rules, and a pruning filesystem walker.
//
// The
// behavioural specification and regression fixtures are in docs/spec/.

package ignore
