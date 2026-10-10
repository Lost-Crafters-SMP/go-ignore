# Comparative benchmark suite

A reproducible, Git-oracle-verified benchmark harness comparing
`go.lostcrafters.com/ignore` against two public Go gitignore libraries on
shared functionality (Tier 1), filesystem integration (Tier 2) and
go-ignore's incremental feature costs (Tier 3). The suite lives in its own
Go module so competitor dependencies never enter the production module.

The goal is a fair, independently reproducible comparison — not proof that
go-ignore is fastest. Unfavourable results are reported alongside the rest.

## Layout

- `cmd/benchsetup` — materializes all datasets into a cache directory
  (`$IGNORE_BENCH_CACHE`, default `<user cache dir>/go-ignore-bench`),
  labelling every query with `git check-ignore` (black-box oracle).
- `internal/dataset` — dataset types, deterministic generators, pinned
  moby fetcher.
- `internal/oracle` — Git oracle with isolated configuration.
- `internal/drivers` — one thin adapter per library, public APIs only.
- `tier1_test.go`, `tier2_test.go`, `tier3_test.go`, `mem_test.go`,
  `correctness_test.go` — the benchmarks and correctness gates.
- `results/` — raw benchmark output, benchstat summaries, correctness and
  memory transcripts, dataset manifest, environment record.

## Reproducing

```sh
cd bench
mise exec -- go run ./cmd/benchsetup                      # downloads moby once (~40 MB), builds datasets
mise exec -- go test . -run 'TestCorrectness|TestPairEquivalence|TestRankAndReservedEquivalence|TestWalkerEquivalence' -count=1 -v | tee results/correctness.txt
mise exec -- go test . -run '^$' -bench 'BenchmarkTier1' -benchtime=1s -count=6 | tee results/tier1.txt
mise exec -- go test . -run '^$' -bench 'BenchmarkTier2' -benchtime=1s -count=6 | tee results/tier2.txt
mise exec -- go test . -run '^$' -bench 'BenchmarkTier3' -benchtime=1s -count=6 | tee results/tier3.txt
mise exec -- go test . -run 'TestRetainedMemory' -count=1 -v | tee results/memory.txt
mise exec -- go tool benchstat results/tier1.txt           # etc. -> *.stats.txt
```

Results in `results/` were produced on Windows 11, Go 1.27.1, git
2.56.0.windows.1, AMD Ryzen 7 5800X3D (see `results/environment.txt` and
the manifest for full dataset provenance). For stable numbers on a noisy
machine, stop background load (on this host: `wsl --shutdown`) before
measuring.

## Competitors and pins

| Library | Version | Engine | Notes |
| --- | --- | --- | --- |
| go.lostcrafters.com/ignore | local module (`replace ../`) | literal index + wildcard programs | library under test |
| github.com/git-pkgs/gitignore | v1.3.0 | wildmatch backtracking, suffix fast-reject | full-featured competitor |
| github.com/sabhiram/go-gitignore | v0.0.0-20210923224102-525f6e181f06 (latest, untagged) | compiled regular expressions | minimal competitor |

Pins live in `bench/go.mod` / `bench/go.sum`. Adapters call only public APIs.

## Capability matrix

| Capability | go-ignore | git-pkgs | sabhiram |
| --- | --- | --- | --- |
| Compile rule text | `Compile` | `New("")+AddPatterns` | `CompileIgnoreLines` |
| isDir-aware matching | `Match(path, isDir)` | `MatchPath(path, isDir)` | — (path only) |
| Ancestor blocking | yes (Block provenance) | implicit (verdict only) | — |
| Nested discovery | lazy per chain (`Policy`) | eager whole tree (`NewFromDirectory`) | — |
| Traversal + pruning | `Policy.Walk` | `Walk`, `WalkFrom` | — |
| Match provenance | `Rule` (source, line, pattern) | `MatchDetail` | `MatchesPathHow` (line) |
| Ranks / custom precedence | yes | — | — |
| Reserved paths | yes | — (`.git` hardcoded in walk) | — |

Unsupported functionality is marked, never emulated with wrapper code that
would penalize a competitor or conceal setup cost.

