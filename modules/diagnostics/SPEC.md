# Diagnostics

## Requirements

Inherited from [product requirements](../../docs/product.md#user-requirements): retain private
troubleshooting evidence (request failures, model-visible instructions, detached server logs, and
opt-in debug bundles) offline without affecting execution outcomes or exposing private content in
ordinary output/sanitized metrics.

## Current approach (replaceable)

- **Always-on private records**:
  - Request failures store private references (`failure-<hash>.json`) queryable via
    `mekugi inspect-session --failures [REF]`.
  - Per-thread forwarded instructions/developer messages/tools store content-addressed (RFC 8785 +
    SHA-256) through [Session history](../sessionhistory/SPEC.md) and join into `inspect-session`
    as `instructions` (shared across threads, bounded to 4 MiB per index/revision).
  - Auto-started local servers retain private `process.jsonl` and ≤1 MiB `stderr*.log` under
    `$XDG_STATE_HOME/mekugi/server` (14-day inactive expiry) via a detached supervisor.
- **Opt-in `--debug` bundles**: creates a private (`0700`/`0600`, 14-day inactive expiry) bundle
  under `$XDG_STATE_HOME/mekugi/debug` with unsanitized request diagnostics, instruction dumps,
  optional capture/AX exports, and a post-exit `mekugi inspect-session --debug-dir DIR --field diagnostic`
  hint.
- Offline queries are strictly read-only (≤64 MiB files, ≤32 MiB lines, no mutation or cross-session
  fallback) and fail explicitly on corrupt/missing references.
