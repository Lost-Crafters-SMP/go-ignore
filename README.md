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

### Directory descent

`Descend` answers Git's traversal question for consumers that own their
filesystem walking: whether a directory may be entered, evaluated in the same
pass as its canonical decision.

```go
matcher, _, _ := ignore.Compile([]ignore.Source{{
    Label: ".gitignore", Rank: ignore.RankPerDirectory,
    Data: []byte("build/\nbin/*\n!bin/keep\n"),
}}, ignore.Options{})

d, descend, _ := matcher.Descend("build")
// d.Action == ignore.Exclude and descend == false: build itself is
// bare-excluded, no deeper negation can re-include anything, so a walker
// prunes it.

d, descend, _ = matcher.Descend("bin")
// d.Action == ignore.Exclude yet descend == true: only bin's contents are
// excluded (bin/* matching the canonical slash form), so bin/keep is still
// discoverable, exactly as in git status traversal.
```

`d` is always identical to `Match(path, true)`. `descend` is false only for a
blocking strict ancestor (`d.Block != nil`) or the directory's own
bare-directory self-exclusion — the conditions under which Git descends no
further, and the exact pruning predicate `Policy.Walk` applies. Reserved paths
and the traversal root itself remain the caller's concern: `Descend` follows
`Match`'s input contract (the root has no path below the source scopes),
performs no filesystem access or policy discovery, and a zero-value matcher
descends everywhere.

### Incremental compilation

`NewBuilder` compiles lazily discovered sources once and keeps matching
available between discoveries, so `Descend` can decide whether a directory is
worth entering before its own sources are loaded:

```go
builder, err := ignore.NewBuilder(ignore.Options{})
err = builder.Add([]ignore.Source{{
    Label: ".gitignore", Rank: ignore.RankPerDirectory, Data: rootBytes,
}})

if _, descend, _ := builder.Descend("vendor"); descend {
    // Enter the branch: load vendor's sources only when traversal reaches it.
    err = builder.Add([]ignore.Source{{
        Label: "vendor/.gitignore", Scope: "vendor",
        Rank: ignore.RankPerDirectory, Data: vendorBytes,
    }})
}
matcher, diagnostics := builder.Build() // optional: publish for concurrent use
```

After accepting sources s1…sn, the builder matches, descends and reports
diagnostics exactly like `Compile` on the concatenated list; every accepted
source compiles once and later batches never rewrite published rules. Failed
additions change nothing, and error positions are cumulative. `Build` seals
permanently, returns the existing compiled storage without recompilation or
copying, and repeated calls return the same matcher with fresh detached
diagnostics; reads keep working after sealing while every `Add` fails.
Builders are not safe for concurrent use — external serialisation is the
caller's — but the published matcher is. Scoped sources may stay loaded across
sibling visits: scope eligibility keeps one branch's rules out of another
branch's matches.

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
`Walk` prunes exactly the directories Git would not descend into (reserved
paths, directories blocked by an excluded strict ancestor, and directories
excluded by their own bare-directory decision) after reporting them, honours
`filepath.SkipDir`, aborts on the first I/O or discovery error, never follows
symlinks and treats ignore source files as ordinary candidates. Traversal roots
may be any subtree; the policy namespace stays root-relative.

A directory excluded only by its canonical slash-form decision (its contents
rather than itself, such as `bin/*` matching `bin/`) remains traversable, so
`bin/*` followed by `!bin/keep` discovers `bin/keep` exactly as `git status`
does. The decision reported for such a directory is still its canonical
slash-form `Match` result; matching and traversal blocking are distinct
contracts. `MatchPath` containment is lexical, not a security boundary
against symlink escapes.