## Datasets

Real world — `moby/moby` @ `9fabd6dfbb926381278c436a866bc8d6ac222274`
(2026-10-09, master head at selection): 3,654 directories, 16,889 files
after materializing untracked query candidates (13,278 tracked files at
that commit), 100 `.gitignore` files across depths 0–7, 1,017 rules,
1,600 oracle-labelled queries (700 ignored) mixing tracked files, ignored
artefacts (`.orig`, `.exe`, `bin/…`, …) and clean untracked names.

Synthetic — deterministic generators (`math/rand/v2` PCG, fixed seeds,
parameters in `internal/dataset/generate.go` and the manifest `Notes`):

- Rule sets (single source, 512 labelled queries each): `mixed-{10,100,
  1000,10000}` (40% bare literal / 20% bare wildcard / 15% each anchored
  literal+wildcard / 8% dir-only / 2% char class, ~8% negations), category
  sets `lit-100`, `wild-100`, `anchored-100`, `dir-100`, adversarial
  `wild-complex` (500 two/three-star, `**` and character-class patterns),
  and `moby-root` (moby's real root rules over moby-derived paths).
- Trees: `nested-d2/d8/d32` (chain + side dirs, scoped rules per
  directory), and the `pair-nested`/`pair-single` equivalence pair — the
  identical bare-rule block either duplicated in every directory or placed
  once at the root — which by construction decides every query
  identically (asserted in `TestPairEquivalence`).

Query labels are always Git's verdict (`check-ignore`, batched,
materialized paths, isolated config, `core.longpaths` enabled for the deep
trees), never the generator's intent.

## Methodology and fairness decisions

For the focused retained-storage follow-up, reuse the existing cache without
running setup again: the historical moby setup can resample previously generated
artifacts. Before/after runs must use the same cached manifest and tree. The
follow-up is a within-library comparison, not a refreshed competitor ranking.

- One `op` is one compilation (compile benchmarks), one query (match and
  warm benchmarks), one construct+serve cycle (cold-sweep) or one full
  traversal (walk). Sinks defeat dead-code elimination; construction,
  compilation, discovery and warm matching are separated into distinct
  benchmarks; `b.Loop` timers exclude setup.
- Every direct comparison feeds identical rule text, identical query order
  and identical trees to each library. Where a library's API returns less
  (bool vs decision+error) that is its real per-call cost, not hidden.
- Cold shapes differ by design: go-ignore discovers lazily per directory
  chain, git-pkgs loads the whole tree eagerly. Both `cold + 1 query` and
  the equalizing `cold + full query sweep` are reported.
- git-pkgs' filesystem constructors may consult git config; the process
  environment isolates system/global config exactly like the oracle's.
- Retained memory is a GC-before/after `HeapAlloc` median (5 trials),
  order-of-magnitude only.
- Statistics: 6 samples per benchmark, `benchstat` medians and confidence
  ranges; significance follows its nonparametric comparison test. Overlapping
  median confidence intervals alone are not a test of the between-library
  difference. Small samples and this single-host run still limit conclusions.

## Correctness verification

`go-ignore` matched the Git oracle on **every** query of every dataset
(0 mismatches across ~6,600 labelled queries), including directory
endpoints. `git-pkgs` also matched everywhere. Divergences found:

- `sabhiram` misses some `?.ext` single-character wildcard patterns
  (`?.swp` vs `q.swp`: Git ignores, sabhiram does not — 3/268 on
  `mixed-100`, 3/266 on `wild-100`), and cannot express directory
  endpoints (25 dir queries on `dir-100` are API-limited, counted
  separately, not timed as equivalents).
