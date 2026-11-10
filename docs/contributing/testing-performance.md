# Test performance measurements

Run commands from the repository root using the [common test runner](../../CONTEXT-TESTS.md#runner).
Select these recipes for frontend/symbol benchmarks or test CPU/block profiles.

Native-reader latency covers a fresh authenticated worker, 3,000-row read, three-document
architecture batch and eight-file Go inspection at the default shared budget:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^$' TEST_FLAGS='-bench=BenchmarkNativeFrontends -benchtime=3x -count=1'
```

The inspection batch exercises incomplete-output recovery. These benchmarks clear thread identity
and exclude active-session lease/retention costs. `BenchmarkSelectRows` in
`libraries/outputbudget/tokenizer` isolates row admission from startup/source I/O.

Go symbol resolver/frontend benchmarks use normal caches and exclude frontend registry
creation/active-session leases:

```sh
make test TEST_PACKAGES='./modules/symbolquery/query ./apps/mekugi/internal/launch' TEST_RUN='^$' TEST_FLAGS='-bench=BenchmarkNativeSymbol -benchtime=5x -count=1'
```

For test CPU/block profiles, select one package and set `TEST_SUPPORT_PACKAGES=` so
the runner does not add another package. Pass temporary `-cpuprofile`, `-blockprofile`
and `-o` paths through `TEST_FLAGS`:

```sh
make test TEST_PACKAGES=./modules/symbolquery/query TEST_SUPPORT_PACKAGES= TEST_RUN='^TestNativeSymbol' TEST_FLAGS='-cpuprofile=/tmp/mekugi-symbol.cpu -blockprofile=/tmp/mekugi-symbol.block -o=/tmp/mekugi-symbol.test'
```

Inspect profiles with `go tool pprof`; summed blocked time is not wall time.
