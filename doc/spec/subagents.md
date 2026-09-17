# Third-party native-agent routing

## REQ-SUBAGENTS-001 — Third-party native-agent routing

### Grok

`--grok` enables `grok:grok-4.6` in `mekugi` mode. Passthrough mode rejects the flag.
Without it, the existing model catalog and OpenAI routing remain unchanged;
a `grok:` request fails locally rather than sending it to the OpenAI provider.

The wrapper adds a Grok model to Codex's selected catalog, retaining the catalog's
native v2 instruction and executor metadata. The entry advertises text/image input, the 500,000-token
context window, and low/medium/high/xhigh reasoning (high by default). It does not inherit OpenAI's
Responses Lite transport, hosted search, service tiers or upgrade schedule. Existing catalog entries
remain unchanged. A catalog without a native v2 template fails explicitly. An existing cached
Grok entry is rebuilt from that template rather than duplicated. With `--grok`, the wrapper runs
`codex debug models` with the invocation's configuration and working-directory selectors,
then writes a private session catalog and enforces its `model_catalog_json` path in the final
invocation-only config layer. Named Codex profiles (`--profile`/`-p`) and `exec --ignore-user-config`
are rejected with `--grok` before catalog loading because the catalog command cannot honor those
configuration modes. The default configuration and explicit
`-c model_catalog_json` remain supported. The catalog is fixed for the session and cannot be replaced by
another Codex process's shared cache. Thread switching, spawning, and follow-ups retain the same
Grok tool metadata. Catalog command failure, invalid output, or private-file creation failure
prevents the main Codex launch. Output is bounded to 8 MiB and catalog preparation to one minute.
Preparation emits content-free stderr progress immediately and every ten seconds while waiting.
Interactive status is width-bounded and cleared before terminal handoff; redirected output uses
complete lines. Rendering failures stop progress only, never cancel preparation or replace its
result. Cancellation and deadline errors take precedence over generic subprocess/configuration
advice, and private bootstrap stderr is not exposed.
The private directory is mode 0700, its file is mode 0600, and both are removed on exit or launch
failure. User configuration and the shared cache are not rewritten by Mekugi; Codex retains its
normal catalog-command behavior. `/v1/models` forwards the upstream catalog unchanged, without
Grok injection or a synthetic ETag.

Both main-agent and native child requests may use the Grok route. Child-specific headers and
metadata are not required for routing. Codex owns spawning, listing, messaging,
waiting, interruption, follow-up, tool execution, permissions and sandboxing. The router never
creates a substitute agent process or executes a tool itself.

In ordinary Mekugi turns, the provider-visible collaboration namespace is
`mekugi_collaboration`, independently of whether Grok is enabled. Its message
schema has no OpenAI encryption annotation. Response calls are restored to the original native
`collaboration` namespace; spawn, send-message and follow-up calls carry the explicitly empty
`encrypted_function_args` marker required by Codex for plaintext delivery. Replay consistently maps
bridge-produced native calls back to their provider-visible identities. Historical calls with
nonempty encryption markers retain their native namespace and encryption metadata. Both ordinary and additional-tool catalogs
are covered. When this namespace is exposed, the complete pinned native dispatch instruction in
top-level and developer-message text uses the same namespace, including multipart content.
Caller wording, fenced examples, and user messages remain unchanged.
The reserved OpenAI collaboration schema is not modified in place, because the provider
rejects that operation. User-visible agent lifecycle remains native.

Plaintext projection makes new assignment payloads readable to the router, allowing journal
answer association. It does not decrypt existing encrypted agent messages; a new plaintext
follow-up is required to attach those tasks. Ordinary passthrough, prewarm, and auxiliary turns
without Grok remain unchanged.

When Grok is enabled, the projected spawn catalog places Grok's fresh-context and reasoning requirements beside
existing `model`, `fork_turns`, and `reasoning_effort` arguments, retaining their native
descriptions. It does not add absent arguments, change defaults or validation constraints,
or relax role restrictions. The caller still explicitly selects fresh context and supplies
a self-contained assignment; projection never changes submitted arguments.

Grok uses streaming Chat Completions, either through the public xAI API with `XAI_API_KEY`, or through
the Grok CLI chat proxy with the existing Grok OAuth credential store. An API key takes precedence.
`--grok-auth-file` selects a different credential file; otherwise the router uses the current user's
Grok OAuth store. This route supports the standard `https://auth.x.ai` Grok public client, not custom
enterprise issuers. Grok owns interactive login; Mekugi refreshes expired/near-expiry OAuth tokens
and retries one rejected access token. Refresh uses Grok's cross-process advisory lock, re-reads
credentials under that lock, and atomically saves rotated credentials while preserving unrelated
accounts and fields. No credentials, provider error bodies, or prompts enter sanitized diagnostics.
Codex credentials and internal account/thread headers are never forwarded to Grok, and Grok credentials
never reach OpenAI. Credential-bearing requests do not follow redirects.