- **Walker descent rule (corrected):** the walker prunes exactly where Git's
  own traversal prunes (reserved paths, excluded strict ancestors and
  bare-directory self-exclusions) and descends through directories excluded
  only in canonical slash form (a contents glob such as `bin/*` matching
  `bin/`), where `git status` also descends and re-includes content such as
  `!bin/keep`. The decision reported for such a directory remains its
  canonical slash-form verdict (`check-ignore bin/`); that is a matching
  contract, not a traversal decision. `TestWalkerEquivalence` verifies
  included-file discovery against `git ls-files --cached --others
  --exclude-standard` less `git ls-files --cached --ignored
  --exclude-standard` (rule-based discovery semantics, since a walker has
  no index; exact set equality on `nested-d8` and the full moby tree) with
  git-pkgs as a secondary file-set cross-check. Directory entries
  are not compared across libraries: git-pkgs silently skips ignored
  directories and has no canonical directory semantics, so its reporting
  conventions must not define go-ignore's contract; directory callback
  semantics are verified against Git itself by the main module's
  differential tests. Previously the walker pruned canonical slash-form
  exclusions as well and missed re-includable files under `x/*`-style
  patterns (2 walk entries on moby); the historical transcripts in
  `results/` predate the correction. See
  [the specification](../docs/spec/README.md#filesystem-policy-and-traversal).

## Results summary

Medians from `results/*.stats.txt`; ± is benchstat's reported confidence
range across 6 samples, not median absolute deviation.

### Tier 1 — shared in-memory functionality

Matching, ns/query (hit+miss mix):

| rules | go-ignore | git-pkgs | sabhiram |
| --- | --- | --- | --- |
| mixed-10 | 180 ±3% | 149 ±10% | 4.44µ ±4% |
| mixed-100 | 685 ±10% | 1.048µ ±5% | 43.1µ ±8% |
| mixed-1000 | 6.43µ ±3% | 9.86µ ±3% | 470µ ±20% |
| mixed-10000 | 128µ ±3% | 143µ ±2% | 5.39m ±4% |
| lit-100 | 139 ±7% | 614 ±3% | 46.0µ ±2% |
| wild-100 | 835 ±8% | 1.015µ ±4% | 77.0µ ±2% |
| anchored-100 | 608 ±3% | 957 ±8% | 10.5µ ±5% |
| dir-100 | 489 ±4% | 817 ±4% | 57.9µ ±3% |
| wild-complex (500) | 6.21µ ±7% | 5.92µ ±7% | 336µ ±4% |
| moby-root (27) | 482 ±9% | 453 ±11% | 22.8µ ±4% |

Compilation:

| rules | go-ignore | git-pkgs | sabhiram |
| --- | --- | --- | --- |
| mixed-100 | 50.7µ ±14% | 45.9µ ±19% | 4.42m ±9% |
| mixed-1000 | 645µ ±14% | 550µ ±7% | 26.5m ±6% |
| mixed-10000 | 2.39m ±8% | 3.56m ±6% | 169m ±11% |
| wild-complex | 219µ ±11% | 162µ ±9% | 14.1m ±6% |

Reading: on shared functionality go-ignore and git-pkgs are close
competitors. go-ignore is 1.3–4.4× faster on matching for every category
except `wild-complex` and `moby-root`, where they tie (within overlapping
error bars); git-pkgs compiles slightly faster except at 10k rules and on
literal-heavy sets; sabhiram's regex engine is 1–2 orders of magnitude
slower at matching and dominates compilation cost. go-ignore's core
`Matcher.Match` allocates a small scratch row slice on some
wildcard-evaluating queries (≤7 B/query on hits), where both competitors
are allocation-free on the same sets; the `Policy` path is allocation-free.

### Tier 2 — filesystem integration

| operation | go-ignore | git-pkgs | sabhiram (root file only) |
| --- | --- | --- | --- |
| cold + 1 deep query, moby | 379µ ±4% | 497m ±2% | 616µ ±7% |
| cold + full sweep (1600 q), moby | 22.9m ±6% | 505m ±5% | 36.1m ±2% |
| warm, moby | 942n ±6% | 2.70µ ±2% | 22.3µ ±3% |
| warm, nested-d8 | 880n ±2% | 903n ±13% | 5.83µ ±1% |
| full walk, moby (21.7k entries) | 377m ±6% | 488m ±3% | unsupported |
| full walk, nested-d8 | 1.78m ±3% | 48.3m ±2% | unsupported |

Throughput (from `results/tier2.stats.txt`): cold sweep 69.9k vs 3.17k
queries/s on moby; walk 63.7k vs 44.5k entries/s on moby, 134k vs 2.4k on
nested-d8.

Reading: cold differences are dominated by loading models — go-ignore's
lazy chain discovery answers one deep query 1000× faster than git-pkgs'
eager full-tree load, and serves the whole labelled sweep 22× faster
because only queried directories are ever read. Warm, go-ignore is ~2.9×
faster on moby and ties on the small synthetic tree. Walking, go-ignore
is 1.3× faster on moby and 27× on nested-d8; git-pkgs' `Walk` carries a
large fixed cost per call (its allocation signature on nested-d8 — 4.8k
allocs — matches its eager `NewFromDirectory`, ~48ms even for 17 ignore
files). sabhiram's column measures its supported subset only (single root
file, no nesting): competitive to compile one file, but its per-query
regex cost still makes warm matching 24× slower.

