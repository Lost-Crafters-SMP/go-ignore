# Architecture

`go.lostcrafters.com/ignore` is a stdlib-only Go library. The root package separates
pure compiled matching from optional filesystem discovery and traversal. The
[specification](spec/README.md) defines behaviour; this document describes how the
current implementation supports it.

## Compilation and storage

`ignore.go` validates all source scopes, NUL bytes and options before compilation.
Oversized sources are skipped whole with a diagnostic. Physical line parsing
preserves raw text and line numbers, handles BOM/CRLF and escaped trailing spaces,
and keeps malformed rules inert without disabling later rules.

`pattern.go` compiles byte patterns into literal, star, cross-directory star,
directory-span, single-byte and character-class instructions. Classes use inline
256-bit sets; case folding is ASCII-only. Escape-free literal runs, raw lines,
patterns and matching bounds share the owned source-string copy. Escaped runs
use decoded strings.

Rule storage is allocated in stable batches of up to 256 records, with smaller
exact-sized batches for small compilations. Programs occupy stable instruction
batches: a program fits wholly within one batch, normal batches hold at most 256
instructions, and larger programs receive exact-sized storage. Published slices
never require growing or copying their backing arrays. Rule pointers stay stable
for the matcher's lifetime. Deterministic diagnostics are returned as detached
values, and caller source bytes are not retained or mutated.

## Incremental compilation

