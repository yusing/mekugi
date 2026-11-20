# File reading

## Requirements

- Provide bounded, batchable UTF-8 source reads through stock Codex execution (`exec_command` / JS
  `exec`), preserving host filesystem permissions, exact continuation for omitted lines, and no
  mutation of source files.

## Current approach (replaceable)

- Exposes the authenticated `mcat` helper through
  [Executable frontends](../../apps/mekugi/internal/frontends/SPEC.md):
  `mcat [-n N] [--max-tokens N] [--tail] [--number] PATH [START:END ...] [PATH [START:END ...] ...]`
- **Selection & bounds**: accepts 1–16 regular UTF-8 file/range reads (`START:END` or `START-END`
  inclusive; `--number` adds `nl -ba`-style line numbers; `-n`/`--tail` apply to single-file reads).
  Default ceiling is 6,000 tokens (max 15,500), admitting complete logical lines only. Incomplete
  reads exit nonzero and persist omitted lines via [Output recovery](../outputrecovery/SPEC.md)
  before emitting a `next_call: mread ...` continuation.
- **Coordinated multi-file batches**: divides the shared budget equally, redistributes unused
  shares from complete files without reopening sources, prepends a compact manifest for
  failed/incomplete/empty inputs, and emits one combined `next_call: mread ...`. Independent source
  failures do not abort other reads.
- Emits an advisory stderr reminder when leading header comments declare a generated file, without
  altering stdout or exit status.