### Tier 3 — incremental feature overhead within go-ignore

| comparison | result |
| --- | --- |
| cumulative matcher vs composed policy, moby warm | 8.12µ vs 947n — composition 8.6× faster (and 112 B/op → 0) |
| cumulative matcher vs composed policy, nested-d8 | 1.35µ vs 893n — 1.5× |
| single source vs composed (pair trees, identical decisions) | 414n vs 804n — composition costs ~2× per query at depth 8 |
| standard vs custom ranks | 906n vs 914n — parity (rank order preserved ⇒ same decisions, asserted) |
| reserved list on vs off (moby, no reserved queries) | 963n vs 887n — +8.6% prefix-scan overhead |
| chain depth 2 / 8 / 32 | 293n / 901n / 6.23µ — super-linear growth (ancestor-blocking scan) |
| decision workloads (moby): ancestor-blocked / endpoint / clean | 970n / 1.04µ / 868n — blocked decisions allocate 24 B (`Block` provenance) |

Original baseline retained memory (median `HeapAlloc` delta, order-of-magnitude):

| input | go-ignore | git-pkgs | sabhiram |
| --- | --- | --- | --- |
| mixed-100 | 42.3kB | 22.8kB | 250kB |
| mixed-1000 | 487kB | 225kB | 2.56MB |
| mixed-10000 | 5.81MB | 1.93MB | 26.3MB |
| nested-d8 tree (warm policy vs eager) | 71.0kB | 30.6kB | — |
| moby tree (warm policy vs eager) | 483kB | 253kB | — |

These are pre-investigation figures. Much of the excess was redundant
instruction backing storage, not a necessary cost of indexing or provenance.

### Retained-storage investigation

The only production change is stable instruction batching: never grow a backing
array after handing out program slices. A program fits in one batch; normal
batches hold at most 256 instructions, small sources use their remaining rule
count, and larger programs get an exact-size batch. No evaluator, public API,
policy or traversal change. Rule batching, indexes, provenance and scratch reuse
are unchanged.

Precise post-GC profiles (`-memprofilerate=1`, subtracting an empty profile)
attribute the original mixed-10k matcher to these allocation sites:

| storage | before | after | live objects before → after |
| --- | ---: | ---: | ---: |
| instruction backings | 3,367 kB | 1,052 kB | 4 → 71 |
| rule batches (including allocator rounding) | 1,926 kB | unchanged | 40 → 40 |
| literal indexes, scope groups and wildcard candidates | 290 kB | unchanged | 26 → 26 |
| source copy, rule-pointer slice and matcher | 229 kB | unchanged | 3 → 3 |

These matcher allocations total 5.81 → 3.50 MB and 73 → 140 heap objects:
smaller retained heap, not fewer objects. Runtime/profiler objects are excluded.
The mixed-10k input has 10,033 physical rules, 16,446 instructions, 182 classes,
6,056 distinct indexed keys and 3,977 wildcard candidates. Its 144,131 source
bytes are copied once; pointer and wildcard slices have capacities 10,033 and
4,096. Exact storage replay separates the original 60,062 reserved instruction
slots into 16,446 useful units, 39,287 copied-prefix units (2.20 MB) and 4,329
unused slots (242 kB). Stable batches reserve 16,468 slots: **no copied prefixes
and only 22 unused slots (1,232 bytes)**. Allocator rounding adds about 130 kB to
the new instruction batches and 241 kB to rule batches; that is distinct from
logical unused capacity. Three-index slicing alone never released old backings.

