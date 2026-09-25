# Executable semantic symbol lookup

## REQ-SYMBOL-001 — Executable semantic symbol lookup

The wrapped session supplies an authenticated `msymbol` executable on Codex's
session-private `PATH`:

```text
msymbol [--max-tokens N] [--workspace ROOT] (def|refs) PATH [LINE] SYMBOL [N]
```

Stock `tools.exec_command` launches the frontend under Codex's cwd, environment,
sandbox, signals, and process lifecycle. The frontend validates the pinned registry,
then delegates the queries to the generated symbol implementation. It is not a
model-visible custom tool or a private shell command. Output uses the shared
[reader token ceiling and read continuation](read.md). Reader options and
`--workspace ROOT` may surround the query operands.

The canonical workspace defaults to `realpath(process.cwd())`. An optional
`--workspace ROOT` selects another existing canonical directory for resolver scope
and relative input paths without changing the caller's working directory. `PATH`
may be relative or absolute, but its canonical target must remain within that
workspace and be a regular UTF-8 supported source file. Supported sources are Go
`.go`; Python `.py` and `.pyi`; JSON `.json`; and the stable TypeScript 7 formats
`.ts`, `.tsx`, `.d.ts`, `.mts`, `.d.mts`, `.cts`, `.d.cts`, `.js`, `.jsx`, `.mjs`,
and `.cjs`.

`LINE` is a positive current logical line. It needs no additional source token. Msymbol selects an exact language token on that line, rejects
missing or ambiguous occurrences before resolver startup, and rejects an input
change during the semantic query before emitting result rows. Successful queries do not echo the input location on stderr. Missing-token errors
list up to five nearest matching token lines; ambiguity errors give the occurrence count.
`PATH:LINE` and a JSON-quoted `"PATH":LINE` operand are also accepted.
A selector such as `store.readChanges` selects its final segment.
For `def PATH SYMBOL` and `refs PATH SYMBOL`, a unique complete outline declaration
supplies the selection; zero or multiple matches require an explicit line. Multiple-match
errors list the declaration lines; zero-match errors list up to five matching token lines.

`SYMBOL` selects an exact language token on the selected line. Go accepts
non-keyword identifiers; JavaScript and TypeScript accept their identifier,
property, private-name, type-name, and JSX-name tokens; Python accepts identifier
and property tokens; JSON accepts a decoded property-name or string token.
Comments, larger identifiers, and unrelated literal text do not count. `N`, when
present, is a positive base-ten occurrence without leading zeroes. When `N` is
absent, exactly one matching token must exist.

Missing-resolver errors identify the executable needed on the executor's `PATH`,
including TypeScript 7's `tsc --lsp` capability. Msymbol never installs
dependencies, searches for a different workspace, weakens result confinement, or
substitutes text search. Go and LSP processes both run in the selected workspace.

Several `(def|refs) PATH [LINE] SYMBOL [N]` tuples may follow each other in one
invocation. All inputs are validated before resolver startup. Tuples share one
language-server session per language in the selected workspace, and one combined
output budget. Results follow tuple order. A single Go query uses gopls CLI; a
Go batch uses one invocation-owned gopls LSP server. No detached cross-invocation
daemon is started. The session deadline is 30 seconds, shared by its queries;
timeouts use `resolver_timeout` and suggest a narrower `--workspace ROOT`. Final pipe
drain and protocol shutdown are bounded to one second; protocol replies receive a
separate one-second dispatch grace after process exit. A reply completed within
those bounds remains valid, and forced cleanup does not change completed stdout or
exit status. Reference queries include declarations. Missing dependencies, invalid
input, changed source, malformed protocol results, timeouts, and failed queries
return concise stderr and nonzero status without useful stdout.

Successful definitions have one header and raw body rows:

```text
"PATH":START-END
SOURCE TEXT
```

References group first-seen rows by canonical file, in first-seen file order:

```text
"PATH":
LINE SOURCE TEXT
```

`PATH` is the JSON-quoted path from the default canonical workspace root to the
canonical result file, without a leading `./`. With an explicit `--workspace`,
output paths are canonical absolute paths so a different
resolver root cannot make references point at same-named files in the caller's
directory. Each result file is canonical, in-workspace, regular, UTF-8, and owned
by the selected resolver; other returned locations are omitted and counted by
reason on stderr. References are deduplicated by canonical path and logical line.
Empty `refs` is successful. A `def` without an editable workspace location is
nonzero.

Token-limited results retain all formatted rows up to 16 MiB and return only
omitted rows to the host's managed output recovery store. The `mread` receipt and
pagination follow the managed read contract without rerunning the resolver or
writing temporary dumps, even after source changes or router shutdown. Display
truncation remains nonzero, and skipped locations still prevent claiming a
complete definition or reference set. Location skip counts cover the complete
resolver result. Exceeding the retention bound or failing to save the snapshot
returns an explicit failure, never a claim of complete reference coverage. `def`
emits every editable definition returned by the resolver in first-seen order and
deduplicates canonical result rows.

Definition expansion occurs only when the resolver's definition selection exactly
matches the declared name of a complete inspect_file outline entry. Supported
package or module declarations, functions, classes, types, variables, and direct
methods emit the entry's inclusive logical-line range. JSON, imports, fields,
parameters, locals, and declarations whose own syntax is uncertain emit only the
definition line. An unrelated parse error elsewhere in the file does not suppress
an error-free declaration's range. Go boundaries use the shared `go/parser` owner.
Token admission never emits a partial result row.

The authenticated frontend owns one AX read observation without retaining command,
path, source, or result content. Msymbol remains excluded from custom-tool routing
. Generated `def` activity is labeled `Read`;
generated `refs` activity is labeled `Search`.

Acceptance:

1. A current use-site token resolves through one language-appropriate semantic
   query and emits compact, complete source rows.
2. Every listed source format is accepted. Omitting `N` selects one unique exact
   language token and rejects an ambiguous line before the resolver starts;
   comments, unrelated literal text, and larger identifiers do not affect the count.
3. `def` expands only supported exact outline declarations; every other valid
   definition emits its one current logical line. Multiple definitions retain
   resolver order and deduplicate rows.
4. `refs` includes declarations, groups by first-seen file and row order, deduplicates one
   canonical path and line, reports skipped locations, and accepts an empty result.
5. Relative and absolute in-workspace paths work. Lexical escapes, escaping
   symlinks, missing resolvers, source changes, malformed protocol results, and
   uneditable definitions fail without useful stdout.
6. The stock executable frontend preserves cwd, environment, resolver cleanup,
   bounded output recovery, AX observation, and `Read`/`Search` activity across
   direct and Code Mode invocation.
7. Explicit workspace selection determines input resolution and resolver cwd
   without changing shell state. Results remain confined to that root and use
   unambiguous absolute paths. Missing prerequisites are actionable without
   automatic installation.

8. Combined path/line operands, qualified selectors, and unique no-line definition
   and reference queries are accepted. Ambiguous and missing-token errors are actionable without
   starting the resolver. A batch starts one server per language, shares its budget,
   preserves tuple order, and rejects changed inputs before emitting rows.
