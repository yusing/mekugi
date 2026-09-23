Create a tiny Go library while exercising stock Codex editing, execution, and Mekugi journal publication.
Create exactly `go.mod` and `coverage.go`; do not add other files. The module and root package must
both be named `commentarycoverage`, use Go 1.26, and have no third-party dependencies.

Complete these steps in order. The tool choices and commentary values are part of the benchmark:

1. Use stock `apply_patch`, directly or through Code Mode, to create both files. Define an
   unexported string constant named `status` with value `draft`, and export `func Status() string`
   returning that constant.
2. Use stock `exec_command` to change only that constant from `draft` to `current` with `sed`.
   Publish `coverage:command` with the journal tool and request immediate delivery.
3. Use stock `apply_patch` to change the complete constant declaration from `current` to `ready`.
   Publish `coverage:edited` with the journal tool and request immediate delivery.
4. If `functions.report_issue` is available, call it once with Markdown stating that the stock edit
   sequence completed and no failure was observed. Then publish `coverage:reported` with immediate
   journal delivery. Skip this step when the optional tool is absent.
5. Use stock `exec_command` to run `gofmt -w coverage.go` and `go test ./...`.
   Publish `coverage:validated` with immediate journal delivery.
6. Use stock `exec_command` to run `printf '%s\n' coverage:exec-complete`.
7. In Code Mode JavaScript, publish `coverage:code-mode` through
   `await journal({op: "add", text: "coverage:code-mode", report_now: true})`, then emit
   `coverage:code-mode-complete` with `text(...)`.

The final workspace is complete only when `Status()` returns `ready`, package validation passes,
and every required operation above has completed. Then make the final assistant response exactly:

```text
verification: exhaustive commentary coverage passed
```

Return no code fence or other text, with no leading or trailing line feed.
