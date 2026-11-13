# Request execution

## Required execution guarantees

Preserve stock Codex editing, execution and JavaScript interfaces, including their
authority, inputs and results. Independent batched or parallel stock operations
remain independent; observation and request projection never replay their effects.

## Responsibility and current lifecycle

The application runs one request lifecycle for HTTP and WebSocket exchanges.
The inference gateway owns transport and provider selection; Codex owns execution.
The executor orders preparation, provider admission, transformation and delivery
without retaining a second transcript, agent registry or accounting ledger.

## Replaceable request policy

Preparation applies authenticated guidance around execution admission and file
attachment projection. Non-generating WebSocket prewarm cannot initialize live
execution, retained history or agent lifecycle. Codex compaction uses native
summary-free reset without a provider request. [Agent guidance](../../../../modules/agentguidance/SPEC.md#compaction-context)
owns selected-source recovery and historical summary compatibility. The executor
neither generates nor publishes compaction summaries.

Temporary `/btw` answers use low reasoning when the selected model advertises it.
Explicit lower efforts remain lower; an unset effort uses the catalog's advertised
default to decide whether to reduce it. Unknown defaults and models without an
advertised low choice stay unchanged. This request-local policy preserves Main,
Side and One-off settings. On OpenAI models advertising reasoning updates,
preparation appends a low-effort configuration update while retaining the request
baseline and earlier history.

Codex-owned automatic approval reviewers retain their native instructions, tools
and output schema. They use ordinary provider delivery and accounting without
entering the managed-agent tree or receiving Mekugi execution guidance. Codex
continues to own review decisions, reviewer permissions and action execution.
Inherited Mekugi compaction references expand to their exact retained summaries
under the reviewer's authenticated source scope, preserving existing retained
bindings and frozen fork authority. This restoration neither runs
reload readers nor publishes reviewer history. Missing, corrupt or unauthorized
local references stop forwarding; provider-owned opaque compaction and other
native review context remain unchanged.

## Initial input prefix

The initial instruction layer is frozen after request projections, before provider
history reconciliation. It includes base instructions and leading instruction/tool
catalog items before the first user or conversation item. Later feature contributions
cannot rewrite that applicable prefix. Conversation and tool-result history remain
normally projected and subject to exact provider-confirmation checks.

Initial context contributions are acquired once against the admitted workspace.
Prewarm neither acquires them nor freezes a generating context. Confirmed same-workspace
full-history children, Side and One-off forks inherit immutable contributions, including
across independent native runtimes; rewind preserves the applicable context without
preserving discarded conversation. Fresh start, resume, clear, compaction and workspace
changes acquire a new prefix. Native permission, role, instruction and model changes
remain authoritative and establish a newly applicable instruction layer, without
reacquiring unchanged context contributions. History-reset identity remains independent.

Only the initial layer is retained, not another conversation transcript. Frozen values
are context-lifetime data, not provider confirmation or execution authority. Successful
provider terminals and exact reconciliation still determine continuation reuse.

## Replaceable integration design

[Execution observation](../executionobservation/SPEC.md) supplies the prepared
transformer, usage observation and cleanup lifetime. It admits review identity
before forwarding and rebinds to the execution lifetime after provider admission
while preserving storage identity and turn leases. Invocation startup constructs
and closes its actual owners; request cleanup closes only that response's
observations.

## Current delivery and failure behavior

Native coordination checkpoints logical waits at actual execution settlement.
Request Execution neither creates an empty provider response nor owns logical
work, stop eligibility, wait timers or recovery. Native owns
[provider timing and capacity backoff](../../../../docs/contracts/coordination.md#logical-waits-and-ordered-delivery);
provider execution retains ordinary transport/rejection evidence and accounting.

Raw provider usage is observed before response projection. Definite provider
rejections do not charge inferred execution; uncertain transport failures retain
the existing accounting distinction. Pinned provider prices survive catalog
refreshes. Naming admission follows a successfully delivered completed response. The
auxiliary naming adapter captures only provider authentication, validates terminal
completion and attributes usage to the originating thread. It carries no Main
execution or steering identity; optional naming policy stays with its module.

Non-stream responses are staged and terminal-validated before headers or delivery
confirmation. Streams confirm only successful writes and flushes. Partial or
failed transformation cannot become successful execution evidence. Cancellation,
response-start timeout, stream inactivity and downstream disconnection retain
their distinct diagnostics; missing terminal state remains a failure.

Retained-publication diagnostics share one classification across request and native
session consumers. Conflicting host facts, storage capacity and storage I/O remain
distinct; classification never retries publication or execution.

Catalog transport failures use the same private classification and recording
owner without becoming inference failures or terminal notices.

Private failure records, [retained instructions](../../../../modules/diagnostics/SPEC.md#retained-instructions),
optional notices and debug bundles remain independent consumers. Retained
instructions observe the same final projection as debug instruction dumps. Sanitized stream metrics contain classification, not prompt or error
content. Debug instructions distinguish local projection from the actual wire
subset. No diagnostic operation retries host execution or provider inference.
Preparation and outcome hooks also feed
[retained transport timing](../../../../modules/transportdiagnostics/SPEC.md#retained-request-timing).
The transport owner correlates and publishes those facts off the delivery path;
execution retains no additional request ledger or tool-result pairing heuristic.

## Acceptance

Acceptance covers both transports, prewarm and compaction, exact forwarded
history, cancellation, accounting, naming, partial delivery and failed publication.
Installed-Codex checks establish host execution separately from offline fixtures.
