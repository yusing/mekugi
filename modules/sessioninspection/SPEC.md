# Session inspection

## Requirements

Inherited from [product requirements](../../docs/product.md#user-requirements): provide read-only
offline inspection of session rollouts, retained evidence, and multi-session friction findings
without starting Codex, mutating stored state, or inferring unrecorded outcomes.

## Current approach (replaceable)

- Exposed via `mekugi inspect-session` (`--session PATH` or `--thread ID`, schema `mekugi.session.v1`)
  and `mekugi inspect-sessions` (corpus scan, schema `mekugi.sessions.v1`), dispatched by
  `apps/mekugi/internal/inspectioncli`.
- Joins decoded rollout calls ([Codex backend](../../backends/codex/SPEC.md#retained-rollout-evidence))
  with validated retained change/shell/instruction records strictly by matching call and workspace
  identity (never falling back to inspector cwd or scanning other workspaces). Distinguishes
  `applied`, `no_changes`, `already_satisfied`, `rejected`, `unconfirmed`, and `unavailable`.
- Supports call filtering, pagination (`--limit` 1–500, default 50), explicit field/text selection
  (`--field`, `--text-bytes`), optional [AX evidence](../agentmetrics/SPEC.md) +
  [retained request timing](../transportdiagnostics/SPEC.md), and static corpus findings (empty
  polls, truncation/rereads, optional provider usage; bounded to 10,000 files, default last 48h).
- Strictly read-only: exits 0 on success, 2 on invalid options, 1 on read/validation failure.
