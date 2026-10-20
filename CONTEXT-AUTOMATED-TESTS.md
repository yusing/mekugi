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
| OpenAI | Local base-URL override: web-tool exposure, turn metadata, reasoning-effort updates and workspace routing over WebSockets and HTTP/SSE fallback; WebSocket prewarm and side-thread continuity |
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

These checks do not decrypt prior encrypted assignments. When real-provider behavior is needed,
report its coverage separately; use an isolated workspace and temporary build outside the
repository, never an installation build.
