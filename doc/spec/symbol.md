# Shell-routed semantic symbol lookup

## REQ-SYMBOL-001 — Shell-routed semantic symbol lookup

The private `hsymbol` command is available only through the model-visible shell tool:

```text
hsymbol [--workspace ROOT] def PATH (LINE|LINE:HASH) SYMBOL [N]
hsymbol [--workspace ROOT] refs PATH (LINE|LINE:HASH) SYMBOL [N]
```

The canonical workspace defaults to `realpath(process.cwd())`. An optional leading
`--workspace ROOT` selects another existing canonical directory for resolver scope
and relative input paths without changing the caller's working directory. `PATH` may be relative or absolute, but
its canonical target must remain within that workspace and be a regular UTF-8 supported source
file. Supported sources are Go `.go`; Python `.py` and `.pyi`; JSON `.json`; and the stable TypeScript 7
formats `.ts`, `.tsx`, `.d.ts`, `.mts`, `.d.mts`, `.cts`, `.d.cts`, `.js`, `.jsx`, `.mjs`, and
`.cjs`.
`LINE:HASH` identifies one current logical line under `REQ-READ-001`. Hsymbol verifies
the line and hash before starting a resolver and never searches for another matching
hash. A plain positive `LINE` explicitly selects the current file snapshot without
requiring a preceding verified read. It does not assert that previously read content
is unchanged. Both modes select exact language tokens, reject missing/ambiguous
occurrences before resolver startup, and reject input changes during the semantic
query before emitting result rows. Plain-line successes report the selected input
identity on stderr as `hsymbol: input "PATH":LINE:HASH (current snapshot)`.

`SYMBOL` selects an exact language token on the selected current line. Go accepts non-keyword identifiers;
JavaScript and TypeScript accept their identifier, property, private-name, type-name, and JSX-name
tokens; Python accepts identifier and property tokens; JSON accepts a decoded property-name or
string token. Comments, larger identifiers, and unrelated literal text do not count. `N`, when
present, is a positive base-ten occurrence without leading zeroes. When `N` is absent, exactly
one matching token must exist; multiple matches fail as ambiguous before the resolver starts.

Missing-resolver errors identify the executable needed on the executor's PATH,
including TypeScript 7's `tsc --lsp` capability. Hsymbol never installs dependencies,
searches for a different workspace, weakens result confinement, or substitutes text
search. Go and LSP processes both run in the selected workspace.

Each invocation performs one semantic query with the required resolver for the
selected language and workspace. Resolver dependencies are never installed
automatically, and text search is never substituted for a semantic result. The query
deadline is 30 seconds. Final pipe drain and protocol shutdown are bounded to one
second; protocol replies receive a separate one-second dispatch grace after process
exit. A reply completed within those bounds remains valid, and forced cleanup does
not change completed stdout or exit status. Reference queries include declarations.
Missing dependencies, invalid input, stale rows, changed source, malformed protocol
results, timeouts, and failed queries return concise stderr and nonzero status without
useful stdout.

Successful stdout contains first-seen complete verified rows:

```text
"PATH":LINE:HASH TEXT
```

`PATH` is the JSON-quoted path from the default canonical workspace root to the canonical
result file, without a leading `./`. With an explicit `--workspace`, output and selected-input
paths are canonical absolute paths so a different resolver root cannot make references
point at same-named files in the caller's directory. Each result file is canonical, in-workspace, regular, UTF-8, and owned by
the selected resolver; other returned locations are omitted and counted by reason on stderr.
References are deduplicated by canonical path and logical line. Empty `refs` is successful.
A `def` without an editable workspace location is nonzero. Token-limited results retain all
formatted rows up to 16 MiB and return only omitted rows to the host's managed output
recovery store. The `hread` receipt and pagination follow the shell output contract,
without rerunning the resolver or writing temporary dumps, even after source changes or router shutdown. Retained hashes describe the query
snapshot. Display truncation remains nonzero, and skipped locations still prevent claiming a
complete definition or reference set. Location skip counts cover the complete resolver result.
Exceeding the retention bound or failing to save the snapshot returns an explicit failure,
never a claim of complete reference coverage. `def` emits every editable definition returned by the
resolver in first-seen order and deduplicates canonical result rows.

Definition expansion occurs only when the resolver's definition selection exactly matches the
declared name of a complete inspect_file outline entry. Supported package or module declarations,
functions, classes, types, variables, and direct methods emit the entry's inclusive logical-line
range. JSON, imports, fields, parameters, locals, and files with uncertain parsing emit only the
definition line. Hsymbol uses the shared verified-row token admission rule in `REQ-READ-001` and
never emits a partial row.

Shell replay retains the original shell call and output. Hsymbol remains private, is not routed as
a standalone model-visible tool, and never enters editable rejected-script recovery.

Acceptance:

1. A verified use-site token resolves through one language-appropriate semantic query, and emitted
   hashes equal hcat for the same current lines.
2. Every listed source format is accepted. Omitting `N` selects one unique exact language token and
   rejects an ambiguous line before the resolver starts; comments, unrelated literal text, and
   larger identifiers do not affect the count.
3. `def` expands only supported exact outline declarations; every other valid definition emits its
   one current logical line. Multiple definitions retain resolver order and deduplicate rows.
4. `refs` includes declarations, preserves first-seen order, deduplicates one canonical path and
   line, reports skipped locations, and accepts an empty result.
5. Relative and absolute in-workspace paths work. Lexical escapes, escaping symlinks, stale rows,
   missing resolvers, malformed protocol results, and uneditable definitions fail without useful
   stdout.
6. Private routing, replay, passthrough isolation, and recovery exclusion follow
   [REQ-READ-001](read.md); hsymbol adds no model-visible tool or executable
   frontend.

7. Plain-line queries need no pre-acquired hash but retain exact language-token
   selection, ambiguity checks, and post-query source-change rejection. Hash-qualified
   queries continue rejecting stale rows before any resolver starts.
8. Explicit workspace selection determines input resolution and resolver cwd without
   changing shell state. Results remain confined to that root and use unambiguous
   absolute paths. Missing prerequisites are actionable without automatic installation.
