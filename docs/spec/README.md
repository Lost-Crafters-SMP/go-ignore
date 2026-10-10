# Behavioural specification

This document specifies the matching and filesystem policy contracts for
`go.lostcrafters.com/ignore`. The adjacent JSON files are executable regression
fixtures; `manifest.json` records their counts and SHA-256 checksums.

## Scope and package

Module `go.lostcrafters.com/ignore`, root package `ignore`, stdlib-only runtime.
`Compile` and `Matcher` provide pure byte-oriented matching, ordering, winning-rule
metadata and diagnostics without filesystem I/O or subprocess execution; `Builder`
compiles the same sources incrementally during lazy discovery. `Policy`
and `Walk` provide optional filesystem discovery and traversal.

Git is an **external executable oracle only**, not a code dependency. Compatibility
means high-fidelity agreement on a growing eligible corpus with recorded versions,
not a guarantee over every possible input or future release. Arbitrary custom ranks
are project semantics and need independent tests rather than fabricated Git answers.

## Matching API

- `Source`: value fields `Label string`, `Scope string`, `Rank int`, `Data []byte`.
  Label is provenance, not a filename to open. Scope `""` is root. Slice order
  assigns source sequence internally. Compile owns input bytes after return.
- `Options`: `CaseFold bool` (default false); `MaxSourceBytes int64` (0 = 100 MiB,
  positive values may lower the cap; reject negative or above-default values).
- `Compile(sources []Source, opts Options) (*Matcher, []Diagnostic, error)`.
  No partial matcher on API validation failure. Oversized sources are wholly
  skipped with one diagnostic, not partially compiled. Malformed patterns produce
  never-matching rules and diagnostics without aborting other lines.
- `NewBuilder(opts Options) (*Builder, error)` validates options exactly like
  Compile; a zero-value Builder adopts the default options. `(*Builder).Add`
  validates and compiles one batch of sources, numbered after every previously
  accepted source; after accepting s1..sn the builder's `Match`, `Descend` and
  `Diagnostics` behave exactly like those of `Compile(s1..sn, opts)`, including
  provenance and diagnostics ordering. Empty sources and wholly skipped
  oversized sources still consume sequence positions. Scoped sources may be
  added in any discovery order and stay loaded; scope eligibility keeps
  sibling branches isolated. Whenever `Add` returns an error the builder is
  unchanged, matching decisions, diagnostics, ordering and future additions
  included; error positions are cumulative across batches, and NUL is checked
  even in oversized sources. `Add(nil)` is a no-op on an open builder.
- Builders are not safe for concurrent use and require external serialisation;
  only the matcher published by `(*Builder).Build() (*Matcher, []Diagnostic)`
  carries `Matcher`'s concurrent-use guarantee. `Build` seals permanently and
  returns the builder's existing compiled storage — no recompilation, cloning
  or copying, previously returned rules unchanged — with fresh detached
  diagnostics on every call; repeated calls return the same matcher. Reads
  remain available after sealing while every `Add`, including `Add(nil)`,
  wraps `ErrInvalidInput`. An empty builder builds an empty matcher; a nil
  builder errors on Add/Match/Descend, returns nil from Diagnostics and
  (nil, nil) from Build.
- `(*Matcher).Match(path string, isDir bool) (Decision, error)`, pure and safe
  for concurrent calls. Zero-value matcher is usable and empty; nil receiver
  returns an API error. No caller input mutation or shared mutable scratch.
- `(*Matcher).Descend(path string) (Decision, bool, error)`: one evaluation of
  a directory endpoint whose `Decision` is identical to `Match(path, true)`.
  The bool is Git's descent verdict — false only when the decision carries a
  strict-ancestor `Block` or the endpoint's own positive bare-directory
  self-decision supplies its Exclude. A canonical slash-form exclusion alone
  never blocks descent. Inputs follow the Match contract; the traversal root
  is not a valid query and callers evaluate roots themselves. Pure,
  concurrent, no I/O; reserved paths are outside the matcher's rules.
- `(*Matcher).Diagnostics() []Diagnostic`: detached values in deterministic
  source/line/kind order, stable source-sequence tie-breaking.
- `Action`: `NoMatch`, `Exclude`, `Include` (zero is NoMatch).
- `Decision`: endpoint action, endpoint `*Rule` (nil for NoMatch), optional
  strict-ancestor `*Block`. `Ignored()` is true for a block or endpoint Exclude.
- `Block`: directory string (tree-root-relative, no slash suffix) and winning
  `*Rule`. Returned records must not mutate matcher state.