Same-cache retained-heap medians (five trials, decimal units):

| input | before | after | reduction |
| --- | ---: | ---: | ---: |
| mixed-10 | 4.91 kB | 3.84 kB | 22% |
| mixed-100 | 42.3 kB | 34.6 kB | 18% |
| mixed-1000 | 487 kB | 336 kB | 31% |
| mixed-10000 | 5.81 MB | 3.50 MB | 40% |
| literal-100 | 30.4 kB | 30.4 kB | unchanged |
| wildcard-100 | 64.2 kB | 34.6 kB | 46% |
| anchored-100 | 43.3 kB | 37.0 kB | 15% |
| directory-100 | 43.4 kB | 33.0 kB | 24% |
| complex-wildcard-500 | 263 kB | 193 kB | 26% |
| moby-root | 7.70 kB | 6.30 kB | 18% |
| nested-d8 policy | 71.0 kB | 56.9 kB | 20% |
| moby policy | 483 kB | 440 kB | 9% |

At fixed 1,000 literal rules, lengthening patterns from 8 to 1,024 bytes adds
approximately 1.02 MB retained both before and after: one source copy, not
multiple copies per program/bound/index. An eight-classes-per-rule controlled
input retains 2.58 → 0.83 MB. A literal-only 1,000-rule compile allocates about
5.4 kB more (+1.7%) and three extra objects from batch/page rounding; small
literal-only sources need no extra program batches. This is an honest batching
cost, not a memory improvement on every distribution.

The index-vs-exhaustive controlled benchmark retains the same matcher and feeds
identical first-hit/last-hit/miss paths: 100 literal rules take 45 ns indexed
versus 839 ns scanning; 1,000 take 47 ns versus 7.80 µs. All allocate zero.
These are internal algorithm trade-offs, not refreshed competitor ratios.

Final latency samples alternate baseline/candidate order (six 1-second samples,
one logical CPU, affinity mask `0x40`; `results/storage.stats.txt`). Compilation
improves 23% on mixed-1k, 19% on mixed-10k and 26% on complex wildcards. Mixed-10k
compile bytes fall **5.85 → 3.535 MB (−40%)**, while allocations increase
**89 → 156**. Small literal compilation is statistically unchanged. All shared
match workloads and cold/warm policy/traversal latency comparisons show no
statistically significant differences in this run; several confidence ranges are
wide, so this is not proof of exact parity. Direct class matching is also broadly
unchanged, except an 8-class miss **+2.2% (+2.7 ns)**. Moby warm policy is
883 → 889 ns and walk 386 → 379 ms, both statistically unchanged. Match allocation
counts are unchanged: warm clean/endpoint queries retain zero allocations;
ancestor `Block` provenance still costs 24 bytes. Moby cold-sweep allocation bytes
fall 4%, with allocations +0.5%. The more dramatic regressions in discarded
variants are not hidden in the accepted results.

**Root cause.** Append growth copied earlier instructions into new stores while
existing rules still pointed into old stores. All generations remained live,
including duplicated instructions and capacity. Batching had reduced allocation
counts while unintentionally increasing retained bytes. Stable batches remove
that redundancy during compilation, without a consolidation copy afterwards.

**Remaining storage and scaling.** Every physical rule keeps a 168-byte record
on this host (including required provenance, precedence and hot matching bounds),
plus an 8-byte pointer. The 256-rule batching introduces allocator-page rounding,
not spare rule slots. Programs scale with compiled units rather than growth
generations. Batch-boundary waste depends on program lengths: repeated programs
just over 128 units can leave nearly half of each 256-slot batch unused, whereas
programs larger than 256 units get exact-size batches. Instructions remain
56 bytes, including a 32-byte inline class set
even for non-class instructions: a measured speed/storage trade-off, not a claim
that every field contains useful information. Escape-free instruction text,
pattern, raw line and head/tail bounds
share the single owned source-string copy. Pattern length increases source bytes;
escaped runs additionally allocate decoded strings. Comment bytes in a source
with live rules also remain in that source copy. Index cost scales with distinct
literal keys and scopes; wildcard candidates retain compact rule indices.
Policy composition retains each discovered directory's own matcher once, plus
discovery/snapshot/diagnostic bookkeeping, rather than duplicating its ancestors.

