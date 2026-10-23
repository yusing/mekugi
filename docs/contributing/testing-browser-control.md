# Browser control transport checks

Run commands from the repository root using the [common test runner](../../CONTEXT-TESTS.md#runner).

The opt-in browser fixture uses `agent-browser` against the actual connector and
Unix-socket server with disposable credentials and no Codex/model invocation:

```sh
MEKUGI_TEST_CONTROL_BROWSER=1 make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestControlBrowser$'
```

`MEKUGI_TEST_AGENT_BROWSER` can select an absolute executable when a tool-manager
shim cannot run under isolated test configuration. The fixture owns and closes a
named browser session. It covers initialization, publication, mutation, native
method denial, reconnection and token rotation; TLS deployment remains separate.
`make check-generated` checks generated browser wire types as well as the module
index. Regenerate only browser types with
`go run ./apps/mekugi/cmd/sessionapi-schema`; full `make generate` also refreshes
other workspace-generated assets.