- `Rule`: opaque immutable metadata with value accessors `Source() string`,
  `Line() int`, `Raw() string`, `Pattern() string`, `Negated() bool`,
  `DirOnly() bool`, `Scope() string`, `Rank() int`. Stable identity within a
  matcher's lifetime; do not return mutable internal storage. Raw excludes line
  terminator; Pattern is after trimming and removing negation/directory flags.
  Verbose winning pattern is optional `!` + Pattern + optional directory `/`;
  this differs from Raw. No public standalone Rule.Match or compiled fields.
- `Diagnostic`: detached `{Source string, Line int, Pattern string, Kind,
  Message string}`. Stable kinds for malformed patterns and oversize sources;
  kinds are `MalformedPattern` and `OversizeSource`. Invalid API inputs wrap
  `ErrInvalidInput`; diagnostic message text is not a stable contract.
- Rank constants: global 100, info-exclude 200, per-directory 300, command 400.
  Fixed ranks never include directory depth. Matcher state is immutable once
  published; incremental compilation happens through `Builder` before `Build`.

Match paths are nonempty clean slash-relative byte strings: reject leading/trailing
slash, empty/`.`/`..` components and NUL. Backslash is a literal name byte, not an
OS separator. Source scopes use those rules except root `""`. No silent cleaning.
`isDir=true` selects canonical trailing-slash directory semantics described below;
directory queries without a slash are separate oracle observations. As an explicit
library input-safety contract (not a claim about Git), any NUL in Source.Data is
an API validation error with no partial matcher. Wrap underlying errors
with context and `%w`; ordinary API misuse must not panic.

## Behavioural specification

### Source lines

1. Ignore a UTF-8 BOM at the start of a source. Handle newline-separated physical
   lines, optional final newline and CRLF; count blank/comment lines in line numbers.
2. A truly empty line or line whose **first byte** is `#` creates no rule. Leading
   spaces are significant; ` #name` is not a comment. Tabs are not trimmed as spaces.
3. Remove only unescaped trailing ASCII spaces. Backslash escapes the next byte;
   odd/even escape runs matter. Retain the escape in pattern text until interpreting
   the pattern; Raw preserves the physical pre-trim line.
4. Leading unescaped `!` negates a rule. A final `/` marks a directory-only rule
   and is removed from its match text. A leading `/` anchors to source scope.
5. A nonempty whitespace-only line becomes a **live empty basename rule**. A lone
   `!` is its negation; lone `/` is a live directory-only empty rule observed at
   slash-form directory endpoints. Do not drop an empty post-trim rule. Use
   isolated cases: a later negation must not mask the error. Truly blank is different.
6. Malformed/unclosed classes, unknown POSIX classes and a trailing lone escape
   never match. Produce diagnostics, leave later rules active. Validate both ordinary
   and escaped-separator pattern shapes consistently.

### Pattern bytes, anchoring and directory candidates

- Matching is byte-oriented, no Unicode normalisation. Optional case folding is
  ASCII only and applies to literals, scopes, class members and ranges. High-bit
  bytes do not acquire Unicode class or case behaviour.
- `?` matches one non-separator byte; ordinary `*` matches zero or more non-separator
  bytes. Runs of stars embedded in a component behave as ordinary stars.
- A run of at least two stars has directory-crossing meaning only when bounded
  on the left by pattern start or slash, and on the right by pattern end, slash,
  or escaped slash. Leading/interior double-star directory spans may consume zero
  directories. Trailing `a/**` matches entries inside `a`, not bare `a` as a file;
  a canonical directory `a/` has a distinct observable endpoint described below.
- `\x` represents literal byte x. Escaped `/` may match a real path separator:
  `foo\/bar` matches `foo/bar`. Do not lose this behaviour by decomposition.
- Classes support leading `!`/`^` negation, initial literal `]`, unsigned-byte
  ranges, literal end `-`, escaped members and ASCII POSIX classes: alnum, alpha,
  blank, cntrl, digit, graph, lower, print, punct, space, upper, xdigit. A class
  never consumes `/`. A slash inside `a[b/c]d` is class content, not a component
  split: `abd` and `acd` match, `a/d` does not. Reversed ranges add no range members;
  interactions with folding are covered by regression tests.
- A pattern with no slash applies to basenames at any depth below its scope.
  A leading or contained slash anchors relative to scope. The raw presence of a
  slash inside a class or escape still distinguishes a path pattern from a basename
  pattern. Source rules do not apply to ancestors/siblings or their bare containing
  directory; a slash-form endpoint can have an empty relative basename and consult
  that directory's own source (see the isolated fixture). Do not collapse bare and
  slash-form candidates into a universal strict-scope assertion.