**Measured alternatives rejected.** Exact final compaction retained 3.37 MB but
increased mixed-10k compile bytes 5.85 → 6.77 MB and large mixed compile latency
14–18%. A 24-byte instruction with a class side table cut retention further
(2.86 MB combined), but direct 8/64-class hit/miss matching regressed 14–25%
even after moving the lookup out of the byte loop. The initial in-loop variant
was 35–82% slower. Inline sets and the evaluator were restored byte-for-byte;
the direct class benchmark is kept to prevent that regression being hidden by
literal rejection. Stable batches accept more objects and modest page rounding
instead of either cost. Exact instruction counting would duplicate parsing or
require a second compile pass. Sorted-slice or selective literal indexes could
save some map storage but change the lookup hot path; the measured maps were not
the dominant cost, so they remain. Packing rank into int32 is invalid for the
unbounded public int rank; offset-encoding hot head/tail or program headers,
interning strings, and replacing rule batches were not implemented because their
smaller possible savings did not justify hot-path/ownership churn. Phase-local
allocation-region designs were not measured by these profiles.

Profiles can be reproduced using `TestRetainedHeapProfile` in `profile_test.go`:
set `IGNORE_HEAP_DIR` to an existing output directory and
`IGNORE_HEAP_RULESET=mixed-10000`, then run
`mise exec -- go test . -run '^TestRetainedHeapProfile$' -memprofilerate=1 -v`
from `bench/`. Subtract `empty.prof` from `live.prof` with
`go tool pprof -base empty.prof live.prof`, selecting `inuse_space` and
`inuse_objects`. Run one workload per process. Other manifest rule sets and the
controlled 1,000-rule inputs `literal-short` (8-byte patterns), `literal-long`
(1,024-byte patterns) and `class-heavy` (eight sets per rule) are supported.
The root-package `TestStorageLayout` reports exact structure counts when
`IGNORE_STORAGE_RULES` names the same rule text. Heap profiling is separate from
latency benchmarks; `BenchmarkLiteralIndexTradeoff` measures the same matcher
with index lookup and exhaustive scanning, including identical hit/miss paths.

## Full post-storage comparison

All three tiers were rerun on the final stable-batch implementation against both
pinned competitors, using the same cached manifest/tree (hash in the environment
record), six 1-second samples, `test.cpu=1` and affinity `0x40`. These are current
cross-library medians, not a comparison against the older 16-thread timings.
`results/comparison-storage-tier{1,2,3}.txt` retain all samples; corresponding
`.stats.txt` files include confidence intervals, significance, bytes, allocations,
hit/miss splits and throughput. Raw retained-memory and correctness results are
`comparison-storage-memory.txt` and `comparison-storage-correctness.txt`.

### Shared matching and compilation

Matching, **µs/query** (lower is better):

| input | go-ignore | git-pkgs | sabhiram |
| --- | ---: | ---: | ---: |
| mixed-10 | 0.160 | 0.141 | 4.24 |
| mixed-100 | 0.672 | 0.979 | 40.3 |
| mixed-1000 | 5.86 | 9.01 | 429 |
| mixed-10000 | 122 | 134 | 5,054 |
| literal-100 | 0.127 | 0.567 | 43.7 |
| wildcard-100 | 0.754 | 0.924 | 71.3 |
| anchored-100 | 0.543 | 0.851 | 10.1 |
| directory-100 | 0.449 | 0.738 | 55.5* |
| complex-wildcard-500 | 5.85 | 5.44 | 309 |
| moby-root | 0.456 | 0.408 | 21.3 |
| mixed-100 hit subset | 0.554 | 0.778 | 41.3 |
| mixed-100 miss subset | 0.658 | 1.114 | 39.2 |

Compilation, **µs/compile**:

