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
