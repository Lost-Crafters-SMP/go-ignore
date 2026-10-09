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
wholly skipped. A zero-value matcher is usable. Traversal is not implemented.
