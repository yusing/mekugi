# Output deduplication

## Purpose

Reduce repeated model-input text while preserving host execution and evidence.

## Scope

The module owns request-local matching, reference text, eligibility, and repetition
receipts. The Codex binding selects original host parts and submitted attachment
bodies. The application applies the projection after its evidence consumers and
maps returned receipts to native activity and durable attachment identities. The module also owns
local token-savings estimates; capture aggregates their receipts without reading
output content or initializing a tokenizer.

## Behavior

Duplicate-output projection is enabled by default in Mekugi mode. Disable it with
the launch option `--deduplicate-output=false`. It changes only eligible text
in the model-visible request input. Passthrough is unchanged. The original request
input, host result, Codex rollout, retained replay and change evidence, and UI
keep the full text. Transcript output rows add `N lines repeated` for partial
replacement or `repeated` for full replacement. A multi-command cell puts its
returned-output summary on the last command row; the suffix describes the cell
result, not each command independently. File and skill attachment rows carry the
same suffix, aggregated across chunks of each submitted snapshot. Refreshing a
request does not add another receipt. Identity-keyed annotations remain available
when native items arrive late or the same process replaces its transcript feed;
restoration resolves only the arriving item, without republishing unrelated history.
Operation batches with repetition annotations keep their individual rows visible.
Attachment dialogs retain the original
submitted content. Legacy attachment envelopes without durable submission identity
still deduplicate model input but receive no guessed transcript suffix. Every
evidence consumer, including command
and patch observation, result confirmation, failure observation, and retention, reads the original output before this final projection.

A unit is one host-produced text part of a `function_call_output` or
`custom_tool_call_output`, or one submitted file or managed-skill frame body in a
user message through a complete durable envelope in an `input_text` part.
A string output is one part. Execution and attachment headers remain
verbatim. Durable attachment envelopes use their submitted bodies; files and skills
are never reopened to infer equality. Compaction reader results participate as
native tool output before the application adds reload-context framing, so later
ordinary reads can reference the supplied source text. Frame byte ranges describe the original
snapshot, while reference rows address the earlier visible body. Ordinary user
requests, base instructions, selection attachments, malformed envelopes, and media
are excluded. String-content or non-`input_text` envelopes and bare frame text
remain ordinary input. Native selected-skill XML retains its existing manager
rewrite. Router-appended parts, including warnings and edit notices, remain intact. Outputs without a call ID and native
live-session JSON envelopes are excluded.

Verbatim runs aligned to logical lines and covering at least 192 bytes may be
replaced with a reference to earlier output still visible in the same request.
Each replacement must save at least 32 estimated GPT-5 tokens after counting its
complete reference, including source ID, row range and trailing newline. Smaller
savings or an unavailable estimate leave that run verbatim and referenceable.
The gate counts the matched run and complete reference independently. Surrounding
BPE token boundaries can make the whole text part's savings differ from this
standalone estimate; receipts measure the whole changed text part separately.
Matching is content-based, without line-ending or line-number normalization.
The bounded matching window covers the last 256 KiB of indexed verbatim text;
units below the byte threshold are neither replaced nor indexed. The longest matching run
wins, with ties resolved in favor of the most recent source.

References have the form `[same as O3]` or `[same as O3 L40-112]`; a single row
uses `L40`. Each eligible unit with remaining verbatim text gets a source anchor
such as `[O3]`, on its own line after the preserved execution or attachment header.
IDs are assigned in forward order in one request-local sequence across tool text
parts and attachment frames. Fully replaced units get no anchor. Sources get their
anchors immediately, even before a later repeat, so extending the input preserves
earlier projected items.

The row range is omitted for an entire earlier body. Rows start at 1 after the
anchor and count the earlier body as delivered, with each generated reference
counting as one row. References always target visible verbatim text, never anchors,
generated references or replaced spans, so they need no second hop.
Literal host text resembling a marker remains host text.

Each projection is a fresh forward pass over the current original input, with
no persistent matcher state. Compacted, forked, and resumed views can reference
only their own current input. Fresh projections of the same original host input
are byte-identical, and extending that input preserves earlier projected items.
The [backend history owner](../../backends/codex/responses/SPEC.md#connection-local-history) owns provider-history reconciliation.

### Local savings estimates

Each receipt reports the net local content-token difference before and after
projection, including anchors and references. Savings can be negative and are
not provider billing savings. Estimates count decoded JSON keys and scalar values
independently, excluding punctuation, ordering, whitespace and string-escape
spelling. Equivalent numbers normalize without losing precision; arrays retain
every element. Literal code and JSON strings keep their internal escapes.
Unchanged keys and scalar values cancel. Measurement counts complete changed
text parts, including attachment envelopes, rather than their individual body fragments.
These pinned GPT-5 estimates are never exact provider input or billed tokens.

An unavailable estimate prevents further replacements in that pass and is
counted as unmeasured; earlier replacements and their receipts remain intact.
Tokenization is initialized only when evaluating a match or measuring changed
input, not when composing a frontend worker. The gate and measurement share one
request-local codec. Unchanged input has zero savings.
The [metrics contract](../transportdiagnostics/SPEC.md) owns invocation aggregation and
presentation of these receipts.

## Lifecycle

The module is stateless. Each prepared projection captures only the original
request's host-part bounds. Applying it creates one bounded matcher and releases
its buffers before returning. No matcher, references, or request content survives
in invocation, thread, or turn state.

## Codex connection

The required input-projection binding prepares against the original request and
applies against the final model-input copy. Removing the composition entry removes
both phases. Compaction, execution-free requests, and prewarm keep their existing
application paths. Launch opt-out selects no duplicate-output projection.

## Records

None. Durable attachment envelopes and original host evidence remain with their
existing owners; projection neither rewrites nor reopens them.

## Failure

Malformed or unsupported item and attachment shapes pass through unchanged.
Unavailable required binding capability rejects module activation. Indexed text
is bounded to 256 KiB; this is not a total-heap or zero-allocation guarantee.

## Acceptance

Module checks cover matching thresholds, newest-source ties, visible references,
line endings, eviction, and independent prepared requests. Binding checks cover
eligible tool and attachment shapes and original-part bounds. Application checks
cover original evidence, host continuation results, provider-prefix reuse, native read
ranges, call attribution, opt-out, and unchanged transcript rendering.
