# Codex router end-to-end behavior

The original workspace observations below were recorded on 2026-07-28 with Codex CLI
0.145.0, `codex-dynamic`, and `gpt-5.6-luna`. They are dated evidence, not an
eternal Codex contract. Re-run focused E2E checks after a Codex upgrade before
changing routing assumptions.

### Built-in OpenAI through the local endpoint (2026-10-08)

Codex CLI 0.161.0 passes with the existing base-URL override, retaining its
built-in OpenAI provider while requests pass through Mekugi's existing projection
owners.
`TestAppServerBuiltInOpenAINativeCodexE2E` passes native web-tool exposure,
turn metadata, effort changes, and selected-workspace routing over
WebSockets and stock HTTP/SSE fallback. Prewarming and side-thread continuity
pass over WebSockets.
### Native nested-tool receipts (2026-09-25)

Installed Codex CLI 0.156.1 was exercised with a deterministic local provider by
`TestMChangesNestedNativeCodexE2E`. Native rollout tracing linked outer calls to
`exec` cells and individual nested tool results without printing those results
or modifying execution. Patch success and shell exit zero produced durable
confirmed captures; a caught patch failure and shell exit 7 remained failed
despite outer-cell completion. A failed patch without file effects allocated no
change ID, while a failed command's file effects remained reviewable.

The fixture checked both a running cell continued by `wait` and an already
completed cell whose nested command was still running, followed by `write_stdin`.
Neither produced completed changes before terminal native evidence. It also
deleted native traces, reopened the replay store, and read explicit author IDs
through another thread's authenticated frontend.

The native recorder writes whole-session payloads, not only tool receipts, and
has no tool-only filter or disk cap. Mekugi's launch-scoped cleanup does not imply
otherwise. A non-repository temporary directory again supplied no workspace
metadata; initializing the fixture repository made its root available for
relative capture. No router-cwd fallback was used.

### Anchored child Activity history (2026-09-30)

Codex CLI 0.159.2 accepted `thread/items/list` item anchors introduced by
`de9e78e3e` (#48151), including anchors in a fork's inherited visible turn.
`TestAppServerAnchoredHistoryNativeCodexE2E` exercises the native child loader
against an isolated, host-migrated synthetic rollout with 2,000 assistant
messages and one user item. It verifies the newest 100 items, the next older
100, chronological projection, stable page boundaries, and turn timing.
No provider or account is used. The existing native resume PTY fixture also
passes with the installed host.

The full result was 8,467,204 bytes; metadata plus the first 100-item page
was 434,124 bytes, about 94.9% less initial payload. One local diagnostic run
measured full read plus projection at 264 ms versus paged hydration at 87 ms.
These synthetic, single-machine timings are not a live-session latency SLA,
model-consumption claim, or proof of provider performance. The repeatable
acceptance asserts bounded payload and content, not a timing threshold.

The host item-list API supports paginated histories, not legacy rollouts.
Mekugi observes the reported history mode; it does not migrate user sessions
or fall back to an unbounded read after a paginated read fails. Main's
full-history resume and oversized individual items remain subject to the
existing 16 MiB RPC frame cap. New older-history controls have rendered
snapshots and interaction tests, but no dedicated interactive PTY paging
fixture; the installed-host acceptance drives the real loader directly.

### Codex workspace metadata

- A session started inside this Git repository declared the enclosing
  repository root as its base directory. A nested `-C` did not declare a
  second base directory.
- `--add-dir` added a sandbox-writable root but did not add it to
  `x-codex-turn-metadata` `workspaces`. That observation predates the current
  stock-execution cutover and does not imply router confinement.
- A standalone non-repository temporary workdir supplied no usable base
  directory. The router must not substitute its own cwd for relative
  observation operands.

Stock `apply_patch` and `exec_command` remain Codex-owned. The router uses
selected workspace metadata only for bounded observation and durable review
scope, not to authorize or execute a patch. See the
[filesystem boundary](architecture/boundary.md) and
[stock execution](spec/execution.md) contracts.

### Interpret end-to-end evidence

A transcript tool label or model final answer is not proof of the selected
workspace or successful edit. Inspect the exact stock call, host result, and
filesystem outcome. A streamed live diff is provisional until the result and
workspace state are known. The router serves `GET /v1/models`; a model-refresh
`404 Not Found` is a routing or version mismatch, not an expected limitation.

### Reproduction shape

Launch the router and probe together from this repository:

```sh
go run ./apps/mekugi/cmd/mekugi codex --model gpt-5.6-luna \
  --sandbox workspace-write --ask-for-approval never \
  exec --ephemeral -C /absolute/path/to/disposable/fixture "PROMPT"
```

Use a disposable temporary directory outside the repository for probes and
verify the resulting paths. Initialize it as a Git repository when checking
selected-workspace capture; a non-repository directory may supply no workspace
metadata. The wrapper stops its router when Codex exits.
Dated observations do not substitute for focused E2E tests with the installed
Codex.