- The first excluded strict ancestor's **bare-directory** decision blocks all
  descendants. A deeper negation cannot reopen it and sources below it do not
  contribute. Report that ancestor/rule with NoMatch and nil endpoint rule.
  Slash-form exclusion alone is not a traversal block: `a/**` can exclude `a/`
  while `!a/keep` still includes the child; an own-source empty rule can exclude
  `a/` without excluding `a/keep`. Never use a canonical slash-form Match result
  as proof of a strict-ancestor block. The walker contract is described below.
- Canonical directory queries use a slash-suffixed endpoint with an empty final
  basename; patterns such as `d/*`, `d/**` or empty basename rules can match `d/`.
  Direct exclusion is Exclude with a rule, **not** a block naming the endpoint.
  Preserve Git-observed winning provenance, not just the bool. The behavioural
  rule is: with no strict ancestor block, a positive bare-directory self-decision
  supplies direct Exclude and provenance. Otherwise use the slash-form endpoint's
  winner, including applicable own-directory sources. A bare negation permits
  traversal but is not automatically the slash endpoint's winner: `a/` then `!a/`
  gives Include for bare `a`, but NoMatch for `a/`; `*` then `!*` gives Include
  for both forms. `e/f` then `e/**` gives line 2 because the latter also matches
  the bare descendant `e/f`. This states observable outcomes, not an engine's
  control-flow prescription. The evaluator may compute them in any original way.
- Directory-only rules never exclude a file endpoint. A positive bare-name rule
  can exclude a directory and block descendants. A negation that wins at a parent
  before it becomes excluded prevents that block.

### Ordering and output

Each matching rule has lexicographic key `(rank, scope-depth, source-sequence,
physical-line)`. Greatest key wins. Source sequence is input-slice order, not label
sorting; line orders within a single source. Standard global/info/command sources
are root-scoped; custom scoped sources use scope depth within their fixed rank.
Increasing depth never defeats a higher rank. A blocked path's winner is the block
rule; no speculative endpoint history. Preserve source label, line, negation,
directory-only flag and reconstructed verbose pattern even when not ignored.

## Black-box corpus and oracle protocol

`corpus.json` and `directory-cases.json` are a behavioural corpus, not
a claim of exhaustive compatibility. Each case has a
unique ID, explicit ignore files, one query and independently stated expected Git
verdict/provenance. Cases are isolated, so a path is not simultaneously a file and
directory. These cases are not copied upstream test tables. Their expected outputs
are checked by executing Git in scratch repositories, never reading matcher code.
Expand cases monotonically and record exact version/build, platform and eligibility
when running the live oracle tests.
Every case's origin is the file's project-authored origin plus that case's exact
files/query/expected data. A `block` record is the library's strict-ancestor
projection verified by additional bare-directory probes, not a Git output field.
Absence of `block` means no strict ancestor block, not untested block metadata.
For slash-form queries the expected library action is Exclude for a positive
winning endpoint, Include for a negated winning endpoint, NoMatch for no winner;
when blocked it is NoMatch with nil endpoint rule and the specified Block.
NoSlash cases are oracle-only contrasts, not an additional public Match mode.

`policy-cases.json` isolates **library-only** ordering from byte-pattern matching:
rules have fixed rank/depth/source-sequence/line/action; candidates list the
applicable matching rule IDs, ordered as strict ancestors then endpoint. Its
expected action/rule/block is a specification assertion, not Git evidence.
Rule IDs identify provenance; in blocked results the ID belongs to Block.Rule,
not an endpoint rule. Tests combine this policy with actual
Source inputs and pattern matching; the supplied abstraction tests precedence,
not parser correctness.

The runner in `spec_test.go` uses this protocol:
create an isolated untracked scratch repo, materialise files/directories faithfully,
configure excludes/case explicitly, suppress inherited Git/home/XDG/template config,
then, in fresh processes **for each distinct query**, run
`git check-ignore -z --stdin` for verdicts and
`git check-ignore -v -z --stdin --non-matching` for winning provenance. Use
NUL-terminated exact query arguments. Fresh processes are a conservative reproducible
protocol, not evidence of an established batching defect. Any future batching
optimisation must independently verify complete results and order invariance.
Verbose records have four NUL fields:
source, line, reconstructed pattern, query. Verbose negation output is **not** an
ignored verdict; a positive winning empty pattern may have an empty pattern field.
Exit 0/1 are normal; other exit codes or stderr errors require investigation.

Pin Git ≥2.52 and record full build; do not assume other versions agree. Query
identity includes case/fixture version, exact path bytes, dtype, slash form and case
mode. Deduplicate identical semantic cases; report repeat execution separately.
Every unique query has eligible result or explicit reasoned ineligibility. Missing,
duplicate or unexpected records fail closed; compare provenance in both directions.
Unmaterialised or wrong-dtype queries are not successful parity evidence. Keep legal
POSIX names even if Windows cannot create them; rerun them where representable.
Do not conflate slash/no-slash directory arguments or case-folding modes.

