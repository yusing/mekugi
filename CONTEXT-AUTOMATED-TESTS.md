# Automated live Codex tests

Tagged router fixtures use the installed Codex consumer with a deterministic
local provider. They exercise stock execution and native collaboration without
credentials or live model usage. These tests require the `journal_e2e` build tag.

## internal/router

The fixtures are the `internal/router/*_codex_e2e_test.go` files. List them with
`git grep -h '^func Test.*E2E' -- 'internal/router/*_codex_e2e_test.go'`.

The built-in OpenAI fixture uses the existing local base-URL override and fake
credentials. It checks native web-tool exposure, internal turn metadata,
reasoning-effort updates, and selected-workspace journal identity over WebSockets
and stock HTTP/SSE fallback. It also checks prewarming and side-thread continuity
over WebSockets. These local mocks prove installed-host behavior; live model
acceptance is a separate check.

The frontend fixture invokes an authenticated configured command through
stock exec `tools.exec_command`, including cwd, environment, argv, stdin
separation, and exit status. The mrun fixture checks Codex-owned PTY yield and
`write_stdin` continuation. The journal fixture checks native child
assignment, live milestones, terminal child result, and parent delivery without
an extra final-answer provider request.

The host-finish journal fixture carries an MCP mutation batch and completion marker
with useful stock execution inside exec for both root and native child. It verifies
terminal journal delivery without forwarding the tool-result continuation to the
provider, including journals retained without workspace metadata. The ordinary journal fixture
retains provider-authored substantive-final coverage.

The journal MCP read fixture checks standard nested tool discovery and execution
through installed Codex. It verifies that host-supplied identity selects the
durable journal and that structured results reach the exec-cell continuation.

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
The native journal-presentation fixture drives ordinary manual `/compact` in
`auto` mode through real app-server notifications, checking journal-reset rendering
and exact host-item provenance after reopening storage, without slice-driven dispatch.
The native journal-rollout disclosure fixture also checks the installed host's
paginated item-to-compaction-response association with built-in OpenAI V2 compaction.
The slice-reset fixture drives manual app-server compaction followed by the next
planned turn through the shared reset policy. It verifies the continuation path,
recovered summary and consumed intent without a provider compaction request.
It covers both built-in OpenAI Responses compaction V2 and legacy custom-provider
text compaction. The built-in fixture uses isolated fake credentials and stock
HTTP/SSE fallback against the local listener.
It reopens durable storage and restarts Codex, then verifies exact V2 summary
restoration through native thread resume and fork without reference leakage.
The built-in fixture loads an instruction-named file through real stock execution
in both exec-cell and function-call forms. It checks fresh reset-time guidance at
the local provider on continuation, resume and fork, including function-call
history whose tool is absent from the current declarations. These checks verify
installed-host routing and local payloads, not live-provider acceptance of the
synthetic history.
The headless fixture exercises the production JSONL adapter with both `off` and
`slice`, proving two turns, reset-only compaction and clean host shutdown.

The reasoning fixture gates a local provider stream on rendered PTY frames. It
checks waiting before public text, two incremental public-summary updates before
completion, elapsed-duration display, and folding after completion.
It does not establish a real provider's public-summary delivery cadence.

The segment-report fixtures in `app_server_exec_track_codex_e2e_test.go` check
installed Codex execution and measured reports under read-only and workspace-write
sandboxes. Run the affected acceptance without changing the host policy:

```sh
make test TEST_PACKAGES=./internal/router TEST_RUN='^TestAppServerExecTrackNativeCodex(Sandbox)?$' TEST_FLAGS='-tags journal_e2e -count=1'
```

A tagged compile-only check is `go test -tags journal_e2e ./internal/router -run '^$'`.

These deterministic tests do not prove live-model behavior or decrypt prior
encrypted assignments. If a task needs real provider behavior, report that
coverage separately and use an isolated workspace and a temporary build
outside the repository; do not use an installation build.

The router-fault fixture forces an intercepted-call translation failure after response
creation and checks one upstream request, one terminal turn failure, and durable reference
lookup. Its retry case confirms a pre-stream 503 still permits a successful retry.

The third-party authentication fixture uses empty isolated Codex credentials and
its bundled native catalog. Dedicated plain-ID and standalone namespaced Grok
requests complete through the real adapter with a local mock provider over HTTP
and WebSocket. It checks one inference and credential separation without live
provider usage.

The third-party resume fixture uses the UI and the real Grok adapter to
verify saved model restoration after a fresh launch and in-session switching,
plus explicit model overrides, in both namespaces. It checks inferred models
and thread identity against a local provider, without live model usage.
