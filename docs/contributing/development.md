# Development

Build, test, diagnose, and profile Mekugi. Run checkout commands from the repository root.
See [Contributing](../../CONTRIBUTING.md) for the contributor entry point.

## Build and test

From a checkout with [source prerequisites](../../README.md#build-from-source):

```sh
make build
make test
make lint
```

The build, test, lint, generation, and snapshot recipes enumerate the explicit
`go.work` members. The repository root has no Go module; use these recipes or
explicit member package paths for root-level Go commands. `make build-binaries`
writes both executables to `bin/` without installing them. Run member tests from
that member with the workspace enabled. Local modules are released together and
resolved by `go.work`, not unpublished version requirements or local replacements.
See [workspace ownership](../contracts/composition.md).

Interpreter and query fixtures use Node.js/ripgrep; lint needs `golangci-lint`/`deadcode` and installs neither.
Focus tests with `TEST_PACKAGES`/`TEST_RUN`/`TEST_FLAGS`; `-count=1` bypasses caching.
Make builds use `-trimpath` so unchanged packages reuse the Go build cache across
worktrees and staged snapshot checks. Set `GO_BUILD_FLAGS=` when absolute source
paths are needed.
Use `make test-host` for the focused installed-Codex checks with local mock providers;
see [host acceptance](../../CONTEXT-AUTOMATED-TESTS.md).
Built-in frontends are Go. `make preview-native-ui` uses no models.
`make test-ui-snapshots` checks fixtures; review before updating. See [test procedures](../../CONTEXT-TESTS.md).
Enable the repository's commit gate once per clone:

```sh
git config --local core.hooksPath "$(git rev-parse --show-toplevel)/.githooks"
```

The gate checks every UI snapshot against the staged source in a temporary checkout.
The absolute hook path also applies the gate to this clone's other worktrees.
Unstaged fixes cannot hide stale staged fixtures. A mismatch blocks the commit and
prints its diff. Review and update the affected baselines, then stage them with the
source change. The existing Linux CI suite also checks all UI snapshots.
The [release workflow](../../.github/workflows/release.yml) packages Linux/macOS; tags publish assets.

## Diagnostics

`--debug` records future requests under `$XDG_STATE_HOME/mekugi/debug` (default
`~/.local/state/mekugi/debug`). **Bundles contain private content and are not
sanitized.** Inactive bundles expire after 14 days; copy elsewhere to keep one. Exit
prints a diagnosis command. Codex tracing uses uncapped private temporary storage;
shutdown removes it, forced kills may leave it. See [debug
evidence](../../modules/diagnostics/SPEC.md#private-debug-bundles).

### Inspect a session

Inspection runs no recorded operations. Rollout and debug-bundle reports omit private
text unless `--field` selects it. `--request-id ID --field all` drills into one request.
Failure lookup with `--failures [REF]` always includes complete private error text,
which may contain request content or credentials.

```sh
mekugi inspect-session --session /path/to/rollout.jsonl
mekugi inspect-session --debug-dir /path/to/debug --field diagnostic
```

See [session inspection](../../modules/sessioninspection/SPEC.md) and [AX evidence](../../modules/agentmetrics/SPEC.md).

### Replay a session

```sh
mekugi replay-session --session SESSION_ID
```

Use the full ID from `CODEX_HOME` sessions/archives. Replay makes no provider, command,
or approval calls; it simulates retained text/timing, not original inputs or live
previews. Missing evidence is reported; private content may appear on screen. `p`
pauses, `+`/`-` changes speed, `[`/`]` seeks, `r` restarts, `q` quits. Use `--from 5m
--until 6m` for intervals or `--headless --width 160 --height 48 --seed 1 --speed 1` for
repeatable timing; headless excludes terminal backpressure. See
[replay](../../modules/sessionreplay/SPEC.md).

## Profile live sessions and replay

`make mekugi-pprof` builds a separate optimized executable with symbols and the
`pprof` build tag, plus its sibling executor, without installing or replacing the
normal command. Launch arguments, worker dispatch and hooks retain their behavior.
The normal build starts no profiling listener.

Launch or [replay](../../modules/sessionreplay/SPEC.md) with `bin/mekugi-pprof`.
Each invocation binds its own OS-assigned IPv4 loopback port and prints
`mekugi-pprof: http://127.0.0.1:PORT/debug/pprof/` to stderr before UI handoff.
Use that URL with `go tool pprof`. Workers and hooks start no listener; completion
cancels active captures and closes only the invocation's listener.

Standard routes expose CPU, heap, allocs, goroutine, block, mutex and execution
trace data. CPU and trace collection start only on request. Block sampling uses
a 1 ms blocked-time rate; mutex sampling records one in ten contention events.
Instrumentation may perturb performance. Profiles cover the Mekugi process,
including UI, routing and replay, not separate Codex, executor or worker processes.

The routes have no additional authentication: local callers can read command-line
arguments and other private process data. Keep captures private. Profiling is
independent of sanitized metrics and debug evidence.
