# Executable structural file inspection

## REQ-INSPECT-001 — Executable structural file inspection

The model-private `inspect_file [--json] [--max-tokens N] PATH [PATH ...]` command remains available
through its authenticated session frontend and stock command execution. It accepts shell-separated
paths, relative to the process working directory or absolute, like mcat. Parent
paths and symlinks are allowed; the target must be a host-readable regular file.
Codex owns filesystem permissions.

Extension matching is exact and case-sensitive. Supported formats are `.go`, `.py`,
`.pyi`, `.ts`, `.tsx`, `.d.ts`, `.mts`, `.d.mts`, `.cts`, `.d.cts`, `.js`,
`.jsx`, `.mjs`, `.cjs`, `.md`, and `.json`. Every other extension returns
`kind: "none"`, regular-file byte size, `line_count: null`,
`parse_complete: true`, and an empty outline without reading or decoding content.
Supported files must be strict UTF-8, and their logical line count follows
`REQ-READ-001`.

Default output is LF-terminated `START-END KIND NAME` rows. Imports collapse to one
`START-END import` range; methods use `RECEIVER.NAME`. An empty outline prints
`(no outline)`. Multiple paths have `--- PATH ---` headers and share the output
budget. Control characters in displayed paths and names are JSON-quoted.

With `--json`, single-file success is one LF-terminated JSON document with
`ok`, `data`, `truncated`, and `truncation`; multiple files produce JSONL, one such
document per path.
`data` contains the normalized requested path, kind, language, exact inspected byte size, logical
line count, parser-completeness flag, and a flat source-ordered outline. Code entries include only
imports, top-level constants and variables, types, classes, functions, and direct methods.
Declaration-owned names MUST exclude initializer-local declarations, type parameters, and fields,
including when the enclosing top-level declaration spans multiple lines.
JavaScript and TypeScript side-effect imports use their decoded module string as the name,
including single- and double-quoted literals and ECMAScript escapes and line continuations.
Recovered invalid module strings MUST NOT contribute fabricated names.
Markdown includes only ATX headings outside fences and top-level scalar keys parsed from a closed
initial `---` YAML frontmatter block. JSON includes every recognized value as a depth-first RFC
6901 pointer and value type, including the empty root pointer. Each outline entry's `line` and `line_end` are positive one-based numeric
logical lines for the inclusive span. A single-line span repeats that number.
Syntax errors appear as `parse_error` outline entries named `syntax error` with
their logical row position. Results contain no raw excerpts, bodies, fields, comments, frontmatter values,
JSON scalar values, or source text.

The complete successful stdout, including its final LF, is at most 65,536 UTF-8 bytes and
uses the shared [reader token ceiling](read.md). Options may precede or follow the path.
When necessary, compact output emits a complete row prefix, exits nonzero, and
retains the rest for `mread`. Single-file JSON returns
`truncation: {"reason":"output_bytes"|"output_tokens","after_entries":N}`.
Omitted complete entries are available as JSON arrays through the shared `mread` interface,
without repeating the prefix or reopening the source. Multi-file JSON retains
omitted JSONL documents as an exact byte stream; a continuation may split a JSONL
document. Go uses the host toolchain's `go/parser` grammar through the portable
shared core, including the repository's supported Go syntax. TypeScript uses
Babel's TypeScript grammar, with local Lezer recovery when malformed source
cannot be recovered by Babel. Other code uses Lezer. Parser recovery or YAML
frontmatter diagnostics set `parse_complete: false` independently of output truncation. There is
no input-size or entry-count limit. If an empty-outline success envelope cannot fit, the command
fails with `output_limit`.

If omitted entries exceed the shared recovery capacity, the current output remains available;
stderr explains that recovery is unavailable and no reference is exposed.

Compact failures write concise path-qualified stderr; JSON failures write one
closed LF-terminated envelope to stdout and leave stderr empty. Both exit nonzero,
and a failed path does not suppress other requested paths. Stable codes are `usage`, `not_found`, `not_regular`, `not_utf8`,
`read`, `parse`, and `output_limit`. The centralized Codex guidance and
private call contract describe compact rows and the JSON option without embedding a schema.
Stock execution keeps the original call and output; inspect_file is
not model-visible, shell-routed, or included in mekugi recovery ancestry. Passthrough mode
installs and advertises none of these surfaces.

Acceptance:

1. Each default language projection returns only its declared navigation identifiers and exact
   inclusive numeric line spans, while malformed recoverable input remains a successful
   partial result with `parse_complete: false`.
2. Default Markdown inspection excludes fences, Setext headings, nested YAML frontmatter keys, and all frontmatter
   values while preserving source order for repeated top-level scalar keys; JSON escapes `~` and
   `/`, preserves duplicate pointers, and never returns scalar values.
3. Unsupported files are checked as regular without content reads, UTF-8 validation,
   line counting, content detection, or command-level truncation.
4. Frontend authentication, model visibility, replay, and passthrough isolation follow
   [REQ-READ-001](read.md). Instruction and executable-frontend behavior remain owned by
   `REQ-GUIDE-001` and `REQ-PLUGIN-001`.

5. Absolute, parent-relative, and symlink paths outside the current directory work
   when host permissions allow; non-regular files still fail.
6. Compact, JSON, and multi-path output have exact-output coverage. Compact output
   for `plugins/msymbol.ts` uses at least 70% fewer tokens than JSON.
7. Tracked Go and plugin TypeScript sources have complete declaration spans;
   an unrelated syntax error does not prevent expansion of an error-free declaration.
