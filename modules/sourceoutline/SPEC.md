# Source outline

## Requirements

Inherited from [product requirements](../../docs/product.md#user-requirements): provide fast,
host-authorized, read-only structural source outlines through stock Codex execution without
requiring external language servers or returning raw source bodies.

## Current approach (replaceable)

- Exposes the authenticated `inspect_file [--json] [--max-tokens N] PATH [PATH ...]` helper through
  [Executable frontends](../../apps/mekugi/internal/frontends/SPEC.md).
- Supports `.go`, `.py`, `.pyi`, `.ts`, `.tsx`, `.d.ts`, `.mts`, `.d.mts`, `.cts`, `.d.cts`,
  `.js`, `.jsx`, `.mjs`, `.cjs`, `.rs`, `.md` (ATX headings + top-level YAML frontmatter keys),
  and `.json` (RFC 6901 pointers + value types); unsupported regular extensions return an empty
  `kind: "none"` summary without reading content.
- Emits compact `START-END KIND NAME` rows by default (collapsed imports, `RECEIVER.NAME` methods,
  no bodies/values) or `--json` envelopes (`parse_complete` flags recovery syntax errors). Bounds
  output to 64 KiB / default 4,000 tokens (max 15,500) and retains omitted entries via
  [Output recovery](../outputrecovery/SPEC.md). Independent path failures do not abort other paths.
