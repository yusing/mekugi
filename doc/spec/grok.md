# Grok provider route

## REQ-GROK-001 — Grok provider route

Grok follows the shared [third-party native-agent routing contract](third_party.md).

`mekugi grok [Codex arguments...]` selects a Grok-only session with plain model
IDs: `grok-4.5`, `grok-4.6`, `grok-4.7`, and `grok-4.7-build-fast`.
Standalone `mekugi` enables Grok when its credentials are usable, retaining the
`grok:` model prefix alongside configured OpenCode routes. The old `--grok` flag
is rejected. Both launch modes require `mekugi` response mode and no Codex login.
`mekugi codex` preserves OpenAI behavior and rejects Grok requests locally.

An explicit invocation model wins. Otherwise Grok's default follows
`[models].default` in `~/.grok/config.toml`, falling back to the latest standard
model (`grok-4.7`) if absent or empty. Standalone adds the `grok:` namespace to
this selection; dedicated Grok uses plain IDs. An unreadable or malformed Grok
config fails with actionable advice unless the invocation selects a model.
Automatic startup defaults are not resume overrides: saved model/provider choices
survive startup resume and in-session session switching unless the caller explicitly
selects a model.

The wrapper builds Grok models from Codex's bundled catalog, retaining its native v2
instruction and executor metadata. The entry advertises text/image input, the 500,000-token context
window, and low/medium/high/xhigh reasoning (high by default). It does not inherit OpenAI's Responses
Lite transport, hosted search, service tiers, or upgrade schedule. OpenAI catalog entries are
excluded from third-party-only sessions. Existing third-party entries are rebuilt
rather than duplicated. Wrong-mode Grok namespaces fail locally before inference.

When Grok is enabled, the projected spawn catalog places Grok's fresh-context and reasoning
requirements beside
existing `model`, `fork_turns`, and `reasoning_effort` arguments, retaining their native
descriptions. It does not add absent arguments, change defaults or validation constraints,
or relax role restrictions. The caller still explicitly selects fresh context and supplies
a self-contained assignment; projection never changes submitted arguments.

Grok uses streaming Chat Completions, either through the public xAI API with `XAI_API_KEY`, or through
the Grok CLI chat proxy with the existing Grok OAuth credential store. An API key takes precedence.
`grok-4.7-build-fast` is available only through the OAuth proxy, not the public API;
an API-key request for it fails locally. The selected model is sent as the proxy model override.
`--grok-auth-file` selects a different credential file; otherwise the router uses the current user's
Grok OAuth store. This route supports the standard `https://auth.x.ai` Grok public client, not custom
enterprise issuers. Grok owns interactive login; Mekugi refreshes expired/near-expiry OAuth tokens
and retries one rejected access token. Refresh uses Grok's cross-process advisory lock, re-reads
credentials under that lock, and atomically saves rotated credentials while preserving unrelated
accounts and fields. No credentials, provider error bodies, or prompts enter sanitized capture or
metrics; diagnostic error strings can contain them.
Grok inference does not require Codex Authorization or account headers, including
HTTP and WebSocket clients. Codex credentials and internal account/thread headers are never forwarded to Grok, and Grok credentials
never reach OpenAI. Credential-bearing requests do not follow redirects.

The adapter preserves supported text/image messages, plaintext agent messages, custom/function tool
calls and their identities/results, parallel calls, structured output, reasoning effort and usage.
Multipart text retains separate content parts so visible-output source identities do not merge.
Empty text arrays retain empty-string content.
Custom tool grammars remain explicit input instructions and are still validated by their existing
router/executor owners. The Code Mode `exec` description also states that only `text(...)` output
reaches the model, so a nested tool's result must be printed, and that its input is JavaScript,
not shell; Codex models need neither note. The OpenCode route shares this translation.
Provider-hosted OpenAI search is not offered on the Grok route; the model is
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
Plaintext `delta.reasoning_content` streams as a Responses reasoning summary item: it is added
before its first delta and completes before the answer text starts, so reasoning precedes the
message in output order. Reasoning received after text has started completes after the message
instead of interleaving items. Replay ignores these unencrypted summaries; they never reach the
provider again.
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

1. Dedicated and standalone launches work without Codex login, preserve their distinct
   model namespaces, and select the configured/default model without changing user config.
   Disabled routing preserves existing OpenAI behavior. Catalog registration allows native
   model validation without launching another agent runtime.
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
