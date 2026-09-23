# Automated live Codex tests

Tagged router fixtures use the installed Codex consumer with a deterministic
local provider. They exercise stock execution and native collaboration without
credentials or live model usage:

```sh
go test -tags journal_e2e ./internal/router -run '^TestConfiguredToolFrontendNativeCodexE2E$'
go test -tags journal_e2e ./internal/router -run '^TestMRunNativeCodexYieldAndWriteStdinE2E$'
go test -tags journal_e2e ./internal/router -run '^TestJournalNativeCodexSpawnE2E$'
```

The frontend fixture invokes an authenticated configured command through
stock Code Mode `tools.exec_command`, including cwd, environment, argv, stdin
separation, and exit status. The mrun fixture checks Codex-owned PTY yield and
`write_stdin` continuation. The journal fixture checks native child
assignment, live milestones, terminal child result, and parent delivery without
an extra final-answer provider request.

The fixtures are under `internal/router/tool_frontend_codex_e2e_test.go` and
`internal/router/journal_codex_e2e_test.go`. A tagged compile-only check is
`go test -tags journal_e2e ./internal/router -run '^$'`.

These deterministic tests do not prove live-model behavior or decrypt prior
encrypted assignments. If a task needs real provider behavior, report that
coverage separately and use an isolated workspace and a temporary build
outside the repository; do not use an installation build.
