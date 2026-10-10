# Library benchmarks and resource measurements

These raw records cover the library's compilation, matching, policy composition
and resource workloads. They supplement the current
[comparative methodology and results](../../bench/README.md), which retain dataset
manifests, pinned dependencies, hardware, repeated samples and statistical summaries.
Earlier measurements are within-library baselines, not current competitor rankings.

## Records

| Files | Workload |
| --- | --- |
| `core-windows.txt`, `core-linux.txt` | Core compilation and matching across rule counts and path depths |
| `index-before.txt`, `index-after.txt` | Same-workload exhaustive versus indexed matching measurements |
| `builder-windows.txt` | Incremental `Builder` construction, matching and retention against one-shot `Compile` |
| `policy.txt` | Cold/warm discovery and walking |
| `core-policy.txt` | Core and policy workloads, including deep chains |
| `composition-before.txt`, `composition-after.txt`, `composition-warm.txt` | Per-directory composition and warm deep-chain measurements |
| `resources-windows.txt`, `scaling-windows.txt` | Retained-memory, source-cap and adversarial scaling results |
| `rss-test-output.txt`, `rss-test-stderr.txt`, `sampled-rss.json`, `peak-rss-linux.txt` | Process-memory measurements, separate from retained Go heap |
| `windows-environment.json`, `linux-environment.txt`, `linux-go-environment.txt`, `linux-git-build.txt`, `wsl-version.txt` | Recorded execution environments |

Records date from 2026-10-09, using Go 1.27.1 on Windows amd64 and Linux amd64
under WSL2. The Windows host is an AMD Ryzen 7 5800X3D; current comparative runs
record its settings in `bench/results/environment.txt`. Raw benchmark headers
identify platform, CPU and benchmark-specific parameters. These are finite,
host-specific measurements, not performance guarantees.

## Reproduction

From the repository root, resolve tools through mise:

```sh
mise exec -- go test . -run '^$' -bench '^Benchmark' -benchmem -benchtime=50ms -count=3
mise exec -- go test . -run '^$' -bench '^BenchmarkPolicy' -benchmem -benchtime=100ms -count=3
```

The first command reproduces the core/index measurement shape. Compare unchanged
workloads and settings, not different benchtimes or setup lifetimes. Cold policy
benchmarks include construction and discovery; warm ones reuse loaded state.
Benchmarks print allocation counts and bytes independently of latency. Current
storage layouts differ from earlier baselines; the comparative suite documents
the stable-instruction-batch implementation and its measured trade-offs.

With `IGNORE_RESOURCE=1`, run:

```sh
mise exec -- go test . -run '^Test(KillableScaling|RetainedLiteralMemory|RetainedRuleSets|DefaultCapBoundaries)$' -count=1 -v -timeout=180s
```

On Windows, build a test executable with `mise exec -- go test -c -o <output-dir>/ignore.test.exe`
and run `sample-rss.ps1 -Executable <absolute-executable-path>` to sample its
working set. On Linux, run the same executable under `/usr/bin/time -v` with
`IGNORE_RESOURCE=1` and the retained-memory/source-cap test selection. Process RSS
includes runtime and transient memory; it is not post-GC retained heap. Profiling
commands, GC-delta trials and controlled storage workloads are documented in
[the comparative suite](../../bench/README.md#retained-storage-investigation).
