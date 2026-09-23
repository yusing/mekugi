# Agent issue reports

## REQ-DIAGNOSE-001 — Agent issue reports

When Mekugi mode starts with inherited `MEKUGI_DIAGNOSE=1`, the router adds a
`report_issue` function to the stock tool catalog. The function accepts one
`markdown` string. Other values and passthrough mode leave the catalog alone.
Issue reporting is a router-local diagnostic call, not an editing or execution
replacement and not an executable plugin frontend.

The router snapshots `hooks.diagnose` from `settings.json` at startup. A
completed report calls each configured command once with the exact Markdown as
`.Body` and the current Codex task title as `.Title`; the fallback title is
`mekugi diagnostic`. `format_markdown` returns the same body, and `shellquote`
quotes a string for the hook shell. Commands share a 10-second timeout. An
empty hook list succeeds without side effects. Invalid settings prevent
startup; command or template failures yield a `mekugi: warning:` tool result
without interrupting response routing.

The router intercepts this function before Codex dispatch. It durably records
the completed call/result and restores that exact pair on continuation so
replay never runs a hook again. Incomplete calls do not run hooks. The function
does not observe, execute, or alter stock `apply_patch`.

Acceptance:

1. Only exact opt-in exposes one `report_issue` function. Configured frontend
   names cannot collide with it when enabled.
2. One completed report sends its body and task title to every configured
   diagnose hook; replay or duplicate stream completion does not rerun it.
3. No hooks is a successful no-op; hook failures remain visible as result
   warnings without turning the response into an execution failure.
4. No executable wrapper or plugin worker is installed for this function.