| input | go-ignore | git-pkgs | sabhiram |
| --- | ---: | ---: | ---: |
| mixed-10 | 2.10 | 2.91 | 247 |
| mixed-100 | 15.9 | 20.0 | 2,078 |
| mixed-1000 | 228 | 229 | 21,017 |
| mixed-10000 | 2,490 | 3,040 | 250,086 |
| literal-100 | 16.5 | 18.9 | 1,913 |
| wildcard-100 | 15.3 | 17.6 | 2,013 |
| anchored-100 | 22.2 | 25.8 | 2,129 |
| directory-100 | 15.0 | 24.5 | 2,045 |
| complex-wildcard-500 | 123 | 126 | 10,766 |
| moby-root | 4.39 | 4.07 | 361 |

Compilation allocation cost, **KiB / allocation count** (MiB explicitly marked):

| input | go-ignore | git-pkgs | sabhiram |
| --- | ---: | ---: | ---: |
| mixed-10 | 4.13 / 15 | 4.27 / 40 | 231 / 2,610 |
| mixed-100 | 34.4 / 29 | 36.5 / 313 | 2,136 / 23,945 |
| mixed-1000 | 330 / 40 | 418 / 3,059 | 21,345 / 239,182 |
| mixed-10000 | 3.37 MiB / 156 | 4.62 MiB / 30,519 | 211.85 MiB / 2,403,361 |
| literal-100 | 29.7 / 12 | 34.8 / 313 | 2,058 / 22,783 |
| wildcard-100 | 34.7 / 27 | 34.5 / 319 | 2,120 / 23,379 |
| anchored-100 | 36.8 / 26 | 38.4 / 313 | 2,183 / 25,686 |
| directory-100 | 32.6 / 26 | 36.8 / 313 | 2,095 / 23,968 |
| complex-wildcard-500 | 191 / 35 | 163 / 1,549 | 11,233 / 122,047 |
| moby-root | 6.56 / 20 | 8.32 / 67 | 386 / 4,234 |

Go-ignore wins most matching rows, including 4.46× faster literal-100 and 1.54×
mixed-1k versus git-pkgs, but **git-pkgs wins mixed-10, complex-wildcard and
moby-root matching** (statistically significant). Mixed-1k, complex-wildcard,
anchored and moby-root compilation are statistical ties. Go-ignore reports
3–15 B/query on several mixed/wildcard rows because of blocking/scratch, while
git-pkgs reports zero; average `allocs/op` rounded to zero is not a guarantee that
every query allocates zero. Sabhiram is substantially slower, with the correctness
limitations below; its directory timing (*) is path-only, not equivalent isDir
semantics.

### Retained heap

Median of five GC-delta trials, **decimal kB** (MB explicitly marked):

| input | go-ignore | git-pkgs | sabhiram |
| --- | ---: | ---: | ---: |
| mixed-10 | 3.95 | 2.76 | 30.0 |
| mixed-100 | 34.6 | 22.8 | 250 |
| mixed-1000 | 336 | 225 | 2.56 MB |
| mixed-10000 | 3.50 MB | 1.93 MB | 26.34 MB |
| literal-100 | 30.4 | 21.8 | 205 |
| wildcard-100 | 34.6 | 21.4 | 209 |
| anchored-100 | 37.0 | 24.0 | 356 |
| directory-100 | 33.1 | 22.8 | 209 |
| complex-wildcard-500 | 193 | 104 | 1,239 |
| moby-root | 6.30 | 4.99 | 46.2 |
| nested-d8 policy/eager tree | 56.8 | 30.3 | unsupported |
| moby policy/eager tree | 440 | 254 | unsupported |

Memory remains a weakness versus git-pkgs: mixed-10k is **1.81×** its retained
heap, down from 3.01× before the change. Go-ignore uses fewer total compile bytes
on that workload despite retaining more metadata/index/program state.

### Filesystem integration

Cold, sweep and walk values are **ms**; warm values are **µs/query**:

