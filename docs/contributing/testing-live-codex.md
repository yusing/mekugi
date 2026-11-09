# Live Codex requests

Run commands from the repository root using the [common test runner](../../CONTEXT-TESTS.md#runner).

The Makefile defines and exports `MEKUGI_E2E_MODEL`, the model for E2E tests making real
Codex provider requests. Override it through the environment or a Make command-line assignment
for a specific run. Direct test invocations require this variable; unset or blank values fail
before starting Codex. The `e2e` build tag enables
`TestCodexMekugiGrammarE2E` (file-edit acceptance) and `TestCodexCacheAffinityE2E`
(request identity and cache-affinity observations). Both use the installed Codex and forward
requests to the live OpenAI Codex endpoint using its authentication.

`TestCompactReloadLiveOpenAI` requires both `codex_e2e` and `e2e`. It drives
terminal `/compact` with the built-in OpenAI provider and real authentication,
covering summary-free reset after completed work and ordinary model-directed
file and skill reads on live continuation. Run it with:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestCompactReloadLiveOpenAI$' TEST_FLAGS='-tags=codex_e2e,e2e -count=1'
```

`TestSideCacheEligibilityLiveOpenAI` also requires both tags. It warms Main, then checks
that the first `/btw` and `/oneoff` requests preserve the prepared prefix and
report cached input. Each command runs with Main-first and fork-first continuations,
checking each branch's prepared prefix, native thread identity, separate history and
cached input. Provider cache availability can still cause a miss despite an identical
prefix. Run it with:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestSideCacheEligibilityLiveOpenAI$' TEST_FLAGS='-tags=codex_e2e,e2e -count=1 -v'
```

`TestRewindCacheEligibilityLiveOpenAI` uses both tags and checks the first request
after terminal `/rewind` against a warmed retained prefix. It verifies same native
thread and provider affinity, retained/excluded history, prepared fields and
cached input. Confirmed same-workspace rewind preserves the immutable environment
snapshot while the native context/history revision still advances. Later resets
and workspace changes refresh independently. Provider cache availability can
still cause this opt-in check to fail despite an identical prefix.

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestRewindCacheEligibilityLiveOpenAI$' TEST_FLAGS='-tags=codex_e2e,e2e -count=1 -v'
```

Compile these fixtures without making live requests:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^$' TEST_FLAGS='-tags=codex_e2e,e2e'
```

Report real-provider coverage separately; use an isolated workspace and temporary build outside
the repository, never an installation build.