The adapter preserves supported text/image messages, plaintext agent messages, custom/function tool
calls and their identities/results, parallel calls, structured output, reasoning effort and usage.
Multipart text retains separate content parts so independent CTP dictionaries and visible-output
source identities do not merge. Empty text arrays retain empty-string content.
Custom tool grammars remain explicit input instructions and are still validated by their existing
router/executor owners. Provider-hosted OpenAI search is not offered on the Grok route; the model is
informed of its absence. Other unsupported provider tools/content fail explicitly rather than being
silently approximated. A non-null `max_output_tokens` fails before inference: Chat completion
limits exclude reasoning and cannot enforce the Responses total output budget. Encrypted agent messages, encrypted reasoning, opaque provider file IDs and
provider-only history items cannot be translated. Fresh-context spawning (`fork_turns=none`) avoids
inherited OpenAI encrypted history; unsupported history must fail without a provider request.
Encrypted-history rejections are local compatibility errors (HTTP/WebSocket status 400),
not retryable transport failures, and direct the caller to a fresh context.

Provider wire names use stable aliases where needed for namespaces or provider-reserved names.
In particular, Codex's `wait` tool must not be sent under the bare name `wait`: the Grok proxy
can finish with `tool_calls` while omitting that call. Catalog entries, explicit tool choice,
and replayed calls use the same alias; Responses events retain Codex's original name and arguments.

Code Mode `wait`'s `yield_time_ms` and `max_tokens` are advertised to Grok as integers,
matching Codex's native handler. This prevents the provider from serializing them as
floating-point values that Codex rejects; submitted argument bytes are not rewritten.

Text and content-free progress stream while complete tool arguments are buffered and validated.
Validated calls emit the Responses tool lifecycle in order: item added, input or arguments done,
then item done, with stable item/call identities and output indexes. Function argument-completion
events include the restored function name.
The first terminal choice seals its content and calls. Later choice data or conflicting terminal
reasons reject the stream; usage-only trailers and the final stream marker remain valid.
Truncated streams and malformed/unknown tool calls never become successful executable results.
For streaming clients, validation and provider-read failures emit a failed Responses terminal with a stable,
content-free diagnostic code and a router-owned explanation, while retaining the underlying
failure for request accounting. Provider error bodies never enter that explanation. Consumer
write failures do not attempt another terminal write.
JSON clients receive the equivalent successful terminal Responses object; producer failures
return a request error instead. Cancellation and stream inactivity
limits propagate to the upstream HTTP request; request start and execution have distinct lifetimes.
Metrics measure the actual Chat Completions transport and provider usage, not estimated usage from
synthesized Responses events. Chat completion and reasoning counts are combined into Responses
output tokens, retaining reasoning as a breakdown rather than counting it twice. Provider-returned model metadata is not rewritten to disguise aliases.

Acceptance:

1. Disabled routing preserves existing OpenAI behavior; enabled catalog registration allows native
   model validation without changing Codex configuration files or launching another agent runtime.
2. JSON/SSE projection and replay preserve native agent call IDs and plaintext handoffs, including
   messages sent while a child runs and follow-ups after it completes or is interrupted.
3. A Grok-generated custom/function tool call is executed once by Codex, replayed with its result,
   and followed by a native child final answer.
4. Image content, parallel tool results, structured output and supported reasoning settings survive
   conversion; unsupported/encrypted data fails before inference.
5. Fresh credentials, concurrent refresh, token rotation, untrusted discovery, redirects and rejected
   tokens preserve credential separation and the shared credential store.
6. Downstream cancellation aborts upstream work; missing terminal markers, partial tool arguments,
   provider errors and idle timeouts cannot be reported as completed responses.
7. Provider transport, usage, final output and tool-call metrics remain correlated and sanitized.
8. Ordinary and additional-tool spawn catalogs retain native role, context, permission,
   required-field, and validation constraints while exposing Grok requirements at the
   relevant existing arguments. JSON/SSE restoration preserves submitted arguments exactly.

### OpenCode Go and Zen

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
Other Codex account/session headers remain excluded. Redirects are not followed,
there is no authentication retry. Rejected inference requests on the Grok and OpenCode
routes preserve the provider's HTTP status and error details in HTTP/WebSocket errors
and user-facing failure notices, rather than replacing them with authentication advice.
Error reads are limited to 8 KiB and five seconds; unreadable or oversized bodies get an
explicit explanation. JSON error messages retain their name, type, or code when present;
other bodies use bounded text. Display text is limited to 2,048 characters, strips terminal
controls, and redacts credentials used on the request. Sanitized diagnostics and metrics
retain only the HTTP classification, never the provider's error text.
The ordinary Codex authentication boundary still applies to incoming requests.

Cost estimates use the request-selected online rates, including cache reads,
cache writes and explicit context-tier thresholds. Explicit zero rates are free;
missing rates, unknown tier types and unsupported service tiers remain unknown.
Explicit context tiers take precedence over the legacy 200k price field. Estimates
are reference API prices, not Go subscription charges or billing quotes.

Both routes reuse the Grok catalog bootstrap, native execution metadata, plaintext
collaboration projection, full-history HTTP transport, non-generating prewarm handling,
tool validation, cancellation, and stream-failure contracts above. The catalog bootstrap
restrictions on profiles and ignore-user-config apply whenever OpenCode is enabled.
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
