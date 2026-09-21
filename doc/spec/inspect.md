# Shell-routed structural file inspection

## REQ-INSPECT-001 — Shell-routed structural file inspection

The private `inspect_file [--max-tokens N] PATH` command is
available only through the model-visible shell tool. It accepts one shell-separated
path, relative to the process working directory or absolute, like hcat. Parent
paths and symlinks are allowed; the target must be a host-readable regular file.
Codex owns filesystem permissions.

Extension matching is exact and case-sensitive. Supported formats are `.go`, `.py`,
`.pyi`, `.ts`, `.tsx`, `.d.ts`, `.mts`, `.d.mts`, `.cts`, `.d.cts`, `.js`,
`.jsx`, `.mjs`, `.cjs`, `.md`, and `.json`. Every other extension returns
`kind: "none"`, regular-file byte size, `line_count: null`,
`parse_complete: true`, and an empty outline without reading or decoding content.
Supported files must be strict UTF-8, and their logical line count follows
`REQ-READ-001`.

Success is one LF-terminated JSON document with `ok`, `data`, `truncated`, and `truncation`.
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
6901 pointer and value type, including the empty root pointer. Each outline entry's `line` and
`line_end` are `REQ-READ-001` `LINE:HASH` identities for the inclusive span: the positive one-based
logical line and the lowercase four-digit hash of that complete logical line, excluding its
terminator. A single-line span repeats the same identity in both fields. Repeated boundaries within an
inspection MUST reuse the verified identity of that immutable source line rather than rehashing
the complete line for every entry. Those identities are
copyable HPATCH row or `ROW..ROW` range targets. Results contain no raw
excerpts, bodies, fields, comments, frontmatter values, JSON scalar values, or row `TEXT`.

The complete successful stdout, including its final LF, is at most 65,536 UTF-8 bytes and
uses the shared [reader token ceiling](read.md). Options may precede or follow the path.
When necessary, the worker emits a complete outline prefix, exits nonzero, and returns
`truncation: {"reason":"output_bytes"|"output_tokens","after_entries":N}`.
Omitted complete entries are available as JSON arrays through the shared `mread` interface,
without repeating the prefix or reopening the source. Lezer parser recovery or YAML
frontmatter diagnostics set `parse_complete: false` independently of output truncation. There is
no input-size or entry-count limit. If an empty-outline success envelope cannot fit, the command
fails with `output_limit`.

If omitted entries exceed the shared recovery capacity, the current JSON remains available;
stderr explains that recovery is unavailable and no reference is exposed.

Command failures write one closed LF-terminated JSON envelope to stdout, leave stderr empty, and
exit nonzero. Stable codes are `usage`, `not_found`, `not_regular`, `not_utf8`,
`read`, `parse`, and `output_limit`. The centralized Codex guidance and
private call contract embed a concise success, failure, and outline-entry shape rather than the
normative specification schema. Shell replay keeps the original call and output; inspect_file is
not model-visible, directly routed, or included in mekugi recovery ancestry. Passthrough mode
installs and advertises none of these surfaces.

Acceptance:

1. Each default language projection returns only its declared navigation identifiers and exact
   inclusive `LINE:HASH` span identities, while malformed recoverable input remains a successful
   partial result with `parse_complete: false`.
2. Default Markdown inspection excludes fences, Setext headings, nested YAML frontmatter keys, and all frontmatter
   values while preserving source order for repeated top-level scalar keys; JSON escapes `~` and
   `/`, preserves duplicate pointers, and never returns scalar values.
3. Unsupported files are checked as regular without content reads, UTF-8 validation,
   line counting, content detection, or command-level truncation.
4. Private routing, model visibility, replay, and passthrough isolation follow
   [REQ-READ-001](read.md). Instruction and CTP behavior remain owned by
   `REQ-GUIDE-001` and `REQ-CTP-001`.

5. Absolute, parent-relative, and symlink paths outside the current directory work
   when host permissions allow; non-regular files still fail.
