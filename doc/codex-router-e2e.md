# Codex router end-to-end behavior

The original workspace observations below were recorded on 2026-07-28 with Codex CLI
0.145.0, `codex-dynamic`, and `gpt-5.6-luna`. They are dated evidence, not an
eternal Codex contract. Re-run focused E2E checks after a Codex upgrade before
changing routing assumptions.

### Native nested-tool receipts (2026-09-25)

Installed Codex CLI 0.156.1 was exercised with a deterministic local provider by
`TestMChangesNestedNativeCodexE2E`. Native rollout tracing linked outer calls to
Code Mode cells and individual nested tool results without printing those results
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

### Journal compaction (2026-09-29)

Codex CLI 0.158.0 accepted a router-synthesized local compaction response in
`TestJournalCompactionNativeCodexE2E`, using a deterministic local provider and an
isolated Git workspace. No compaction request reached that provider. The immediate
continuation contained journal task and retained change evidence; the matching
`SessionStart` hook omitted duplicate recovery while an independent hook still ran.
The legacy provider-summary hook fixture also passed. These are offline protocol
checks, not evidence that a model resumes better or uses fewer task tokens.

The non-repository fixture emitted no compaction workspace metadata. The router
can recover only a unique prior selected workspace from durable requesting-thread
records, never its process cwd. Ambiguous retained workspaces keep provider fallback.

`TestJournalSliceResetNativeCodexE2E` also passed with CLI 0.158.0. The shared
app-server driver requested manual local compaction, accepted the router summary,
then started the next planned turn. The continuation carried the pending path and
recovered task states, and the durable reset intent was consumed. The mock provider
saw two ordinary turns and no compaction request. The fixture seeds journal facts;
it does not measure model adherence to slice planning or context-size improvement.

`TestJournalHeadlessNativeCodexE2E` exercises the production headless adapter with
the same installed host and mock provider. Both `off` and `slice` complete two
planned turns and emit valid JSONL; only `slice` produces a compaction item. The
host shuts down cleanly after the terminal event. This is not a paid comparison.

### Native child journal visibility (2026-09-30)

Codex CLI 0.159.2 (`rust-v0.159.2`, host commit
`ff6aec96948b70d94983af2641a6b67c94faeff5`) does not merge child provider
commentary into the root's `exec --json` messages. Child lifecycle events are
not a text-publication channel in that consumer. The host's root-only
`send_message_to_user_async` tool needs a root model call; it is not a router
injection API.

`TestJournalNativeCodexSpawnE2E` gates the child's final provider response on
observing its live milestone in actual root JSONL output. With separate durable
root live delivery, the gate passes while preserving the child result, one final
root flush, and two child provider requests. Live notices do not enter provider
message input. This deterministic local-provider check establishes consumer
delivery, not live-model behavior. Root-only publication still needs a writable
root response; it cannot display a new milestone during a host tool wait until
that response channel opens again.

### Host-result journal completion (2026-10-02)

Codex CLI 0.160.0 accepted `TestJournalHostFinishNativeCodexSpawnE2E` with
a deterministic local provider. Root and native child carried the finish marker
inside useful stock Code Mode execution. Their matching successful results
selected local terminal journal delivery without another provider request or
a provider-generated acknowledgment. No standalone journal tool was advertised.
The ordinary `TestJournalNativeCodexSpawnE2E` also passed, preserving substantive
provider-authored finals and child-result delivery.

These fixtures establish installed-host acceptance, not live-model marker usage
or interactive terminal behavior. Offline checks separately cover failed and
yielded work, native trace requirements, continuation matching, and retained
receipt isolation across restart and fork.

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
go run ./cmd/mekugi codex --model gpt-5.6-luna \
  --sandbox workspace-write --ask-for-approval never \
  exec --ephemeral -C /absolute/path/inside/this/repository "PROMPT"
```

Use a disposable temporary directory outside the repository for probes and
verify the resulting paths. The wrapper stops its router when Codex exits.
Dated observations do not substitute for focused E2E tests with the installed
Codex.
