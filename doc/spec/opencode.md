# OpenCode Go and Zen provider routes

## REQ-OPENCODE-001 — OpenCode Go and Zen provider routes

OpenCode follows the shared [third-party native-agent routing contract](third_party.md).

OpenCode Go and compatible Zen models use `opencode-go:<model>` and
`opencode-zen:<model>`. Their [provider specification](../../providers/opencode/SPEC.md)
owns service configuration, key precedence, catalog discovery, cached metadata,
request pinning, refresh behavior, authenticated endpoints and session affinity.

Cost estimates use the request-selected online rates, including cache reads,
cache writes and explicit context-tier thresholds. Explicit zero rates are free;
missing rates, unknown tier types and unsupported service tiers remain unknown.
Explicit context tiers take precedence over the legacy 200k price field. Estimates
are reference API prices, not Go subscription charges or billing quotes.

Both OpenCode routes reuse the private catalog bootstrap, native execution metadata, plaintext
collaboration projection, full-history HTTP transport, non-generating prewarm handling, tool
validation, cancellation, and stream-failure rules in the
[shared third-party contract](third_party.md).
Disabled provider entries are removed from a reused private catalog. Configured provider
models and fresh-context requirements are included in the native spawn description.

Reasoning effort choices come from each model's online `reasoning_options` effort
values. The native catalog exposes those choices without inventing a default.
Supported explicit efforts pass unchanged as Chat `reasoning_effort`, Responses
`reasoning.effort`, or Messages `output_config.effort`. Reasoning effort and
structured output may be requested together; neither setting replaces the other.
An absent effort, or an inherited value the selected model does not support, leaves
provider defaults alone; users never need to clear a Codex setting manually. `none` is forwarded only when
the provider advertises it, not used as a universal sentinel. Toggle-only and
token-budget-only controls are not presented as effort settings. Reasoning
received as `delta.reasoning_content` is retained as plaintext Responses reasoning
summary items and restored on the corresponding assistant message on subsequent
requests, including turns without tools and resumed histories. It must never be
discarded like optional OpenAI summaries. Messages thinking/signature/redacted blocks
and Responses reasoning items are carried in versioned, service/model-bound opaque
replay envelopes, then restored only on that route. Foreign encrypted history remains
a local compatibility error and directs the caller to fresh context.
Visible reasoning streams on every format as it arrives: Chat `reasoning_content`,
Messages `thinking_delta`, and Responses summary or reasoning-text deltas (or, without
deltas, the completed item's summary or text). It uses the shared provider streaming
lifecycle, so an item streamed before the answer precedes that message in history and
replay attaches it to the assistant message that follows. On Messages and Responses
routes the visible text is the summary of the same item that carries the opaque replay
envelope; a visible block that closes without one keeps no summary, because those
routes cannot replay summary-only reasoning.

Unlike xAI, OpenCode's `completion_tokens` already includes reasoning. Responses output
usage retains that total unchanged and exposes reasoning only as a breakdown. Missing
provider usage remains missing. Messages input totals include uncached input, cache
reads and cache writes; output totals are already inclusive of thinking. Messages
requires a generation ceiling, defaulting to 32,768 tokens; an explicit Responses
total-output budget maps to `max_tokens`. Responses routes preserve the requested
total-output budget. Chat routes still reject incompatible total-output budgets.

All formats reuse one tool-identity and complete-argument validator. Messages
projects tool schemas as `input_schema` and native calls as `tool_use`/`tool_result`;
Responses uses flat function schemas and calls. Custom tool input is carried in a
single function parameter and restored exactly before Codex executes it. Signed
reasoning and call IDs survive JSON history persistence. Refusal output remains a
normal Responses refusal lifecycle, not a transport failure. Missing endpoint
terminals or unfinished calls cannot release executable tools. Provider transport
capture measures actual endpoint bytes and complete model-origin output, including
opaque reasoning, separately from user-visible text.

Additional acceptance:

1. Environment-only, config-only, mixed-service, empty override, invalid-config and
   passthrough cases preserve documented precedence and secret-free errors.
2. Each configured service appears in the private catalog without exposing keys;
   unconfigured or unsupported routes make no provider call.
3. JSON and SSE clients on all three Go endpoints preserve tools, signed/plaintext
   reasoning replay, refusals, provider model aliases and usage without double-counting
   reasoning. The bootstrap public Go model list has a route for every ID.
4. WebSocket prewarm does not generate inference; continuation sends complete history
   over HTTP, and disconnect cancels provider work.
5. Online additions, removals, format changes, effort choices and prices supersede
   bootstrap metadata; fresh caches perform no network I/O, failed refreshes preserve
   usable data, canceled refresh waiters return promptly, and in-flight prices survive
   model removal.
6. OpenAI and Grok behavior remains unchanged when OpenCode is not configured.
