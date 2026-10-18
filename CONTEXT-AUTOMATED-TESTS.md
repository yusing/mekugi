# Automated live Codex tests

Tagged Launch fixtures use the installed Codex consumer with a deterministic
local provider. They exercise stock execution and native collaboration without
credentials or live model usage. The shared host environment isolates the home
and configuration directories so shell snapshots do not source personal startup
files. These tests require the `codex_e2e` build tag.

## Focused host acceptance

Run `make test-host` for the shared installed-Codex acceptance set. It checks native
frontends, PTY yield/continuation, durable nested edit evidence, and command sandbox reporting.
The recipe verifies that every named test is present before running a fresh verbose
suite. Missing Codex or tests fail the target. It neither generates assets nor installs
binaries.

Platform-specific skips remain visible in verbose output; sandbox reporting is Linux-only.
A skip is unavailable coverage, not a pass for that boundary. This focused set does not
replace the offline suite or feature-specific host fixtures listed below. It uses local
mock providers and does not establish live-model behavior.

## apps/mekugi/internal/launch

The fixtures are the `apps/mekugi/internal/launch/*_codex_e2e_test.go` files. List them with
`git grep -h '^func Test.*E2E' -- 'apps/mekugi/internal/launch/*_codex_e2e_test.go'`.

The built-in OpenAI fixture uses the existing local base-URL override and fake
credentials. It checks native web-tool exposure, internal turn metadata,
reasoning-effort updates, and selected-workspace routing over WebSockets
and stock HTTP/SSE fallback. It also checks prewarming and side-thread continuity
over WebSockets. These local mocks prove installed-host behavior; live model
acceptance is a separate check.

The frontend fixture invokes an authenticated native command through
stock exec `tools.exec_command`, including cwd, environment, argv, stdin
separation, and exit status. The mrun fixture checks Codex-owned PTY yield and
`write_stdin` continuation.

The mchanges fixture executes nested stock patch and shell calls without printing
their results, then checks durable native receipts, net review, explicit-ID reads
from another thread, caught failures, and cross-request yielded-command completion.
Its native traces are deleted before
review reads, and the replay store is reopened to verify durable confirmation.

The reasoning fixture gates a local provider stream on rendered PTY frames. It
checks waiting before public text, two incremental public-summary updates before
completion, elapsed-duration display, and folding after completion.
It does not establish a real provider's public-summary delivery cadence.

The guard fixtures use fake VCS executables and the installed host to verify
native command-hook dispatch, approvals, denial, and helper failure blocking:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^(TestAppServerVCSGuardNativeCodexYolo|TestAppServerGuardTransportFailureNativeCodex)$' TEST_FLAGS='-tags=codex_e2e -count=1'
```

The transport-failure cases disconnect an unrelated MCP endpoint or remove/fail
the hook helper after turn admission. Effect markers must remain absent. These
checks do not establish fail-closed host hook-shell launch or timeout behavior.

The backend's `TestGuardDiscoveryConfigurationNativeCodex` checks invocation-only
caller-hook replacement, preserved hook identities/trust, and unchanged persistent
configuration. It starts no thread or model turn:

```sh
make test TEST_PACKAGES=./backends/codex/shellhook TEST_RUN='^TestGuardDiscoveryConfigurationNativeCodex$' TEST_FLAGS='-tags=codex_e2e -count=1'
```

The segment-report fixtures in `app_server_exec_track_codex_e2e_test.go` check
installed Codex execution and measured reports under read-only and workspace-write
sandboxes. Run the affected acceptance without changing the host policy:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestAppServerExecTrackNativeCodex(Sandbox)?$' TEST_FLAGS='-tags codex_e2e -count=1'
```

A tagged compile-only check is `go test -tags codex_e2e ./apps/mekugi/internal/launch -run '^$'`.

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
