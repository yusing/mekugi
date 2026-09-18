Create a tiny Go library while exercising Mekugi shell editing, recovery, and journal publication
in the order below. Create exactly `go.mod` and `coverage.go`; do not add other files. The module and root package
must both be named `commentarycoverage`, use Go 1.26, and have no third-party dependencies.

Complete these steps in order. The tool choices and commentary values are part of the benchmark:

1. Use one standalone `hpatch` command through `functions.shell` to create both files. Define an
   unexported string constant named `status` with value `draft`, and export `func Status() string`
   returning that constant.
2. Use `functions.shell` with its Bash evaluator. In that shell program, change only the constant value from
   `draft` to `current` with `sed`, then publish `coverage:bash` using
   `journal add coverage:bash --report-now`.
3. Submit a standalone `hpatch` command through `functions.shell` whose target is the now-stale
   complete constant declaration with value `draft`, replacing it with the same declaration whose
   value is `ready`. This call must be rejected as stale. Use `hpatch --recover HANDLE` through
   `functions.shell`, with the returned recovery handle and current command handle, to correct the
   target to the complete declaration with value `current`; preserve the replacement value `ready`. Keep each `hpatch` invocation separate from other shell commands.
   After successful recovery, publish `coverage:recovered` with `journal add coverage:recovered --report-now`.
4. When `functions.report_issue` is available, invoke it once after recovery. Give the report the
   title `Commentary coverage recovery` and state that the intentional stale-target recovery
   completed. After the call, publish `coverage:reported` with `journal add coverage:reported --report-now`.
   Skip this step when that optional tool is absent.
5. Use `functions.shell` with a compact `#!sh` selector. Run `gofmt -w coverage.go` and
   `go test ./...`, then publish `coverage:posix` with `journal add coverage:posix --report-now`.
6. Use `functions.shell` to run `printf '%s\n' coverage:exec-complete`.
7. Call the custom Code Mode `exec` tool with executable JavaScript. Publish `coverage:code-mode`
   through `await journal({op: "add", text: "coverage:code-mode", report_now: true})`, then emit
   `coverage:code-mode-complete` with `text(...)`.

The final workspace is complete only when `Status()` returns `ready`, package validation passes, and
every required operation above has completed. Then make the final assistant response exactly:

```text
verification: exhaustive commentary coverage passed
```

Return no code fence or other text, with no leading or trailing line feed.
