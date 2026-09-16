# Journal options

Bash/POSIX supports `journal list [AGENT]`, `journal add TEXT`, `journal edit ID TEXT`,
`journal delete ID`, `journal batch JSON_ARRAY`, and `journal finish [JSON_ARRAY]`.
Add/edit accept trailing `--answer` or `--clear-answer`; add/edit/delete accept `--report-now`.
Use `--json` to return assigned IDs or the finish result; list always returns JSON.
Required operands remain exact argv values, even when they start with `--`.
Other interpreters have no journal builtin. Successful non-JSON mutations write no script output.

Structured mutations use normal Markdown in `text`. On edit, omit `answer` to preserve
its attached question, or set it to false to turn the item into a milestone.
Only plaintext native assignments can be attached; omit `answer` for encrypted assignments.
Each mutation array is atomic. `report_now` requests an immediate user-visible notice.
