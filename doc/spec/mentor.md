# Main and subagent Mentor Handoff

## REQ-MENTOR-001 — Main and subagent Mentor Handoff

Mentor Handoff is a `mekugi`-mode product schedule. Subagent handoff is enabled by default
and disabled with `--mentor-handoff=false`. Main handoff is independently controlled by
`--main-mentor-handoff`, which defaults to `true`. Passthrough mode keeps both off and
rejects an explicit enable of either. Its
eligible requests are main-session turns (including ordinary forks) with valid Codex metadata,
`request_kind: turn`, no subagent header, no `subagent_kind`, and a Codex thread ID; or AgentControl thread spawns
carrying exactly one `x-openai-subagent: collab_spawn` header and valid Codex turn metadata whose
`subagent_kind` is `thread_spawn`. Spawned requests require a Codex thread ID.
The router forwards the subagent header unchanged. It does not infer a subagent from lineage
or instructions. Requests outside these boundaries remain unchanged.

Main prewarm and compaction requests remain unchanged and do not start or consume the
main handoff schedule.

For an eligible main request configured with `gpt-5.6-luna`, the mentor is
`gpt-6-astra` with `medium` reasoning, regardless of the configured effort.
Subagent mappings remain unchanged: `gpt-5.6-luna` and `gpt-5.6-terra` use
`gpt-5.6-sol` with `high` reasoning. Main `gpt-5.6-terra` requests also retain that mapping.
For exactly `gpt-5.6` or `gpt-5.6-sol`, the mentor is `gpt-6-astra` with one lower reasoning level,
floored at `low` and capped at `xhigh`: `low` and `medium` map to `low`, `high` to
`medium`, `xhigh` to `high`, and `max` and `ultra` to `xhigh`. Missing or unrecognized
effort uses `low`. Other configured models, including `gpt-6-astra`,
remain unchanged. The router replaces only the top-level model and reasoning effort,
preserving other reasoning members, input history, tools, metadata, and request fields.
This happens before Mekugi projection, CTP preparation, provider serialization, and transport capture. Codex continues to construct later requests from its session
settings; the router never rewrites response model metadata.

The router retains one bounded schedule per thread. Completed provider responses contribute
one count for each `custom_tool_call` or `function_call` output item and each assistant `message`
output item. Streaming counts `response.output_item.done`; a terminal response output is only a
fallback when no done item was observed. Provider-reported `input_tokens` contribute on completed
and failed delivery paths whenever usage was observed. Tool calls and messages contribute only for
a completed terminal response.

After the mentor reaches three tool calls, it remains active for one more completed response so it
consumes the third call's result. A failed response in that position retains the mentor unless the
input budget is reached. The handoff completes after that result-consuming response, after two
assistant messages, or when the latest request reports at least 50,000 input tokens. A request's
input count already includes its inherited conversation history, so counts from separate requests
are never summed. The completed request may overshoot the token limit.
The next explicit request from that thread uses the model and reasoning supplied by Codex without a compatibility
rewrite. Thread schedules are retained for the router lifetime so a completed schedule is never
silently forgotten and restarted. State and progress logs retain counts and identifiers only, not
prompt or response content. The capturer attributes each request to the model actually sent upstream.

An automatic WebSocket steering successor does not send a new provider request. It retains its
parent's effective provider model and reasoning, even when the parent terminal completes Mentor's
schedule. Translation and completion-cost accounting use that retained contract, not a model
switch that was never sent. Successor usage and completed output still contribute to an active
Mentor schedule. The eventual explicit request applies the completed schedule normally.

When a schedule completes, the router queues user-only commentary announcing the handoff.
The next eligible response for that thread delivers it through the existing bounded runtime
commentary path, with child attribution where applicable. It is emitted once and removed
from later provider-bound history using retained provenance. An absent response or exhausted
auxiliary capacity cannot block the handoff or create another model turn.
