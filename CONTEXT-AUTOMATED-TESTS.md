# Automated live Codex tests

Tagged router fixtures use the installed Codex consumer with a deterministic
local provider. They exercise stock execution and native collaboration without
credentials or live model usage. These tests require the `journal_e2e` build tag.

## internal/router

- `TestConfiguredToolFrontendNativeCodexE2E`
- `TestMChangesNestedNativeCodexE2E`
- `TestMRunNativeCodexYieldAndWriteStdinE2E`
- `TestJournalNativeCodexSpawnE2E`
- `TestPostCompactNativeCodexE2E`
- `TestJournalCompactionNativeCodexE2E`
- `TestJournalSliceResetNativeCodexE2E`
- `TestJournalHeadlessNativeCodexE2E`
- `TestAppServerReasoningStreamNativeCodexE2E`
- `TestRouterTransformFaultNativeCodexE2E`
- `TestRetryablePrestream5xxStillRetriesInNativeCodexE2E`

The frontend fixture invokes an authenticated configured command through
stock Code Mode `tools.exec_command`, including cwd, environment, argv, stdin
separation, and exit status. The mrun fixture checks Codex-owned PTY yield and
`write_stdin` continuation. The journal fixture checks native child
assignment, live milestones, terminal child result, and parent delivery without
an extra final-answer provider request.

The mchanges fixture executes nested stock patch and shell calls without printing
their results, then checks durable native receipts, net review, explicit-ID reads
from another thread, caught failures, and cross-request yielded-command completion.
Its native traces are deleted before
review reads, and the replay store is reopened to verify durable confirmation.

The post-compaction fixture forces native automatic compaction and verifies that
the immediate continuation contains the durable journal and change summary. It
also checks coexistence of file-based and invocation-local native hooks. Only
this isolated fixture bypasses hook trust; production registration requires the
normal Codex hook review. The fixture does not exercise the interactive `/hooks`
trust UI or manual `/compact` command.
The journal-compaction fixture additionally verifies that a router-authored
summary is accepted by installed Codex, never reaches the mock provider, restores
task/change evidence, and suppresses only the matching recovery hook injection.
The slice-reset fixture drives manual app-server compaction followed by the next
planned turn through the shared reset policy. It verifies the continuation path,
recovered summary and consumed intent without a provider compaction request.
The headless fixture exercises the production JSONL adapter with both `off` and
`slice`, proving two turns, reset-only compaction and clean host shutdown.

The reasoning fixture gates a local provider stream on rendered PTY frames. It
checks waiting before public text, two incremental public-summary updates before
completion, elapsed-duration display, and folding after completion.
It does not establish a real provider's public-summary delivery cadence.

The fixtures are the `internal/router/*_codex_e2e_test.go` files. A tagged compile-only check is
`go test -tags journal_e2e ./internal/router -run '^$'`.

These deterministic tests do not prove live-model behavior or decrypt prior
encrypted assignments. If a task needs real provider behavior, report that
coverage separately and use an isolated workspace and a temporary build
outside the repository; do not use an installation build.

The router-fault fixture forces an intercepted-call translation failure after response
creation and checks one upstream request, one terminal turn failure, and durable reference
lookup. Its retry case confirms a pre-stream 503 still permits a successful retry.
