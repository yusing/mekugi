# Installed-Codex acceptance

Fixtures need the `codex_e2e` tag and the installed Codex. They use deterministic local providers,
fake credentials and isolated home/configuration, never live models or personal shell startup
files, so they do not establish live-model behavior.

The migrated profile-launch and configured-catalog fixtures use
`mekugi-app-server` and its sibling `mekugi-models.json`. Build them with
`make build-native` and put `bin/native` on PATH. Remaining stock CLI fixtures
still require `codex`.

`make test-host` runs the focused set: authenticated frontends, PTY yield/continuation, durable
nested edits and sandbox reports. Missing Codex or tests fail; a skip (sandbox reporting is
Linux-only) is missing coverage.

Launch fixtures live in `apps/mekugi/internal/launch/*_codex_e2e_test.go`. The compaction fixture
reads `MEKUGI_E2E_MODEL`, which the Makefile sets; raw `go test` runs must set it.

| Fixture | Coverage and limits |
| --- | --- |
| OpenAI | Local base-URL override: web-tool exposure, turn metadata, reasoning-effort updates and workspace routing over WebSockets and HTTP/SSE fallback; WebSocket prewarm and side-thread continuity. `TestCodeModeOnlyCatalogNativeCodex` covers account-catalog execution-mode enforcement with a stale shared cache, stock JavaScript shell execution, and authenticated standalone web search through its returned tool result, without a profile |
| Compaction | `TestCompactReloadNativeCodex`: built-in OpenAI manual/automatic summary-free reset over HTTP/WebSockets, selected and missing-source reload, stable tool/cache identity and immutable fresh-resume results. Deterministic local responses establish installed-host behavior, without live-model selection claims |
| Frontends | Authenticated command through stock `tools.exec_command`: cwd, environment, argv/stdin separation and exit status |
| mrun | Codex-owned PTY yield and `write_stdin` continuation |
| mchanges | Nested stock patch/shell calls with unprinted results; durable receipts, net review, cross-thread explicit IDs, caught failures and cross-request yielded completion; traces removed before reads, replay store reopened |
| Reasoning | Provider stream gated on PTY frames: waiting before text, two incremental summaries, elapsed time and completed folding; no claim about real-provider cadence |
| Guards | Fake VCS executables: native hook dispatch, approvals, denial and helper-failure blocking; unrelated MCP disconnect and hook helper removal/failure after admission leave effect markers absent; excludes hook-shell launch/timeout fail-closed behavior |
| Segment reports | Installed execution and measured reports under read-only/workspace-write sandboxes |
| Router fault | Translation failure after response creation: one upstream request, one terminal failure and durable reference lookup; pre-stream 503 remains retryable |
| Profile routing | `TestProfileLaunchInstalledCodex`: actual profile import, native anonymous/env/literal/managed/command/refresh auth, inherited provider headers/capabilities, static/remote/unknown-model catalogs, HTTP and WebSockets, stock JavaScript child execution/captures and fresh profile resume; synthetic local providers only |

Run affected host checks without changing host policy:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^(TestAppServerVCSGuardNativeCodexYolo|TestAppServerGuardTransportFailureNativeCodex)$' TEST_FLAGS='-tags=codex_e2e -count=1'
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestAppServerExecTrackNativeCodex(Sandbox)?$' TEST_FLAGS='-tags=codex_e2e -count=1'
```

Backend hook discovery (invocation-only replacement, identities/trust, unchanged persistent
configuration; no thread or turn):

```sh
make test TEST_PACKAGES=./backends/codex/shellhook TEST_RUN='^TestGuardDiscoveryConfigurationNativeCodex$' TEST_FLAGS='-tags=codex_e2e -count=1'
```

Compile tagged Launch fixtures without running them:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^$' TEST_FLAGS='-tags=codex_e2e'
```
