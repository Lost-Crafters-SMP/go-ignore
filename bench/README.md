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
go run ./cmd/benchsetup                                   # downloads moby once (~40 MB), builds datasets
go test . -run 'TestCorrectness|TestPairEquivalence|TestRankAndReservedEquivalence|TestWalkerEquivalence' -count=1 -v | tee results/correctness.txt
go test . -run '^$' -bench 'BenchmarkTier1' -benchtime=1s -count=6 | tee results/tier1.txt
go test . -run '^$' -bench 'BenchmarkTier2' -benchtime=1s -count=6 | tee results/tier2.txt
go test . -run '^$' -bench 'BenchmarkTier3' -benchtime=1s -count=6 | tee results/tier3.txt
go test . -run 'TestRetainedMemory' -count=1 -v | tee results/memory.txt
go tool benchstat results/tier1.txt                       # etc. -> *.stats.txt
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

Pins live in `bench/go.mod` / `bench/go.sum`. Adapters call only public
APIs; no competitor source was read or adapted into go-ignore.

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
- Statistics: 6 samples per benchmark, `benchstat` medians; differences
  within overlapping error bars are treated as noise.

## Correctness verification

`go-ignore` matched the Git oracle on **every** query of every dataset
(0 mismatches across ~6,600 labelled queries), including directory
endpoints. `git-pkgs` also matched everywhere. Divergences found:

- `sabhiram` misses some `?.ext` single-character wildcard patterns
  (`?.swp` vs `q.swp`: Git ignores, sabhiram does not — 3/268 on
  `mixed-100`, 3/266 on `wild-100`), and cannot express directory
  endpoints (25 dir queries on `dir-100` are API-limited, counted
  separately, not timed as equivalents).
- **Walker corner (Git itself is torn):** directories excluded only
  through a non-dirOnly slash pattern (`bin/*`) are pruned by go-ignore's
  walker — which follows `check-ignore`'s trailing-slash directory verdict
  (`bin/` → ignored) — while `git-pkgs` descends the way `git status`
  traversal does, re-including content such as `!bin/keep` inside. Git's
  own interfaces disagree here: `git check-ignore bin` says not-ignored,
  `git check-ignore bin/` says ignored, `git status --ignored -uall`
  descends and lists `?? bin/keep`. On the real moby tree this produces
  exactly 2 differing walk entries (`vendor/**/hcsshim/bin`,
  `vendor/**/distribution/bin`), both verified to be this corner by the
  oracle (`TestWalkerEquivalence`); no unexplained walk divergences exist.
  Consequence: a consumer that must reproduce `git status` output exactly
  can miss re-includable files under `x/*`-style patterns with go-ignore's
  current walker. Not fixed here (production change out of scope); listed
  under opportunities.

## Results summary

Medians from `results/*.stats.txt`; ± is benchstat's median absolute
deviation across 6 samples.

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

Retained memory (median `HeapAlloc` delta, order-of-magnitude):

| input | go-ignore | git-pkgs | sabhiram |
| --- | --- | --- | --- |
| mixed-100 | 42.3kB | 22.8kB | 250kB |
| mixed-1000 | 487kB | 225kB | 2.56MB |
| mixed-10000 | 5.81MB | 1.93MB | 26.3MB |
| nested-d8 tree (warm policy vs eager) | 71.0kB | 30.6kB | — |
| moby tree (warm policy vs eager) | 483kB | 253kB | — |

go-ignore retains ~2.6× git-pkgs per rule (programs, head/tail bounds and
literal-index maps buy the matching speed above) and ~1/22 of sabhiram.

## Findings and opportunities

Strengths (measured, not claimed): warm matching speed on realistic rule
sets; lazy discovery and traversal throughput; zero-allocation warm
`Policy` matching including provenance; 8.6× warm advantage of the
composed architecture over the cumulative shape it replaced.

Weaknesses / honest costs: retained memory ~2.6× the leanest competitor;
warm matching ties (not wins) on wildcard-complex and tiny-realistic rule
sets; composition costs 2× over a single source at depth 8 and grows
super-linearly with chain depth; reserved-path scanning costs ~9% even
when nothing is reserved; core `Matcher.Match` (not `Policy`) allocates
scratch rows on wildcard queries; blocked decisions allocate a 24 B
`Block`.

Opportunities worth investigating later (explicitly out of scope here):

1. **Arena allocation for compiled rule storage** — ~580 B retained per
   rule (mixed-10000: 5.8 MB) spread across rule structs, program
   instruction slices, index maps and provenance strings. A dedicated
   arena per compiled matcher could cut retention and construction
   allocations substantially; this benchmark suite now provides the
   before/after baseline (retained memory, compile µs, B/op) to justify
   or reject it.
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
