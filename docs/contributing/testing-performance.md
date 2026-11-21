# Test performance measurements

Run commands from the repository root using the [build and test](development.md#build-and-test) runner.
Select these recipes for test CPU/block profiles.

For test CPU/block profiles, select one package and set `TEST_SUPPORT_PACKAGES=` so
the runner does not add another package. Pass temporary `-cpuprofile`, `-blockprofile`
and `-o` paths through `TEST_FLAGS`:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_SUPPORT_PACKAGES= TEST_RUN='^TestAppServer' TEST_FLAGS='-cpuprofile=/tmp/mekugi-launch.cpu -blockprofile=/tmp/mekugi-launch.block -o=/tmp/mekugi-launch.test'
```

Inspect profiles with `go tool pprof`; summed blocked time is not wall time.