`builder.go` shares the parsing and program-compilation phase with `Compile`
through `compiler`, which owns the rule and instruction arenas across batches so
accepted sources are never parsed or compiled twice; one-shot `Compile` still
builds its index in one bulk pass. After the first batch the program arena is
warm: new program stores are sized to cover as much work again as everything
stored so far, capped at one normal batch, so successive small additions share
one store instead of allocating exact-sized arrays per program. Cold arenas —
every one-shot `Compile` and a builder's first batch — keep exact sizing, tiny
histories keep proportionate sizing, and rule chunks always stay exact-sized
because their large slots make spare capacity a poor trade against the few chunk
objects it saves. `Builder.Add` validates a whole batch
(cumulative error offsets, NUL even in oversized sources), checks cumulative
source, rule and scope counts against index representation limits, and compiles
the batch into unpublished storage. Only then does an infallible commit run:
diagnostics are appended, new scopes are assigned stable IDs, the batch is
spliced into the priority-ordered rule slice — appending when every new rule
outranks the existing set — wildcard positions are remapped through a shift
table, only the literal entries whose winners moved are repaired (in descending
old-position order, so a moved winner's new position can never be mistaken for
a shadowed rule's old position), and new literals update per-scope maps
presized to the batch while anchored key-length bounds grow. Every `Add` error
therefore leaves the matcher, diagnostics, source ordering and future
additions untouched; malformed patterns stay nonfatal diagnostics, and no
partial-update panic is converted into an ordinary error.

Displaced rules keep their identity: no previously published `Rule` field is
rewritten, so provenance returned earlier stays valid, and source bytes are
owned after `Add` returns. Sibling scopes stay loaded in one matcher while
scope eligibility isolates their rules. `Diagnostics` sorts the cumulative
slice when new entries arrive; stable sorting makes that order identical to a
one-shot sort of the same append sequence. `Build` seals the state shared by
every copy of a handle, drops compilation scratch and bookkeeping, and returns
the existing matcher storage as an immutable, concurrently usable `Matcher`.

## Priority and candidate selection

Rules are sorted by `(rank, scope depth, source sequence, physical line)`; the
greatest key wins. Depth never overrides rank. Each scope has exact-match maps
for literal and empty programs, keyed by relative path for anchored rules and
basename otherwise. Entries distinguish directory-only applicability. Folded
matchers fold index keys during compilation; anchored key lengths bound lookups.

A bare-candidate evaluation takes the best eligible literal hit as its priority
lower bound, then scans wildcard candidates above that bound in reverse priority
order. Literal head/tail checks reject impossible wildcard candidates before
evaluation. Scope eligibility and basenames are reused within a call. Canonical
slash-form directory endpoints use exhaustive scanning, preserving their virtual
trailing separator and empty-basename semantics.

## Evaluation and blocking

The evaluator propagates reachable byte positions through instructions with two
rows of scratch. It does not recursively backtrack over wildcard alternatives.
Work scales with compiled units and candidate length; scratch scales with candidate
length. Small rows use fixed stack storage, and larger rows are allocated once
and reused across rules and ancestor prefixes within a call. Matcher state is
immutable, with no shared mutable scratch or match-time I/O.

Strict ancestors are considered from shallowest to deepest as bare directories.
The first positive winning rule returns a detached `Block`, with `NoMatch` and no
endpoint Rule. A deeper negation cannot reopen that ancestor. Without a block, a
positive bare self-decision supplies a directory endpoint's exclusion; otherwise
the canonical slash-form winner supplies its action and metadata. This distinction
keeps endpoint exclusions separate from strict-ancestor blocks.

`Match` and `Descend` share one evaluation (`Matcher.decide`): the decision is
identical, and `Descend` reports the bare-directory self-exclusion beside
ancestor blocking as Git's descent verdict, so consumers that precompile their
sources prune without re-evaluating or probing.

## Filesystem policy

`policy.go` resolves and copies configuration in `NewPolicy` without opening files.
Named `SourceSpec` entries control ranks, nesting, optional absence and tie order;
reserved byte-exact paths are unnegatable subtree exclusions checked before I/O.
`MatchPath` uses lexical containment against the fixed absolute root, not physical
symlink confinement.

Lazy discovery caches each directory's own compiled matcher, diagnostics, first
error and parent link. Source files are read once for the Policy's lifetime;
loaded bytes are released after compilation. A mutex serializes discovery and
diagnostic collection; queries evaluate immutable matchers outside it. A new Policy
is required to observe filesystem changes.

Queries compose winners along the parent chain under the same global priority
order as compiling all applicable sources together. Ancestor eligibility is
structural: directory depths are compared with prefix component counts instead
of repeatedly comparing scope bytes. Basenames are precomputed. The warm blocking
scan still has a quadratic scope/prefix pair count in deep chains, while retained
compiled state stores each discovered directory's sources only once.

Discovery follows host open semantics, including symlinked source files. Optional
sources treat nonexistent or host-unrepresentable directories as absent; required
or unreadable sources fail deterministically. Diagnostics label sources with
root-relative filenames and report each discovered source once.

## Traversal

`walker.go` performs sequential, lexicographically ordered traversal of a selected
subtree while keeping callback paths policy-root-relative. It does not report
the traversal root or follow symlinks. It reports pruned directories before
pruning them, honors `filepath.SkipDir`, and aborts on the first other
callback, filesystem or discovery error. Source files are ordinary candidates.
Directory decisions consult parent scopes, not that directory's own sources.

Pruning follows Git's descent rule: a directory is pruned when reserved, blocked
by an excluded strict ancestor, or excluded by its own bare-directory decision,
which the shared evaluation in `policy.go` (`decide`) computes alongside the
canonical decision at no extra matching cost. A canonical slash-form exclusion
alone (contents excluded, the directory itself not) leaves the directory
traversable, so `bin/*` with `!bin/keep` discovers the child exactly as
`git status` does, while the decision reported for `bin/` remains the canonical
slash-form result. `Matcher.Descend` exposes the same verdict from precompiled
rules for consumers that own their traversal. See the
[specification](spec/README.md#filesystem-policy-and-traversal) and
[benchmark methodology](../bench/README.md#correctness-verification).

## Tests and measurements

Specification fixtures assert actions, winning-rule metadata and blocks. A slow
reference, exhaustive-index differential tests, composition models, fuzz corpora,
filesystem tests and concurrent-use tests guard the implementation. Builder
tests verify exact equivalence with cumulative one-shot compilation after every
addition — decisions, descent verdicts, diagnostics and provenance — beside
lifecycle, ownership, fault-injected error atomicity and concurrent reads of the
published matcher. Live Git
tests exercise an isolated external executable, including layered sources; custom
ranks and reserved paths use library-contract tests rather than Git assertions.
Killable adversarial tests bound work and verify retained memory and source caps.

[Library benchmark records](benchmarks/README.md) and the isolated
[comparative benchmark module](../bench/README.md) retain reproduction commands,
raw runs, measured costs and limitations. Inline class sets, literal indexing,
stable storage batches and per-directory composition balance matching speed with
retained metadata, index memory and compilation costs.
