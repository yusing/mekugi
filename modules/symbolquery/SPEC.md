# Symbol query

## Requirements

Inherited from [product requirements](../../docs/product.md#user-requirements): provide workspace-confined,
read-only semantic definition and reference queries through stock Codex execution without installing
dependencies, executing project build scripts, or falling back to text search.

## Current approach (replaceable)

- Exposes the authenticated `msymbol [--max-tokens N] [--workspace ROOT] (def|refs) PATH [LINE] SYMBOL [N]`
  helper through [Executable frontends](../../apps/mekugi/internal/frontends/SPEC.md), supporting
  batched tuples that share one per-language LSP session (30s shared deadline; no background daemon).
- **Supported languages & prerequisites**: Go (`gopls`, with single-tag `//go:build` retry),
  JS/TS/JSON (TypeScript 7 `tsc --lsp`), Python (`pyright-langserver`), and Rust Cargo projects
  (`rust-analyzer` + installed toolchain + `rust-src` + offline `Cargo.lock`). Missing resolvers
  fail with actionable errors and never auto-install.
- **Rust Cargo analysis**: runs strictly offline/locked with build scripts, procedural macros, and
  `checkOnSave` disabled (`RUSTUP` blocked from installing components); active graphs with disabled
  build scripts/proc macros still return available rows alongside an explicit nonzero incomplete-
  coverage diagnostic.
- **Output & bounds**: `def` expands unique complete outline declarations; batched `refs` emits
  tuple labels plus one deduplicated canonical source inventory confined to the selected workspace.
  Default budget is 4,000 tokens (max 15,500), with omitted rows retained via
  [Output recovery](../outputrecovery/SPEC.md). Independent tuple failures preserve valid results.
- See [Agent tools guide](../../docs/guide/agent-tools.md#rust-semantic-queries) for Cargo cache
  preparation.
