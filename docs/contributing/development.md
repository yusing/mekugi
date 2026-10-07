# Development

Build, test, diagnose, and profile Mekugi. Run checkout commands from the repository root.
See [Contributing](../../CONTRIBUTING.md) for the contributor entry point.

## Build and test

From a checkout with [source prerequisites](../../README.md#build-from-source):

```sh
make preview-assets
make test
make lint
```

The suite needs Node.js 24+/ripgrep; lint needs `golangci-lint`/`deadcode` and installs neither.
Focus tests with `TEST_PACKAGES`/`TEST_RUN`/`TEST_FLAGS`; `-count=1` bypasses caching.
Built-ins are Go; Bun is for separate plugin tests. `make preview-native-ui` uses no models.
`make test-ui-snapshots` checks fixtures; review before updating. See [test procedures](../../CONTEXT-TESTS.md).
The [release workflow](../../.github/workflows/release.yml) packages Linux/macOS; tags publish assets.

## Diagnostics

`--debug` records future requests under `$XDG_STATE_HOME/mekugi/debug` (default
`~/.local/state/mekugi/debug`). **Bundles contain private content and are not
sanitized.** Inactive bundles expire after 14 days; copy elsewhere to keep one. Exit
prints a diagnosis command. Codex tracing uses uncapped private temporary storage;
shutdown removes it, forced kills may leave it. See [debug
evidence](../../doc/spec/router.md#feature-usage-debug-evidence).

### Inspect a session

Inspection runs no recorded operations. Default reports omit private text; selected
`--field` values may include it. `--request-id ID --field all` drills into one request.

```sh
mekugi inspect-session --session /path/to/rollout.jsonl
mekugi inspect-session --debug-dir /path/to/debug --field diagnostic
```

See [session inspection](../../doc/spec/session.md) and [AX evidence](../../doc/spec/ax.md).

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
[replay](../../doc/spec/session_replay.md).

## Profile live sessions and replay

`make mekugi-pprof` builds the profiling binary and executor without installing them.
Launch or replay with `bin/mekugi-pprof`; stderr prints a loopback URL for `go tool pprof`.
Local callers can read private data; keep captures private. Instrumentation affects timing;
profiles cover Mekugi, not separate Codex/executor processes. See [profiling](../../doc/spec/router.md#performance-profiling).
