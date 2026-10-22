# Installed-Codex acceptance

Fixtures use the installed Codex with deterministic local providers, fake credentials,
and isolated home/configuration directories. They require `codex_e2e` and exercise stock
execution and native collaboration without live model usage or personal shell startup files.

## Focused host acceptance

`make test-host` checks authenticated frontends, PTY yield/continuation, durable nested edits,
and sandbox reports. It verifies every named test exists, then runs a fresh verbose suite.
Missing Codex/tests fail; platform skips remain visible (sandbox reporting is Linux-only).
It neither generates assets nor installs binaries.

A skip is missing coverage. This set complements the [offline suite](CONTEXT-TESTS.md) and
feature fixtures below; it does not establish live-model behavior.

## Feature fixtures

Launch fixtures live in `apps/mekugi/internal/launch/*_codex_e2e_test.go`. List tests with:

```sh
rg '^func Test' apps/mekugi/internal/launch/*_codex_e2e_test.go
```

| Fixture | Coverage and limits |
| --- | --- |
| OpenAI | Local base-URL override: web-tool exposure, turn metadata, reasoning-effort updates and workspace routing over WebSockets and HTTP/SSE fallback; WebSocket prewarm and side-thread continuity. `TestCodeModeOnlyCatalogNativeCodex` covers account-catalog execution-mode enforcement with a stale shared cache and actual JavaScript tool execution |
| Compaction | `TestCompactReloadNativeCodex`: built-in OpenAI manual/automatic compaction over HTTP/WebSockets, caller prompt precedence, thread-specific read permissions, immutable fresh-resume and fork results. Deterministic local summaries establish installed-host behavior, without live-model selection claims |
| Frontends | Authenticated command through stock `tools.exec_command`: cwd, environment, argv/stdin separation and exit status |
| mrun | Codex-owned PTY yield and `write_stdin` continuation |
| mchanges | Nested stock patch/shell calls with unprinted results; durable receipts, net review, cross-thread explicit IDs, caught failures and cross-request yielded completion; traces removed before reads, replay store reopened |
| Reasoning | Provider stream gated on PTY frames: waiting before text, two incremental summaries, elapsed time and completed folding; no claim about real-provider cadence |
| Guards | Fake VCS executables: native hook dispatch, approvals, denial and helper-failure blocking; unrelated MCP disconnect and hook helper removal/failure after admission leave effect markers absent; excludes hook-shell launch/timeout fail-closed behavior |
| Segment reports | Installed execution and measured reports under read-only/workspace-write sandboxes |
| Router fault | Translation failure after response creation: one upstream request, one terminal failure and durable reference lookup; pre-stream 503 remains retryable |
| Third-party authentication | Empty isolated Codex credentials, bundled catalog and real Grok adapter with local HTTP/WebSocket mocks; plain/namespaced IDs, one inference and credential separation |
| Third-party resume | UI and real Grok adapter: saved model restoration after fresh launch/switching, explicit overrides, inferred models and thread identity in both namespaces |

Run affected host checks without changing host policy:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^(TestAppServerVCSGuardNativeCodexYolo|TestAppServerGuardTransportFailureNativeCodex)$' TEST_FLAGS='-tags=codex_e2e -count=1'
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestAppServerExecTrackNativeCodex(Sandbox)?$' TEST_FLAGS='-tags=codex_e2e -count=1'
```

Backend discovery checks invocation-only caller-hook replacement, hook identities/trust and
unchanged persistent configuration. It starts no thread or model turn:

```sh
make test TEST_PACKAGES=./backends/codex/shellhook TEST_RUN='^TestGuardDiscoveryConfigurationNativeCodex$' TEST_FLAGS='-tags=codex_e2e -count=1'
```

Compile tagged Launch fixtures without running them:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^$' TEST_FLAGS='-tags=codex_e2e'
```

These checks do not decrypt prior encrypted assignments.

## Live Codex requests

The Makefile defines and exports `MEKUGI_E2E_MODEL`, the model for E2E tests making real
Codex provider requests and the compaction host fixture. Override it through the environment or a Make command-line assignment
for a specific run. Direct test invocations require this variable; unset or blank values fail
before starting Codex. The `e2e` build tag enables
`TestCodexMekugiGrammarE2E` (file-edit acceptance) and `TestCodexCacheAffinityE2E`
(request identity and cache-affinity observations). Both use the installed Codex and forward
requests to the live OpenAI Codex endpoint using its authentication.

`TestCompactReloadLiveOpenAI` requires both `codex_e2e` and `e2e`. It drives
terminal `/compact` with the built-in OpenAI provider and real authentication,
covering live summary selection after completed work and fresh instruction-file
and skill reads before live continuation. Run it with:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestCompactReloadLiveOpenAI$' TEST_FLAGS='-tags=codex_e2e,e2e -count=1'
```

Compile these fixtures without making live requests:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^$' TEST_FLAGS='-tags=e2e'
```

Report real-provider coverage separately; use an isolated workspace and temporary build outside
the repository, never an installation build.