| operation / input | go-ignore | git-pkgs | sabhiram (root file only) |
| --- | ---: | ---: | ---: |
| cold + one query / nested-d8 | 0.661 | 48.0 | 0.229* |
| cold + one query / moby | 0.345 | 475 | 0.494* |
| cold + full sweep / nested-d8 | 2.32 | 49.0 | 1.47* |
| cold + full sweep / moby | 20.3 | 469 | 35.7* |
| warm / nested-d8 (µs) | 0.825 | 0.880 | 5.63* |
| warm / moby (µs) | 0.875 | 2.502 | 21.0* |
| walk / nested-d8 | 1.64 | 46.9 | unsupported |
| walk / moby | 358 | 462 | unsupported |

Go-ignore's moby warm policy is 2.86× faster and traversal 1.29× faster than
git-pkgs. Cold shapes differ (lazy chain discovery versus eager whole-tree load),
so the full-sweep comparison is also reported. Callback/reporting differences
qualify walk ratios. Sabhiram's (*) root-only rows omit nested rules entirely and
must not be interpreted as equivalent filesystem comparisons. Warm moby queries
allocate zero in all three drivers; nested go-ignore averages 3 B/query from
`Block` provenance. Moby cold-sweep allocates 1,044 / 15,634 / 387 KiB for the
three drivers, but sabhiram's smaller supported subset explains its lower cost.

### Go-ignore-only feature costs (Tier 3)

| comparison | current median |
| --- | --- |
| cumulative matcher / composed policy, nested-d8 | 1.309 / 0.830 µs |
| cumulative matcher / composed policy, moby | 7.635 / 0.871 µs; 112 / 0 B/query |
| equivalent single / composed pair | 0.387 / 0.748 µs |
| standard / custom ranks | 0.824 / 0.830 µs |
| reserved on / off | 0.862 / 0.815 µs |
| depth 2 / 8 / 32 | 0.268 / 0.833 / 5.780 µs |
| ancestor-blocked / endpoint / clean | 0.884 / 0.963 / 0.788 µs; blocked = 24 B/query |

These capabilities are not synthesized for competitors that lack them.
Correctness reruns show zero go-ignore/git-pkgs query mismatches; sabhiram still
has six wildcard mismatches and directory API limits. Pair/rank/reserved gates
pass. The same two verified moby walker differences remain, not new regressions.

## Findings and opportunities

Strengths (measured, not claimed): warm matching speed on realistic rule
sets; lazy discovery and traversal throughput; zero-allocation warm
`Policy` matching including provenance; 8.6× warm advantage of the
composed architecture over the cumulative shape it replaced.

Weaknesses / honest costs: required rule metadata and indexing still retain
more memory than a minimal verdict-only representation;
warm matching ties (not wins) on wildcard-complex and tiny-realistic rule
sets; composition costs 2× over a single source at depth 8 and grows
super-linearly with chain depth; reserved-path scanning costs ~9% even
when nothing is reserved; core `Matcher.Match` (not `Policy`) allocates
scratch rows on wildcard queries; blocked decisions allocate a 24 B
`Block`.

Opportunities worth investigating later (explicitly out of scope here):

1. **Further retained-storage work** — the instruction duplication above is
   addressed without arenas. Remaining rule metadata, map lookup and snapshot
   state have useful semantics/performance roles; any further representation
   change needs its own measurements; aggregate memory figures alone do not
   establish which representation should replace it.
2. **Walker semantics for slash-form-excluded directories** — the
   verified `bin/*` corner above: pruning follows `check-ignore <dir>/`,
   diverging from `git status` traversal when re-inclusions exist inside.
   A production change would need its own spec verification.
3. Small per-call allocations: `Block` provenance (24 B) on blocked
   decisions and core-matcher scratch rows on wildcard queries.

## Limitations

- One machine, one OS (Windows 11), one Go toolchain; absolute numbers
  are host-specific, ratios are the meaningful output.
- The moby tree materializes ~3.6k untracked artefact files to give
  ignored queries real targets; both libraries traverse the identical
  tree, but it is not a pristine checkout.
- `sabhiram`'s pinned version (2021) is its latest; its column measures
  the library as published.
- Retained-memory figures are GC-timing approximations.
- The oracle is `git check-ignore` (plus `git status` for the walker
  corner); Git's own interfaces disagree on the slash-form directory
  case, which the harness verifies rather than assumes.
