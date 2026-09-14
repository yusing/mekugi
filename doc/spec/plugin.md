# Router-local tool plugins

## REQ-PLUGIN-001 — Router-local tool plugins

In mekugi mode, `mekugi` discovers tool plugins only from the `mekugi/plugins`
directory beneath the platform user configuration directory. Each direct regular file whose
name ends in `.js` or `.mjs` is one compiled ECMAScript-module declaration, loaded in lexical
filename order; directories, symlinks, and other entries are not declarations. A missing or
empty directory contributes no plugins. There are no plugin-related router flags,
workspace-local discovery, remote discovery, or hot reload. TypeScript is an authoring format,
and the complete registry remains immutable for the router process lifetime. Passthrough mode
neither loads nor exposes the contributed tools.

During declaration validation, translation, and execution, configured and built-in modules may import
`mekugi:core/v1`. This exact virtual ECMAScript module is supplied by the router's authenticated snapshot;
it requires no plugin-owned dependency or copied binary. It exposes deterministic verified-row hashing,
formatting, logical-line counts and UTF-8 byte bounds, positive integer and `LINE:HASH` parsing, quoted
operand decoding, source-format capability classification, Go identifier and string-literal handling,
shell-header parsing, and interpreter identity. An unknown `mekugi:` module fails declaration loading.
The shared core exposes no filesystem, workspace, symlink, process, network, credential, carrier, or
row-resolution authority. Existing declarations that do not import it retain their behavior.

Each plugin declares a stable plugin identity and one or more globally named tools. Each tool
provides its exact OpenAI Responses custom-tool specification, a bounded string-input parser,
a translator, and an executor-side implementation. A specification may omit `format` for
unconstrained text or use an OpenAI grammar format whose syntax is `lark` or `regex`. The
model-visible name, description, format, grammar definition, input limit, translator, and
implementation are part of the validated declaration. Standard JSON-schema function tools,
runtime TypeScript transpilation, and arbitrary undocumented specification fields are not
supported by this increment.
Configured executor-backed names must also differ from shell keywords and built-ins,
including the shell-owned `hrun` command. This rule
ensures that their basename carrier selects an executable frontend instead of shell-owned
behavior.

Before opening its listener or installing any configured contributed-tool wrapper, the router
loads every discovered declaration and validates the complete registry. It reports all detected plugin
schema, API-version, identity, duplicate-name, input, translator, implementation, and wrapper
conflicts, then exits nonzero if any declaration is invalid. Failure exposes no
partial registry, forwards no Responses request, or starts an executor implementation. Locally deterministic grammar syntax and unsupported construct
checks occur at startup; this does not promise to reproduce a provider's model-specific or
complexity limits.

Built-in translations MUST use a router-lifetime host that loads its declaration and shared core
before the listener opens. Calls MUST be serialized within that host, retain the five-second
translation bound (including queue admission), and propagate caller cancellation. A timeout,
crash, malformed response, or output overflow MUST fail the current call and discard that host;
only a later call may start a replacement. No failed call or executor effect may be retried by
this mechanism. Input rejections remain ordinary bounded diagnostics and do not poison the host.
Shutdown MUST cancel active translation and reap the host before removing its snapshot.
Configured declarations and all executor invocations MUST retain isolated per-call hosts.

A successful translator returns a typed normal executor tool-call carrier. The router
validates the carrier kind, name, and payload against the tools available in that
request and retains ownership of response item IDs, call IDs, status, JSON and SSE framing,
history, and replay. A plugin cannot invent an unavailable carrier or return a raw Responses
envelope. The plugin API provides a canonical exec wrapper for tools that need one. The wrapper
owns the repeated outer Code Mode exec program, nested tool invocation, serialization, argument
quoting, and result forwarding. Its canonical Bash quoting keeps the worker command on one
physical line, escaping embedded line terminators while reconstructing each exact argv value.
The optional exec command template contains exactly one `{.}`
placeholder, which the router replaces with the complete quoted worker command. For configured
tools this is their frontend command. For built-in shell it is normally the fixed
`shell <interpreter> <program>` helper command; without a shebang, directive, or template, one
physical line containing one static external Bash command instead remains the complete outer
command. An optional JSON parameter object cannot contain `cmd`. The router supplies `cmd` from
the selected command. If the parameter object contains `login`, its value must be exactly `false`.

