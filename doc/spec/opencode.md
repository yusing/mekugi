# OpenCode Go and Zen provider routes

## REQ-OPENCODE-001 — OpenCode Go and Zen provider routes

OpenCode follows the shared [third-party native-agent routing contract](third_party.md).

OpenCode Go and compatible Zen models are available to main agents and native
subagents under `opencode-go:<model>` and `opencode-zen:<model>`. Available IDs come
from each service's public `/models` endpoint. Model descriptions, API format bindings,
reasoning capabilities and USD-per-million-token prices come from
`https://models.opencode.ai/api.json`, the catalog used by OpenCode itself.
Descriptions are passed through from upstream without Mekugi-written additions;
missing descriptions remain empty. Per-model SDK bindings override the provider binding: OpenAI-compatible Chat,
Anthropic Messages and OpenAI Responses are supported. Other formats are not
advertised or silently approximated. Only text and supported images are advertised.

Online data replaces the available model set and overrides bootstrap values.
New IDs, changes among supported API formats, and price changes do not require a
Mekugi release. The immutable in-memory snapshot gives fast lookup; a versioned
cache at `mekugi/opencode-models-v2.json` beneath the platform user cache directory
survives restart. Startup and inference selection refresh data older than one hour.
Fresh cache hits make no network requests. Refresh has an eight-second deadline,
cancelable coalescing, and a one-minute failure backoff. Invalid or failed refreshes
retain the last usable snapshot; disk writes use temporary files and atomic rename.
The built-in snapshot is only an offline bootstrap when no valid cache is available.
It embeds the checked-in provider metadata and `/models` responses in
`internal/router/opencode_snapshot`; `go generate ./internal/router` refreshes those
source records without a hand-maintained model or price table. The generator keeps
provider-owned metadata and model records, but omits the `/models` response-time
`created` value so repeated captures of an unchanged catalog are byte-reproducible.

Live legacy aliases missing upstream metadata retain their last known metadata;
unknown API formats are never guessed. Missing context limits and prices remain unknown.

Requests pin format and prices so concurrent refreshes cannot alter in-flight
translation or accounting. Public catalog requests carry no credentials and cannot
redirect; metadata cannot change credential destinations. The Codex picker is a
startup snapshot: restart the wrapper to expose newly discovered models there.
Runtime routing and subsequent request prices refresh without restarting.
OpenCode's prose endpoint documentation currently disagrees with its machine-readable
catalog for some Qwen models; routing follows the online catalog, not a pinned
exception. Authenticated acceptance of those conflicting bindings is not established.

Configuration is read once per invocation from `mekugi/config.toml` beneath the
platform user configuration directory. `[providers.opencode_go]` and
`[providers.opencode_zen]` each accept `api_key`. Missing configuration is allowed;
invalid or unknown settings fail startup without quoting secrets. `OPENCODE_API_KEY`
overrides both file keys, then `OPENCODE_GO_API_KEY` and `OPENCODE_ZEN_API_KEY` override
their respective service. Keys are trimmed; an explicitly empty environment value
disables that service. Nonempty keys enable catalog registration and routing in
Mekugi mode only. No new command-line flag, credential-store write, or Codex config
rewrite is introduced.

Go requests use `https://opencode.ai/zen/go/v1` with `/chat/completions`, `/messages`
or `/responses` according to the model's endpoint. Zen uses the same formats
beneath `https://opencode.ai/zen/v1`. Chat and Responses use the selected
service's Bearer key; Messages uses `x-api-key` and `anthropic-version: 2023-06-01`.
A provider-scoped hash of the stable thread ID is sent as `x-opencode-session`
for routing/cache affinity. It survives router restart without sharing identities
between services or threads; absent thread identity does not borrow another session.
Other Codex account/session headers remain excluded. Redirects are not followed, and there is no
authentication retry.

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
