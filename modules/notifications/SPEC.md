# Notifications

## Requirements

Inherited from [product requirements](../../docs/product.md#user-requirements): surface actionable
failure and progress notices (and generic desktop wake-up alerts) reliably without dropping unseen
errors, duplicating alerts, leaking private content into desktop notifications, or affecting
execution outcomes.

## Current approach (replaceable)

- Invocation-scoped store (bounded to 256 notices and 256 recent turn outcomes) holds pending
  notices until a visible renderer explicitly acknowledges painting the affected surface (error
  banner vs transcript progress row); unacknowledged notices survive detach/exit for post-exit
  launcher printing.
- Request failures wait while their host turn can retry (discarded if the turn succeeds or is
  interrupted); non-error feedback auto-expires (3s), while error feedback persists until dismissed
  or superseded by a successful model response on that thread.
- Evaluates desktop notification eligibility from Codex `tui.notifications` policy (unfocused-only
  by default; coalesced while paused; generic text only, never prompts/secrets/tool content).
