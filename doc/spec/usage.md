# Provider usage accounting

## REQ-USAGE-001 — Per-thread accounting and reference costs

### Accounting and incomplete evidence

JSON and streaming responses use the same cumulative provider-authoritative per-thread
counts. The native roster consumes these totals without adding usage messages to the
conversation. Main completion and wrapper exit do not write Markdown usage files.
Thread accounting remains separate from capture-owned [metrics](metrics.md);
compaction and routing-session changes do not reset totals. Repeated terminal observations
within one request count once. Retained totals survive router shutdown and restore
idle completed agents as well as resumed requests, without a lifetime thread-count
ceiling in memory. Managed storage retention may reclaim inactive records.
Arithmetic overflow makes the affected total unavailable.
An accepted or transport-interrupted request without usable terminal usage increments that
thread's missing-usage count once. Missing or null input, cached-input, output, or reasoning
counts likewise exclude that response from observed totals. Earlier totals, known model labels,
and later usable usage MUST remain available; a later success MUST NOT erase the gap.
A thread with only missing responses has zero observed usage, explicitly labeled partial,
not a claim that those responses consumed zero tokens. Definite HTTP rejections, requests
rejected before forwarding, and non-generating WebSocket prewarm do not create usage gaps.
Failed and incomplete terminal responses with complete usage still contribute
to later totals without producing their own notices.

Only positively observed native thread creation establishes a fresh lifetime
baseline. Otherwise the prior window is unknown, even when accounting arrives
before history hydration or without a native UI. Restored counters also remain
lower bounds: persisted counters cannot prove that a later request was retained
before another router stopped. Later observed tokens, cost and roundtrips never
erase this uncertainty. Host context counts and host-normalized usage cannot fill
that window or establish pricing. Diagnostic capture exports are not silently imported into live
accounting. Accounting and roster restoration MUST NOT wait for contention on the
shared publication lock. Pending observations MUST remain available in live totals
and MUST be retained once ordinary contention clears, without repeating provider
requests or counting a response twice. Graceful shutdown MUST attempt to drain
pending publication; cancellation of a waiting caller MUST NOT discard observations.
Storage failure leaves live observations available with a notice; only successfully
retained observations are recoverable after restart. An abrupt stop or exhausted
shutdown wait can leave pending observations unretained.

### Reference pricing

Cost estimates use built-in reference list API prices, not subscription
rates or live billing quotes. No pricing fetch is required. Each response is priced
using its effective provider-request model, service tier, and input size before
accumulation, so model switches and long-context rates do not reprice earlier responses.
OpenAI long-context rates begin above 272,000 input tokens, not at that exact count.
Grok 4.6 long-context rates begin at 200,000 input tokens.
The terminal provider `service_tier` takes precedence over the request, including a downgrade
from `priority` or `fast` to `default`. These two Fast aliases share model-specific reference
rates; a blanket multiplier MUST NOT be applied to every model. When the response omits the tier,
the explicitly requested tier supplies the reference estimate; omission at both boundaries uses
the standard reference estimate. Unresolved `auto`, malformed or null tier evidence, and
unsupported model/tier/context combinations have unavailable cost, not guessed standard pricing.
Rates follow the [official pricing tables](https://developers.openai.com/api/docs/pricing),
[model pricing notes](https://developers.openai.com/api/docs/models/gpt-6-astra),
[GPT-6.1 Sol pricing notes](https://developers.openai.com/api/docs/models/gpt-6.1-sol), and the
[xAI model prices](https://docs.x.ai/developers/pricing); prefixed and unprefixed Grok IDs
share each model's table. Grok Build Fast uses its own published rates. Grok has no
service-tier Fast or priority reference rates, and unpublished
cache-write rates remain unavailable rather than inferred. Reference estimates
are not proof of the billed processing mode when the provider omits it.

GPT-6.1 Sol has standard and Fast reference estimates, including long-context and
cache-write rates. Its cached-input rate is 5% of uncached input, distinct from
GPT-6 Sol's 10% rate.

Cached input is subtracted from ordinary input; reasoning is included in output and MUST NOT be
charged again. Optional `cache_write_tokens` are part of uncached input, not additional input
tokens. For models with published cache-write rates, their premium is included in the uncached
input cost. An omitted cache-write field is zero for older providers; explicit null,
invalid, or contradictory evidence is not known zero. Unknown model/service-tier pricing
or inconsistent usage makes the affected thread's cost unavailable, without hiding known
token counts or presenting a partial cost as complete. Display behavior belongs to
[activity display](activity_display.md).

Acceptance:

1. Main and child completion preserve usage accounting and response delivery without
   writing Markdown usage files or adding usage commentary. Provider answer events
   stream unchanged before terminal usage is available. Auxiliary progress collected
   after a substantive answer stays in terminal history rather than adding a trailing
   completed message.
2. Pricing remains per response across model and tier switches, without double-charging
   cached input or reasoning.
3. Missing usage remains explicit after later successes, compaction, and session remapping.
   Unavailable evidence is never inferred zero; restart restores retained accounting.
4. Per-thread counters exclude other threads, including descendants and fork sources.
5. Idle completed children retain tokens, reference cost and roundtrips after restart;
   a later follow-up adds only its own new observations. Unknown legacy history stays
   incomplete after follow-ups, model switches and subsequent restarts.
6. Contention longer than 250 ms does not block usage consumers, emit a storage
   failure, or permanently disable retention. Queued observations merge across
   concurrent routers with their original pricing, model labels and usage gaps.
