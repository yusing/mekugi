# Bounded foreground commands

## Requirements

Inherited from [product requirements](../../docs/product.md#user-requirements): provide bounded
foreground command output through stock Codex execution (`exec_command` / JS `exec`), preserving
stock process ownership, stdin, exit status, cancellation, continuation, and batch/parallel use.

## Current approach (replaceable)

- Exposes the authenticated `mrun` helper (`mrun (-n N|--max-tokens N) [--head|--tail] [--] COMMAND [ARG...]`)
  through [Executable frontends](../../apps/mekugi/internal/frontends/SPEC.md).
- Keeps `--tail` by default (`--head` for beginning); `-n` bounds rows per stream, `--max-tokens`
  (cap 15,500) bounds combined output (stderr ≤ half when stdout is nonempty). Bytes outside the
  selected head/tail window are discarded; only overflow of the delivery page (15,500-token cap) is
  retained for `mread` continuation.
- Preserves the child's exit status (invalid options exit 2, missing command exits 127), uses
  private temp files cleaned on return, and creates no private process continuation handle.
