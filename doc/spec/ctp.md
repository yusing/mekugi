# Compact token protocol contract

## REQ-CTP-001 — Lossless model-visible data-plane encoding

Compact Token Protocol version 2 is opt-in with `--model-protocol ctp2` in `mekugi` mode.
The default, `--model-protocol native`, keeps request and response strings uncompressed. The removed
`ctp1` value and every other unknown model protocol fail before the router listens. Passthrough mode
uses native and rejects an explicit `ctp2` value.

CTP/2 is a reversible representation between the ordinary Mekugi request projection and the model
provider. It is not another Responses protocol or an edit-engine feature. Responses objects, roles,
instruction priority, identifiers, statuses, reasoning, schemas, grammar definitions, streaming,
usage, conversation selection, and compaction remain provider-owned and native. CTP/2 changes only
eligible model-visible request strings and assistant text. Newly emitted tool names, tool inputs,
and function arguments remain native.

The [agent-guidance contract](guide.md) owns model-visible interpretation and
emission rules. The router selects an existing instruction carrier: a nonempty
string-valued top-level `instructions` field, otherwise the first textual developer
message in `input`. That carrier stays native. Without one, the request stays native
and no response decoder is installed.

### Content-local representation

A content-local dictionary and its reference body occupy one string:

```text
!ctp2 D
0="an exact repeated string"
END
!ctp2 R
Reuse @{0} and @{0}.
```

Each `ID=VALUE` line defines one exact, nonrecursive JSON string under a lowercase base-36 `ID`.
`END` closes the dictionary. The following `!ctp2 R` body expands `@{ID}`. `@@{ID}` represents the
literal text `@{ID}`; every other `@` is literal. Definitions do not cross string boundaries and
cannot be inherited by another request or response string.

An ordinary string without an exact CTP/2 prefix is already native. To represent native text that
begins with `!ctp2 D`, `!ctp2 R`, `!ctp2 L`, or `!V`, the encoder prefixes `!ctp2 L` and a line feed.
The decoder removes only that literal tag. CTP/1 tags have no meaning in CTP/2 and remain native.

For each eligible string, the router discovers repeated exact substrings with the repository GPT-5
tokenizer. Candidates begin and end at token boundaries, stay within the string, and do not use
overlapping occurrences. Discovery grows repeated token seeds to maximal equal spans and trims them
to readable whitespace or punctuation boundaries.

Candidate scoring charges the dictionary line, every reference, the reference tag, and one virtual
token for the definition and for each reference. Candidates are ordered by remaining saving, then
readable boundaries, byte length, and bytewise value. At each byte position the encoder prefers the
longest selected definition. Definitions used fewer than twice after overlap resolution are removed.
The string uses its compact form only when its complete decoded-string token estimate is strictly
smaller than its literal form.

### Visible prior-output lines

Tool output may instead reuse exact lines from preceding visible custom-tool or function outputs:

```text
!V=7fa,12,3
+"literal tail"
```

The bytes after `!V` are newline-terminated operations. `=SUFFIX,START,COUNT` appends `COUNT` exact
lines beginning at one-based line `START` from the one preceding source whose locator uniquely ends
with `SUFFIX`. A string-valued output uses its native call ID as the locator. A textual part in a
multipart output uses `call-ID/PART`, where `PART` is the zero-based lowercase base-36 part index.
`+JSON_STRING` appends the exact JSON string value.

Only source locators without commas or line breaks participate. A single long line of at least 128
bytes or two consecutive equal lines can seed a match. The encoder chooses the longest available
match. Each reference must be smaller than the literal lines it replaces, and the complete visible
representation must be smaller than that string's content-local representation. Otherwise the
content-local or native form remains.

References resolve against sources preceding that encoded request string. Each request rebuilds
sources in input order from its current visible history. Appending history never changes earlier
encoded bytes. Branching or compaction naturally removes sources no longer present; the router keeps
no cross-request dictionary, adaptive codebook, training state, or provider-cache state.