Requests may expose the Code Mode custom `exec` owner at the top level or in `additional_tools`, or native
top-level custom `apply_patch` plus function `exec_command`. The router replaces the editing
surface in either shape without opening another listener. In native requests, `exec_command`
remains the executor-owned carrier. Mekugi invokes the executor's `apply_patch` command through
that carrier and returns the already-rendered report as its exact successful output; ordinary
exec-backed contributions use direct native function arguments rather than a Code Mode wrapper.
Response restoration and replay retain the request's original carrier shape. Durable replay accepts
updates to the opaque `internal_chat_message_metadata_passthrough` field as the provider completes
a tool call, retaining its latest encoding without allowing changes to tool identity or input. JSON and all
terminal SSE statuses restore the request's tool catalog and choice plus completed calls' exact
carriers. Failed or incomplete responses do not evaluate unfinished call input or retain it for
replay. An output-item completion explicitly marked `incomplete` likewise does not evaluate unfinished
input. If `input.done` already handed off the complete call, replay retains that translation and
accepts the item's transition from `in_progress` to `incomplete` without evaluating it again.
An absent status on a completion event remains accepted. Completed calls remain replayable
when a later call or the response is interrupted.

For each configured executor-backed contributed tool, startup creates or verifies a session-private
executable symlink in the authenticated snapshot's `bin` directory. Its basename is exactly the contributed tool name,
and its target is the authenticated process-scoped snapshot wrapper with the same basename.
The snapshot wrapper targets a session-private pinned instance of the running `mekugi`
executable, not its replaceable installation pathname. Replacing the installation
must not change the worker implementation or manifest decoder for an active session.
The runtime directory must be on storage that permits execution of the pinned binary.
On Linux, startup also pins the running image when its installation pathname has
already been replaced or removed. Without a command template,
the exec wrapper invokes only the basename and represents the parsed model input as its ordered
argv. With a command template, the router replaces `{.}` with that same independently quoted
basename and argv. When launched through both symlinks, the router verifies the session frontend
location, snapshot identity, wrapper target, and registered implementation before passing the
remaining argv unchanged.
Worker authentication compares the executing file's identity with the resolved wrapper
target. Strict manifest decoding remains mandatory; unknown fields are not ignored
to accommodate a mismatched executable.
The configured-plugin worker keeps the frontend standard input separate from the JavaScript
host's JSON control stream. The host exposes that input only as a dedicated inherited descriptor during
executor calls.

Built-in shell and its private hcat, hgrep, hsymbol, and inspect_file commands use a shared authenticated executor boundary.
The shared `shell` name locates the authenticated executor for the current thread.
Private commands execute only inside that boundary and do not create standalone
frontends or additional `PATH` dependencies.

Executors may attach a private `failureClass` only with a nonzero exit status.
The host accepts only the documented reader-failure allowlist; arbitrary values and
success/class combinations reject without reflecting their contents. This metadata
supports opt-in AX evidence without changing command output or transport metrics.

Executors may return `omittedOutput: {stdout, stderr}` containing only omitted suffixes,
bounded to 16 MiB combined. The host validates the strings, and the authenticated executor
persists them in the managed output recovery store before exposing an `houtput` receipt.
This optional result field does not execute effects or change the original exit status;
storage failure is explicit and never claims that recovery is available.

An executor returns its current stdout, stderr, and exit status once. Observation
never starts a second execution or substitutes a benchmark baseline.
An executor may attach `terminationReason: "output_limit"` only to a nonzero result
after bounded output capture and stream cleanup. Semantic resolvers may request
invocation-owned descendant cleanup without changing a completed semantic result.
The host validates this private metadata and retires the requested process group on
supported platforms before returning. Absent metadata preserves ordinary successful
background-process and cancellation behavior.

Without exec parameters, the carrier supplies no working-directory or environment override.
With exec parameters, the router forwards the JSON values without replacing the request-specific
Codex contract. Codex validates those values and remains the owner of working directory, sandbox,
filesystem, process, network, terminal, and permission enforcement. Missing, conflicting,
incorrectly targeted, or unusable configured-tool symlinks fail startup before the listener
opens. Configured frontends reside in disjoint session directories. The launcher
prepends only its own directory to Codex's PATH; no installation-directory lock or
shared frontend is created. Concurrent sessions cannot replace each other's
frontends. Shutdown removes only the owning session's frontends and snapshot.

