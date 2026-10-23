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
covering live summary selection after completed work and fresh instruction-file
and skill reads before live continuation. Run it with:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestCompactReloadLiveOpenAI$' TEST_FLAGS='-tags=codex_e2e,e2e -count=1'
```

Compile these fixtures without making live requests:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^$' TEST_FLAGS='-tags=codex_e2e,e2e'
```

Report real-provider coverage separately; use an isolated workspace and temporary build outside
the repository, never an installation build.