### Request and response behavior

Eligible request strings are non-carrier message text, tool descriptions, historical custom-tool
inputs and outputs, and historical function arguments and outputs. Grok historical function
arguments remain native JSON because the downstream Chat tool-call contract requires JSON;
this exception does not disable encoding for eligible text. Tool output uses visible-line
encoding with content-local fallback; other eligible strings use content-local encoding. The router
does not rewrite binary or image content, reasoning items, identifiers, unknown fields, tool names
or choices, grammar definitions, JSON schemas, or executor carriers that are not model-visible.

An active request installs the CTP/2 response transformer even when every current request string is
already native, because the model guidance may still emit a compact assistant response. Assistant
text may contain one content-local representation or visible-line references to prior tool outputs
from that request. The router restores assistant text before Mekugi translates registered calls. It
never interprets CTP/2 in newly emitted tool names, custom-tool inputs, or function-call arguments.
Validated Codex compaction requests bypass CTP/2 request and response transformation even when their
history contains a textual developer message; compaction carries no CTP/2 guidance and remains native.

Malformed dictionaries, duplicate or unknown IDs, reference bodies without a local dictionary,
ambiguous or out-of-range visible sources, invalid literals, and unknown visible operations fail
response transformation. Provider error responses pass through without decoding.

For streaming Responses traffic, pending deltas remain provider-owned. Terminal text events are
decoded and counted once. Content-part, item, and completed-response projections are restored but do
not duplicate output observations. Non-streaming and streaming responses therefore expose identical
native assistant text.

Decoded strings and serialized JSON or SSE remain within the existing 64 MiB upstream JSON buffer
budget. Expansion fails before an oversized value reaches Mekugi or another downstream consumer.

### Observation

The CTP/2 codec emits no metric callbacks or retained observations. The in-process transport
capturer measures the actual client/provider request and response payloads and provider usage. Its
signed protocol savings include expansion and do not claim provider cache or billing behavior.

### Acceptance

1. Native mode preserves request and response behavior without adding CTP/2 guidance or state.
2. `mekugi` mode defaults to `native`; `ctp2` is accepted only in `mekugi` mode; `ctp1` and unknown values fail before listening.
3. CTP/2 activates only with an existing native instruction carrier and never creates one.
   Validated compaction requests remain native and are not considered for activation.
4. Every content-local dictionary and visible-line representation restores exact bytes, including
   leading, trailing, and final line feeds.
5. Content-local dictionaries are independently profitable and never cross string boundaries.
6. Visible-line references resolve only against preceding visible tool output, are independently
   profitable, and beat the complete content-local fallback.
7. Appending history cannot change an already encoded prefix; compaction and branching cannot
   reference removed history. For an otherwise unchanged request and configuration, restored
   tool history after router restart or a thread fork produces the same encoded input as an
   uninterrupted continuation. This is byte stability, not a guarantee of provider cache hits.
8. Literal CTP/2 prefixes round-trip through `!ctp2 L`; CTP/1 and ordinary `@` text remain native.
9. Tool names, new tool payloads, function arguments, schemas, reasoning, usage, identifiers, and
   instruction priority remain unchanged.
10. Non-streaming and streaming restoration expose the same native assistant text and count each
    terminal text once.
11. Malformed or ambiguous compact output fails honestly, and decoded-size limits fail before
    downstream translation.
12. Metrics account for every considered request and bounded observation without retaining text.

### Request wire preservation

Stock passthrough MUST forward the validated original request bytes unchanged. Mekugi and CTP/2
MUST preserve the received top-level envelope and untouched field values; only projected fields
and newly added fields are written. A projected field with unchanged decoded content MUST retain
its original spelling. Router-generated protocol JSON MUST disable HTML escaping of `<`, `>`,
and `&`. Changed content still requires JSON encoding; incoming literal escape text is not rewritten.
CTP/2 profitability MUST compare decoded model-visible strings rather than outer JSON escaping.
