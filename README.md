# Ignore

**Git-compatible ignore matching library for Go.**

Module: `go.lostcrafters.com/ignore`.

The library provides byte-exact gitignore pattern matching, winning-rule metadata,
strict-ancestor blocking, fixed-rank policy layers, lazy nested source discovery
and a pruning walker. The runtime uses only the Go standard library.

- [Behavioural specification and fixtures](docs/spec/README.md)
- [Implemented architecture](docs/design.md)
- [Library benchmarks](docs/benchmarks/README.md)
- [Comparative benchmark methodology and results](bench/README.md)

Licensed under [Apache-2.0](LICENSE.txt).

## Development

```
mise install     # after approving: mise trust
mise exec -- hk fix
mise exec -- hk check --all # formatting, build, lint, tidy and repository hygiene
mise run test              # tests remain independent of code-quality checks
```

Additional checks use the pinned tools:

```sh
mise run test:race # requires a C compiler
mise run vuln     # requires network access
mise exec -- hk check --all --step lint
mise exec -- hk check --all --profile bench # static checks; no datasets needed
mise exec -- hk fix --all --profile dependencies --step tidy # explicit dependency edits
# With IGNORE_LIVE_GIT=1 (external Git >=2.52):
mise exec -- go test . -run '^TestLiveGit' -count=1 -timeout=300s
# With IGNORE_RESOURCE=1:
mise exec -- go test . -run '^Test(KillableScaling|RetainedLiteralMemory|RetainedRuleSets|DefaultCapBoundaries)$' -count=1 -timeout=180s
```

CI runs tests, race checks, live-Git comparisons and resource checks on Linux,
macOS and Windows. Oracle tests compare decisions and winning-rule metadata;
custom ranks and reserved paths are library-specific contracts.

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

`Walk` prunes canonical directory endpoint exclusions, even when they are not
strict-ancestor blocks. Under `bin/*` followed by `!bin/keep`, it may prune `bin/`
and omit a child that pure matching includes. This differs from `git status`
traversal; see the [documented comparison](bench/README.md#correctness-verification).
`MatchPath` containment is lexical, not a security boundary against symlink escapes.
