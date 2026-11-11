# Development

Build, test, diagnose, and profile Mekugi. Run checkout commands from the repository root.
See [Contributing](../../CONTRIBUTING.md) for the contributor entry point.

## Build and test

From a checkout with [source prerequisites](../../README.md#build-from-source):

```sh
make build
make test
make lint
make docs-check
```

Recipes suppress command echoes and compact tool output when RTK is enabled.
Go tests summarize successful packages and stream failure diagnostics; lint keeps
native diagnostics and exit status. Set `RTK=` for native Go package outcomes.

`make webui` builds the pinned Svelte application for embedding; `make build-binaries`
and release recipes include it. Plain `make build` checks the Go workspace.
Browser build and check recipes use **Bun 1.4.4+** on `PATH` to install pinned
dependencies from `bun.lock` and run package scripts. Node.js remains required
for the Vite build and browser binding tests.
`make webui-check` runs browser binding tests, `bun check` for TypeScript,
`svelte-check` for Svelte script/template and CSS diagnostics, lint and formatting
checks. Focused tests use `node --test` with
the selected test file under
`apps/mekugi/internal/webui/app/src/lib/`.
`make webui-fmt` formats the browser sources with `bun fmt`, using the existing
`.oxfmtrc.json` configuration. The assets host returns 503 until assets are built.

`make claude-worker` bundles the pinned Claude Agent SDK worker with Bun for
embedding. Release and installation builds include it; plain Go builds keep Claude
optional. `make claude-worker-check` rebuilds it, checks types, and runs the SDK
transport fixtures. With Bun and Claude Code installed,
`MEKUGI_TEST_CLAUDE_NATIVE=1 make claude-worker-check` additionally exercises the
native CLI against a local mock provider. These checks make no paid model requests;
authenticated provider acceptance remains separate.

After building assets, the opt-in browser fixtures exercise the actual UI,
connector and server with controlled execution hosts:

```sh
MEKUGI_TEST_WEBUI_BROWSER=1 make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestWebUI(BrowserMachines|SemanticBrowser)$'
```

They require Chrome and `agent-browser`; `MEKUGI_TEST_AGENT_BROWSER` may select an
absolute executable. They cover machine isolation, reconnection, semantic controls,
attached-input recovery, native continuation and replay. Live-provider behavior and
deployed TLS require separate checks.

The build, test, lint, generation, and snapshot recipes enumerate the explicit
`go.work` members. The repository root has no Go module; use these recipes or
explicit member package paths for root-level Go commands. `make build-binaries`
writes both executables to `bin/` without installing them. Run member tests from
that member with the workspace enabled. Local modules are released together and
resolved by `go.work`, not unpublished version requirements or local replacements.
See [workspace ownership](../contracts/composition.md).

The [module reference](../reference/modules.md) is generated from workspace
descriptors, binding declarations and composition. `make generate` refreshes it
alongside the other generated assets. `make check-generated` checks the reference
without rewriting it; CI runs the same check.

Interpreter and query fixtures use Node.js/ripgrep; lint needs `golangci-lint`/`deadcode` and installs neither.
Focus tests with `TEST_PACKAGES`/`TEST_RUN`/`TEST_FLAGS`; `-count=1` bypasses caching.
`make test-saved-diff` checks retained-Diff presentation, rendering and shared diff
primitives; `make test-live-preview` checks provisional projection, streaming and
rendering. Both accept `TEST_RUN`/`TEST_FLAGS` without compiling Launch. Launch
retains host/session integration checks, so run its affected tests when changing
those boundaries. These focused checks do not replace the snapshot commit gate.
Make builds use `-trimpath` so unchanged packages reuse the Go build cache across
worktrees and staged snapshot checks. Set `GO_BUILD_FLAGS=` when absolute source
paths are needed.
Use `make test-host` for the focused installed-Codex checks with local mock providers;
see [host acceptance](../../CONTEXT-AUTOMATED-TESTS.md).
Built-in frontends are Go. `make preview-native-ui` uses no models.
`make test-ui-snapshots` checks fixtures; review before updating. See [snapshot procedures](testing-snapshots.md).
Enable the repository's commit gate once per clone:

```sh
git config --local core.hooksPath "$(git rev-parse --show-toplevel)/.githooks"
```

When documentation changes are staged, the gate checks local link targets and Markdown
heading anchors across the staged documentation. It excludes dependency, build and
test-fixture directories. Run `make docs-check` for the same validation in the checkout.
The gate checks affected UI snapshots against the staged source in a temporary checkout.
Go source changes select snapshot packages through transitive imports, including test and
platform-tagged imports. Snapshot baseline changes select their owning package; shared
renderer changes still check downstream rendering consumers. Other inputs, including build
configuration and resources without known ownership, retain the full snapshot gate.
It skips commits made while a rebase is in progress, including amended or reworded steps, so
run `make docs-check` on the rebased tip when documentation changed, and
`make test-ui-snapshots` when UI sources changed.
The absolute hook path also applies the gate to this clone's other worktrees. To test changes
to the hook in another worktree, use `git -c core.hooksPath="$PWD/.githooks" commit` there;
this selects that worktree's hook without changing the clone's configuration.
Older staged workspace runners ignore the shared hook's optional affected-path input
and continue checking all snapshots.
Unstaged fixes cannot hide stale staged fixtures. A mismatch blocks the commit and
prints its diff. Review and update the affected baselines, then stage them with the
source change. The existing Linux CI suite also checks all UI snapshots.
The [release workflow](../../.github/workflows/release.yml) packages Linux/macOS; tags publish assets.

## Integrate reviewed work

Main can batch ready operations through the [workspace helper](../../apps/mekugi/cmd/workspace/integrate.go),
without a receipt or acceptance-state store. Supply explicit commits in pick order
and check argv after `--`; use the existing check selection appropriate to the batch:

```sh
make integrate INTEGRATE_ARGS='--commits "COMMIT1 COMMIT2" -- make test TEST_PACKAGES=./apps/mekugi/cmd/workspace'
```

Preparation does not infer acceptance or set statuses. It prints the resulting
HEAD. After Main's QA, reuse already-completed related-batch checks by explicitly
attesting their full HEAD and choosing the desired status:

```sh
make integrate INTEGRATE_ARGS='--checked-head FULL_HEAD --accept --items "MEKUGI-123 MEKUGI-124" --state "Pending QA"'
make integrate INTEGRATE_ARGS='--checked-head FULL_HEAD --accept --items "MEKUGI-123 MEKUGI-124" --state Done --cleanup .worktrees/task-name'
```

`--accept` is Main's explicit authorization of the requested status/cleanup, not
an inferred result of a passed command. Check argv can instead accompany these
steps in one call when QA and authorization are already settled. Main owns the
coverage claim: the helper neither selects tests nor verifies that a successful
command ran adequate tests. Checks run in the actual integration worktree,
including preserved unrelated dirty files. A checked-HEAD attestation is not an
immutable-clean-tree certificate. Its full hash must match current HEAD and
cannot accompany new picks. No fresh full suite is imposed for each item.

A staged index is refused before mutation; unrelated unstaged/untracked files
are not staged, stashed or discarded. Native cherry-pick refuses actual overlaps,
retains configured signing and leaves failed/partial picks for inspection.
Check or Plane failures stop the sequence before cleanup; all requested statuses
must succeed first. Earlier successful writes are not rolled back. Inspect an
uncertain Plane write before retrying.

Repeat `--cleanup PATH` for explicitly authorized source worktrees beneath this
checkout's `.worktrees`. Cleanup checks exact repository/worktree identity, source
dirt, `git cherry` patch equivalence and unmatched merges, because `git cherry`
omits merge-only changes. Ignored files also block cleanup, except the fixed
generated destinations: `apps/mekugi/internal/webui/app/node_modules/`,
`apps/mekugi/internal/webui/dist/app/`, `backends/claude/node_modules/` and
`backends/claude/dist/worker.mjs`. These dependencies and bundles are discarded
with the worktree; neighboring ignored source/configuration and other output
paths remain protected. It removes only the checked worktree, then its branch.
A cleanup failure retains whatever has not yet been removed; status writes
remain, and a failed branch removal leaves the branch for
recovery. Serialize integration and source edits; this is sequencing, not a
transaction or concurrent-mutation guarantee. Commit gates, signing, pushes and
additional browser/live-provider QA remain unchanged.

## Diagnostics

`--debug` records future requests under `$XDG_STATE_HOME/mekugi/debug` (default
`~/.local/state/mekugi/debug`). **Bundles contain private content and are not
sanitized.** Inactive bundles expire after 14 days; copy elsewhere to keep one. Exit
prints a diagnosis command. Codex tracing uses uncapped private temporary storage;
shutdown removes it, forced kills may leave it. See [debug
evidence](../../modules/diagnostics/SPEC.md#private-debug-bundles).

Automatically started local servers also retain private stderr and start/exit
records under `$XDG_STATE_HOME/mekugi/server` (default
`~/.local/state/mekugi/server`), even without `--debug` and after the client exits.
Inspect `process.jsonl` for timestamps, PID, build and exit evidence; read
`stderr.previous.log` before `stderr.log` for the retained tail. Stderr is bounded
to 1 MiB per server. These files are unsanitized and expire after 14 inactive days.
See [server evidence](../../modules/diagnostics/SPEC.md#detached-server-evidence)
for collection limits.

### Inspect a session

For historical behavior, start with recorded calls/results; current code explains intended behavior.
`--thread ID` finds the thread's rollout under `$CODEX_HOME/sessions` or `archived_sessions`
(default `~/.codex`) by its exact metadata identity; `--session PATH` reads a rollout file directly.
Child sessions and inherited fork history are not independent user sessions.

Session reports include the thread's retained `instructions`: what Mekugi forwarded to the model,
including environment facts, Mekugi guidance, developer messages and tool descriptions, as
content revisions with the time, model and turn each began. They are recorded without `--debug`;
older or expired threads report them as `unavailable`. Revisions reference `content` by digest.

Add `--ax` to inspect the thread's recent retained request phases without a debug
bundle. `ax.request_timing` distinguishes local preparation, request send, first
provider output and completion. Missing phases, including unpairable tool-result
readiness, are unknown rather than zero. First output is observed transport data,
not a first-token measurement. This bounded window does not establish lifetime
coverage or attribute every gap to Mekugi; see
[request timing](../../modules/transportdiagnostics/SPEC.md#retained-request-timing).

Inspection runs no recorded operations. Rollout and debug-bundle reports omit private
text unless `--field` selects it; `--field instructions` selects only retained instruction text.
`--request-id ID --field all` drills into one debug request.
Failure lookup with `--failures [REF]` always includes complete private error text,
which may contain request content or credentials.

```sh
mekugi inspect-session --thread THREAD_ID
mekugi inspect-session --thread THREAD_ID --ax
mekugi inspect-session --thread THREAD_ID --field instructions --text-bytes 65536
mekugi inspect-session --debug-dir /path/to/debug --field diagnostic --limit 5 --text-bytes 512
```

Narrow debug reports by `--request-id` and paginate with `--offset`. Select JSON fields rather
than line-limiting minified output.

See [session inspection](../../modules/sessioninspection/SPEC.md) and [AX evidence](../../modules/agentmetrics/SPEC.md).

### Replay a session

```sh
mekugi replay-session --session SESSION_ID
```

Use the literal full session ID from `CODEX_HOME` sessions/archives, not a path. Replay makes no provider, command,
or approval calls; it simulates retained text/timing, not original inputs or live
previews. Missing evidence is reported; private content may appear on screen. `p`
pauses, `+`/`-` changes speed, `[`/`]` seeks, `r` restarts, `q` quits. Use `--from 5m
--until 6m` for intervals or `--headless --width 160 --height 48 --seed 1 --speed 1` for
repeatable timing. Fix inputs, speed, and dimensions for comparisons. Replay measures rendering,
not terminal backpressure or live host/provider latency. See
[replay](../../modules/sessionreplay/SPEC.md).

## Profile live sessions and replay

For reported live CPU/RAM faults, identify the affected binary, retained history/store scale,
and lifecycle phase. Sample the actual process within the authorized isolation boundary, or
reproduce those dimensions in an isolated process. Fresh sessions and replay cannot establish
coverage of missing retained process state.

`make mekugi-pprof` builds a separate optimized executable with symbols and the
`pprof` build tag, plus its sibling executor, without installing or replacing the
normal command. Launch arguments, worker dispatch and hooks retain their behavior.
The normal build starts no profiling listener.

Launch or [replay](../../modules/sessionreplay/SPEC.md) with `bin/mekugi-pprof`. For rendering profiles,
use `bin/mekugi-pprof replay-session --session ID --headless --width 160 --height 48`.
Each owning process binds its own OS-assigned IPv4 loopback port and prints
`mekugi-pprof: http://127.0.0.1:PORT/debug/pprof/` to stderr before UI handoff.
The native interactive client opens its listener before connecting to the server;
profile that URL for client rendering and the server URL for session execution.
The standalone server opens its listener before accepting sessions. Start a CPU
capture before connecting a client to include session startup and resume costs;
all server-owned sessions share that listener. Use its URL with `go tool pprof`.
Workers and hooks start no listener; process completion cancels active captures
and closes the profiling listener.

Standard routes expose CPU, heap, allocs, goroutine, block, mutex and execution
trace data. CPU and trace collection start only on request. Block sampling uses
a 1 ms blocked-time rate; mutex sampling records one in ten contention events.
Instrumentation may perturb performance. Profiles cover the Mekugi process,
including UI, routing and replay, not separate Codex, executor or worker processes.

The routes have no additional authentication: local callers can read command-line
arguments and other private process data. Keep captures private. Profiling is
independent of sanitized metrics and debug evidence.