Translated history retains the plugin identity, original tool name and input, and exact carrier
kind, name, and payload. Replay accepts only the byte-identical retained carrier and restores
the original model-visible call before upstream forwarding. Ordinary plugins do not enter
mekugi recovery ancestry. Runtime model-input rejection returns a bounded diagnostic
through an available executor carrier; a translator protocol violation, unavailable carrier,
or malformed carrier is a routing failure rather than a successful approximation.

Completed translations MUST survive router restart and restore inherited calls in resumed or
forked threads within the same canonical workspace, independently of routing-session IDs and
cache keys. The router MUST durably retain a completed mapping before exposing its executable
carrier, including completed streaming calls whose enclosing response later ends or is interrupted.
Storage failures MUST fail routing before that carrier is exposed. Replay MUST NOT reevaluate
the historical input or invoke an old plugin worker. Changed carrier identity, conflicting
mappings, and corrupt records reject; unknown legacy calls without retained mappings remain
unchanged. Durable records do not keep executor processes or private runtime capabilities alive.

Grammar compatibility for this requirement is pinned to OpenAI's Custom tools guide
(<https://developers.openai.com/api/docs/guides/function-calling#custom-tools>): regex
definitions use Rust `regex` syntax and do not support lookarounds or lazy quantifiers; Lark
definitions support common imports and `%ignore` while terminal priorities, templates,
non-common imports, and `%declare` are unsupported. Startup validates this stable subset
locally. Rust regex compilation is delegated to installed ripgrep's default engine with
configuration files disabled; PCRE is never selected. The router resolves `rg` on its own `PATH`
before isolating the validation host. The prerequisite applies only when a declaration contains
an actual regex, whether a regex-format definition or a Lark regex terminal. Missing or unusable
`rg`, compilation failure, or a bounded validator failure rejects that declaration with a clear
diagnostic; unconstrained and regex-free Lark declarations remain independent of `rg`.
Provider modifier checks respect escapes, nested character classes, capture names, and group
flags rather than inspecting raw substrings. Lazy repetitions, including counted lazy repetitions,
and extended mode are rejected; ordinary Rust escapes, Unicode properties, classes, and supported
flags retain their engine semantics. Lark terminal flags participate in validation rather than
being discarded. Provider model-specific and complexity limits remain provider-owned, distinct
from the local compiler's resource limits.

Acceptance:

1. A valid discovered JavaScript declaration contributes its exact unconstrained, Lark, or
   regex custom-tool object to mekugi-mode Responses requests without a plugin flag.
2. A missing or empty plugin directory preserves the built-in mekugi-mode behavior, while
   passthrough mode loads and exposes no contributed tools.
3. One invalid declaration or configured-tool symlink prevents the listener from opening;
   independent startup mismatches are reported together and no valid subset is exposed.
4. Duplicate tool names across plugins or built-ins fail startup, and the registry does not
   change until process restart.
5. A plugin may translate to any compatible executor tool call available in the current
   request; an unavailable or wrong-kind carrier rejects before upstream execution.
6. The exec wrapper renders the canonical Code Mode program or native function arguments and independently quotes every argv
   value. An optional template contains exactly one `{.}`, which expands to the complete worker
   command. The plugin declaration does not contain or generate the outer carrier shape.
7. Invoking a configured executor-backed tool resolves its session basename frontend through the
   authenticated snapshot wrapper to `mekugi`, verifies the pinned registry, dispatches
   by `argv[0]`, and delivers the declared argv under Codex's cwd, sandbox, and permissions.
8. JSON and SSE responses preserve call identity while replacing a contributed call with its
   validated carrier. While the complete streaming input is buffered for validation, each withheld
   input delta becomes a content-free native `response.in_progress` event so downstream SSE remains
   active without exposing untranslated content. Native function-argument events replace custom
   input events when the request uses native tools. Replay restores the exact original contributed
   call after verifying the retained carrier.
   Fresh-process resume and same- or different-process forks preserve that exact call in both
   native and Code Mode histories. A fork with fewer inherited calls cannot remove the parent's
   records or use its omitted calls for recovery.
9. A model-input diagnostic is bounded and recoverable, while an invalid translator result
   cannot be returned or counted as a successful tool call.
10. Observation failure cannot replace an otherwise successful translated carrier or executor
    result; request cancellation still propagates.
11. An executor returns one validated current result and does not run a comparison execution.
12. A configured plugin can import `mekugi:core/v1` and obtains the same verified-row, source, Go lexical,
    and shell-header semantics as built-in contributions. An unavailable core version rejects startup,
    and passthrough mode loads no core artifact.