## Filesystem policy and traversal

- `NewPolicy(PolicyOptions) (*Policy, error)` validates a required root and a
  nonempty source list, copies configuration, and resolves the root once to an
  absolute cleaned path. Construction does not open files or require an existing
  root. `Root()` returns that resolved path.
- `SourceSpec` supplies `Filename`, fixed `Rank`, `Optional` and `Nested`.
  Filenames are clean slash-relative paths; nested filenames must be basenames.
  Spec order breaks equal rank/depth ties. There is no implicit Git config lookup.
- `Policy.Match` accepts the matching path contract above. Discovery loads the
  root-to-parent chain, not the endpoint directory's own sources. Each source is
  read at most once per Policy; loaded rules and the first error per directory
  remain cached. Construct a new Policy to observe filesystem changes.
- Optional sources ignore absence and host errors indicating that the directory
  cannot exist; existing unreadable files and required-source failures are errors.
  NUL source bytes are errors. Source labels are root-relative filenames.
- `PolicyDecision` embeds `Decision` and adds `Reserved`. Reserved clean paths
  exclude themselves and descendants before discovery, without a Rule; they are
  byte-exact, unaffected by folding, and cannot be negated.
- `MatchPath` requires an absolute path lexically inside the root, excluding the
  root itself. It does not detect physical escapes through intermediate symlinks.
  Discovery follows host file-opening semantics; Windows name normalization and
  case-insensitive filesystem lookup differ from POSIX byte-faithful names.
  Reading a FIFO source can block.
- `Diagnostics()` returns detached, deterministic source/line/kind-sorted
  diagnostics for discovered sources, reporting each source file once. Policy
  supports concurrent matching, diagnostics and walking.
- `Walk(start, callback)` accepts a root-relative subtree (including `""`) or an
  absolute subtree inside the root. Paths passed to the callback remain
  policy-root-relative. The traversal root is not reported; if traversal of it
  is blocked (reserved, blocked by an excluded strict ancestor, or excluded by
  its own bare-directory decision) nothing is visited. Entries are visited
  lexicographically; symlinks are reported as non-directories and never followed.
  Source files are ordinary candidates. Pruned directories are reported and then
  pruned. `filepath.SkipDir` is honored; other callback errors abort unchanged,
  and filesystem/discovery errors abort with path context.

Traversal pruning follows Git's descent rule, not the canonical directory
endpoint verdict. A directory is pruned when it is reserved, when a strict
ancestor is excluded, or when its own bare-directory decision is a positive
exclusion (including directory-only patterns): conditions under which Git
never descends and no deeper negation can re-include anything. A directory
excluded only in canonical slash form (its contents rather than itself, such
as `bin/*` matching `bin/`) remains traversable: `bin/*` followed by
`!bin/keep` visits `bin`, reports its ignored children and re-included
`bin/keep`, matching `git status` traversal, while the decision reported for
`bin` itself remains the canonical slash-form result (excluded by `bin/*`).
Canonical directory matching and traversal blocking are distinct contracts.
`Matcher.Descend` exposes this descent verdict for precompiled rules, so
consumers that own their traversal prune exactly like the walker without
filesystem access or policy discovery.
File discovery is verified against real Git traversal (`git status
--ignored=matching -uall` and `git ls-files --cached --others
--exclude-standard`) in the live Git and benchmark correctness tests. See the
[benchmark methodology](../../bench/README.md#correctness-verification).

## Regression and resource requirements

- Check verdict, source/line/pattern and blocking metadata, not only booleans.
  Keep eligible, skipped, duplicate and failed oracle queries separate.
- The slow reference uses no production parser/class helpers. Deterministic
  comparisons, parser/equivalence/index/policy fuzz targets and minimized inputs
  in `testdata/fuzz/` cover escapes, classes, folding and invalid patterns.
  CI runs parser and pattern equivalence fuzzing for 1,000,000 executions each.
- Matching work and scratch must be bounded, without unbounded backtracking.
  An isolated 1 MiB literal source must retain at most 8× input plus 64 KiB
  metadata. Resource tests cover stars {8,16,24}, lengths {64,256,1024}, classes,
  directory adversaries and source-cap boundaries. Timeouts fail in killable
  child processes, not detached goroutines.
- Benchmark compilation and matching across rule counts and depths, including
  include/exclude/miss/block/directory/cross-separator outcomes. Track time,
  allocation bytes/counts, retained heap and peak RSS separately. Low allocation
  counts are measured objectives, never permission to weaken semantics.
- CI covers Linux, macOS and Windows, with race checks, live Git and resource
  tests. Local opt-in commands and benchmark reproduction are in
  [the repository README](../../README.md#development) and
  [benchmark documentation](../benchmarks/README.md).
