# Router-owned main and subagent model schedule

## CTR-MENTOR-001 — Router-owned main and subagent model schedule

The router owns the optional Mentor Handoff schedule, its per-thread router-lifetime
state, and invocation-local changes to provider-bound model and reasoning effort. It
identifies eligible main and child turns from validated request metadata. The ordinary
request owner still supplies history, tools, and session settings; Codex remains the
owner of the configured model after handoff.

A request-scoped terminal observer supplies completed output and the latest
provider-authoritative usage before any automatic journal continuation. The schedule
does not sum unrelated requests, parse transformed carriers, or share state with edit
recovery and provider transport retries. Metrics and user-visible usage consume the same
terminal facts through their own owners.
