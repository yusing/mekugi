# Provider usage reporting

## REQ-USAGE-001 — Per-thread accounting and usage files

### Report delivery

An eligible main completion updates a Markdown token-usage file without adding a
conversation message. Unavailable usage is reported as `n/a` in that file with an
incomplete-usage explanation. Child completion does not update the main report.

When used, TypeSafe AI has a separate provider-reported usage section in the
Markdown report, with per-agent and total rows for model, HTTP attempts, input,
output, missing usage, and cost. Agent-model totals, cache accounting, and mentor state exclude these
auxiliary calls. Only proven descendants join the root's TypeSafe total, and
restart resets consumption just as it does agent-model usage. Retries and
unusable answers retain any known consumption; absent, null, invalid, or partial
usage fields count as missing rather than zero. Overflow makes the affected total
unavailable. No TypeSafe price is configured, so its cost is `n/a`; the report
does not claim net savings from local output-reduction estimates.

The report is a Markdown table in the system temporary directory, using the Codex
thread ID as the stable file identity across router restarts. The wrapper reports
written file paths on exit. No usage line or table is added to the conversation.

The file reports aggregate router-session usage since router startup, not the entire
persisted Codex conversation; restart does not reconstruct prior provider usage.
Partial observations and unavailable pricing stay explicit in the file.

The table has one row per proven agent
and a `Total` row under the same `Router session usage` heading. Columns are `Agent`, `Role`, `Model`, `Input (cache hit)`, `Cache write`,
`Output`, `Reasoning`, `Input cost (cached + uncached)`, `Output cost`, `Total cost`, and
`Missing usage`. Missing usage counts forwarded responses without usable terminal usage, not
missing tokens. Affected agent rows and the total are labeled `partial`; their numbers and
cache-hit percentages cover only observed usage. The report states that missing usage is excluded.
Counts use compact decimal units, such as `149K`, `1.4M`, and `1.2B`. Input includes its
cache-hit percentage; the total percentage is weighted by input tokens, not averaged
across agents. Input cost displays `$a+$b=$c`, with cached cost first and uncached cost
second. Costs are in USD. Child roles come from explicit native spawn arguments matched to
successful spawn results and the child's proven parent and canonical identity. Retained role
evidence survives router restart and does not depend on the parent remaining live. Missing or
conflicting role evidence is `n/a`, never inferred from the agent's name. Ordinary forks
must not reuse inherited spawn evidence to assign roles to their own children.
Model labels append `fast` for effective `fast` or `priority` usage, such as
`gpt-5.6-sol fast`; a provider-reported downgrade to `default` has no suffix.
Model or tier switches retain the distinct observed labels and original per-response pricing.
Intermediate client-tool responses and failed or incomplete responses do not update the file.
Completion eligibility is defined by [REQ-JOURNAL-001](journal.md).

### Accounting and incomplete evidence

JSON and streaming responses use the same cumulative provider-authoritative per-thread
counts. Main combines only proven descendants in its selected workspace with its own row;
unrelated threads and ordinary forks' source trees are excluded. Missing child usage or
unavailable tree evidence must not produce an apparently complete aggregate. A missing main
usage total likewise leaves its row and the aggregate unavailable while retaining known child rows.
Intermediate responses contribute without file updates. Thread accounting remains separate;
compaction and routing-session changes do not reset totals. Repeated terminal observations
within one request count once. Totals remain in memory until router shutdown without a
lifetime thread-count ceiling. Arithmetic overflow makes the affected total unavailable.
An accepted or transport-interrupted request without usable terminal usage increments that
thread's missing-usage count once. Missing or null input, cached-input, output, or reasoning
counts likewise exclude that response from observed totals. Earlier totals, known model labels,
and later usable usage MUST remain available; a later success MUST NOT erase the gap.
A thread with only missing responses has zero observed usage, explicitly labeled partial,
not a claim that those responses consumed zero tokens. Definite HTTP rejections, requests
rejected before forwarding, and non-generating WebSocket prewarm do not create usage gaps.
Failed and incomplete terminal responses with complete usage still contribute
to later totals without producing their own notices.

### Reference pricing

Cost estimates use built-in reference list API prices, not subscription
rates or live billing quotes. No pricing fetch is required; the Markdown file is independent of terminal rendering. Each response is priced using its effective provider-request model, service
tier, and input size before accumulation, so model switches and long-context rates do not reprice
earlier responses. OpenAI long-context rates begin above 272,000 input tokens, not at that exact count. Grok 4.6 long-context rates begin at 200,000 input tokens.
The terminal provider `service_tier` takes precedence over the request, including a downgrade
from `priority` or `fast` to `default`. These two Fast aliases share model-specific reference
rates; a blanket multiplier MUST NOT be applied to every model. When the response omits the tier,
the explicitly requested tier supplies the reference estimate; omission at both boundaries uses
the standard reference estimate. Unresolved `auto`, malformed or null tier evidence, and
unsupported model/tier/context combinations have unavailable cost, not guessed standard pricing.
Rates follow the [official pricing tables](https://developers.openai.com/api/docs/pricing),
[model pricing notes](https://developers.openai.com/api/docs/models/gpt-6-astra), and the
[xAI model prices](https://docs.x.ai/developers/pricing); prefixed and unprefixed Grok IDs
share each model's table. Grok Build Fast uses its own published rates. Grok has no
service-tier Fast or priority reference rates, and unpublished
cache-write rates remain unavailable rather than inferred. Reference estimates
are not proof of the billed processing mode when the provider omits it.

Cached input is subtracted from ordinary input; reasoning is included in output and MUST NOT be
charged again. Optional `cache_write_tokens` are part of uncached input, not additional input
tokens. For models with published cache-write rates, their premium is included in the uncached
input cost cell. An omitted cache-write field is zero for older providers; explicit null,
invalid, or contradictory evidence is not known zero. Unknown model/service-tier pricing
or inconsistent usage makes the affected row's cost cells `n/a` and its aggregate cost
unavailable, without hiding known token counts or presenting a partial cost as complete.
The report explains overlapping token categories, reference pricing, the router-lifetime
boundary, and unavailable estimates. Root accounting is not mutated when rendering the
tree total. This is file-based accounting, not a change to capture-owned metrics exports.

Acceptance:

1. Eligible main completion updates the Markdown per-agent table without conversation
   commentary; child, intermediate, failed, and incomplete responses do not update it.
2. Cache-hit percentages and split input cost reflect observed provider usage. Pricing
   remains per response across model and tier switches, without double-charging cached
   input or reasoning.
3. Missing usage remains explicit after later successes, compaction, and session remapping.
   Unavailable evidence is `n/a`, never inferred zero; restart begins a new accounting window.
4. Tree totals include only proven descendants and leave per-thread counters unchanged.
   TypeSafe consumption remains separate from agent-model usage and Mentor counters.
