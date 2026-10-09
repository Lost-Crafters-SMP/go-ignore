# Ignore

**Git-compatible ignore matching library for Go.**

Module: `go.lostcrafters.com/ignore`.

## Development

```
mise install     # after approving: mise trust
mise run check   # build, fmt:check, lint, test, tidy:check
```

## Matching API

```go
matcher, diagnostics, err := ignore.Compile([]ignore.Source{{
    Label: ".gitignore", Rank: ignore.RankPerDirectory,
    Data: []byte("*.log\n!keep.log\n"),
}}, ignore.Options{})
// Handle err and inspect diagnostics as appropriate.
decision, err := matcher.Match("nested/keep.log", false)
// decision.Action == ignore.Include; decision.Rule provides source/line metadata.
```

Paths are clean, nonempty slash-relative byte strings; no automatic path cleaning
or filesystem access occurs. Backslashes are literal bytes. `isDir=true` selects
the canonical slash-form endpoint. `Decision.Block` identifies a strict ancestor
that prevents access; a directory endpoint exclusion alone is not a traversal block.
The matcher owns source bytes and supports concurrent calls without shared scratch.
Standard ranks are global 100, info-exclude 200, per-directory 300, command 400;
custom ranks are library-specific policy, not a Git compatibility claim.
Diagnostics use `MalformedPattern` and `OversizeSource` kinds. Invalid API inputs
wrap `ErrInvalidInput`; malformed rules are inert, while oversized sources are
wholly skipped. A zero-value matcher is usable.

## Policy and traversal API

```go
policy, err := ignore.NewPolicy(ignore.PolicyOptions{
    Root: "repo", // required; resolved once, never re-read from CWD
    Sources: []ignore.SourceSpec{
        {Filename: ".customignore", Rank: ignore.RankPerDirectory, Optional: true, Nested: true},
        {Filename: "tools/excludes", Rank: ignore.RankGlobal, Optional: true},
    },
    Reserved: []string{".git"}, // unconditional, unnegatable, subtree-covering
})
decision, err := policy.Match("nested/file.custom", false)
decision, err = policy.MatchPath(absPath, false) // lexical containment
err = policy.Walk("subtree", func(rel string, isDir bool, d ignore.PolicyDecision) error {
    if d.Ignored() {
        return nil // pruned automatically when a directory
    }
    return nil
})
```

`PolicyDecision` embeds `Decision` (winning source label, line, blocking
ancestor) plus `Reserved`. Discovery is lazy and cached per directory for the
Policy's lifetime; the first error per directory is returned deterministically.
`Walk` prunes ignored/reserved directories after reporting them, honours
`filepath.SkipDir`, aborts on the first I/O or discovery error, never follows
symlinks and treats ignore source files as ordinary candidates. Traversal roots
may be any subtree; the policy namespace stays root-relative.
