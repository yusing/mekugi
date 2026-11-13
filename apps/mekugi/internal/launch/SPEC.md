# Invocation launch

## Required capabilities and constraints

- Codex remains the root execution authority, with one interactive UI and one
  stock JavaScript execution interface. Direct execution is unsupported.
- Preserve stock editing, execution and independent batch/parallel control,
  including captured changes and live edit review for shell writes.
- The native client automatically starts the local server when needed. Shared
  server-owned threads outlive browser and connector closure.
  Server shutdown ends active work; saved Mekugi sessions remain resumable.
- Resume is product-local: Mekugi resumes Mekugi sessions, and stock Codex
  resumes stock Codex sessions.

## Current launch design and behavior

`mekugi` dispatches authenticated frontend calls and offline inspection before
starting a live invocation. Live launch validates options, composes services,
binds a private IPv4 loopback endpoint on an OS-assigned port, and starts the managed
`mekugi-app-server` only after initialization succeeds. Native sessions resolve the
runtime from the caller's PATH, then beside the Mekugi executable, without falling
back to stock Codex. Children retain their parent's executable and configuration.
`mekugi server` owns long-lived
interactive invocations behind a private, peer-admitted Unix WebSocket.
Client disconnection leaves execution running; explicit end or server shutdown
closes the owned runtime. The existing Session Control ledger remains the sole
host dispatcher. Native renderer attachment and remote publication borrow the
same running owners. `mekugi web` serves only the embedded browser application;
it starts no execution runtime and owns no connector credentials.

### Replaceable runtime composition

[Thread Control](../../../../modules/threadcontrol/SPEC.md) creates independent
native child runtimes through the Mekugi server. Launch reuses the captured
provider/configuration selection and supplies each child’s actual scoped owners.
Each backend executes through its own Session Control request owner; logical
coordination, admission and retained waits belong to Thread Control. Child startup
retains prepared authority and assignment before executing work. No client-view
subscription is needed for child execution.
After server restart, retained children remain stopped. Explicit admitted input
can resume the same child identity using its frozen preparation and latest
host-confirmed settings; reconciliation reads history without starting a runtime.

## Invocation selection

Launch owns option parsing and invocation-local settings. The same parser serves
initial launch and resumed model-selection checks, preserving option operands and
the command delimiter. Parsed values can be reused across invocations; generated
output paths and resources belong to each run. Configuration errors identify the
failure without quoting credential-bearing file contents.

The existing Mekugi `config.toml` accepts optional `[features]` boolean defaults:
`shell_guard = true`, `deduplicate_output = true`, `child_reply_summary = true`,
and `debug = false`. Each feature
resolves explicit CLI value, then file value, then built-in default independently.
Bare flags mean true, `=false` explicitly disables, and the last repeated flag
wins. Absent files, sections and keys preserve defaults; non-boolean values and
unknown keys fail with content-free diagnostics. Provider and host-owned options
retain their own contracts.

Configuration is loaded once, independently of provider credential selection,
before feature owners and host startup. Effective values belong to the invocation
and apply to its children and thread switches. Overrides never rewrite configuration
or saved session settings. Fresh launch/resume loads the current file; explicit
CLI overrides remain in the continuation command. New Mekugi boolean flags extend
the same options-owned feature resolution path, without a feature service.

- `mekugi [flags] codex [arguments...]` uses native OpenAI authentication and
  models unless an explicit profile selects another Responses provider.
- `mekugi [flags] -p NAME [arguments...]` uses the selected custom Responses
  provider. Bare `mekugi` without this selection fails with setup guidance,
  rather than silently selecting OpenAI.

Mekugi flags precede `codex` or other Codex arguments. Without a selected profile,
provider-selection overrides are rejected. Invocation-local overrides select
Mekugi's inference endpoint while retaining native OpenAI capabilities, including
tools, reasoning updates and internal request metadata. Codex owns retries,
WebSocket prewarming and HTTP fallback. Configuration files remain untouched.
Custom profiles retain the separate precedence and continuity contract below.

Interactive terminals use a renderer attached to the local server. The client
starts that server when absent through one startup lock and readiness check;
closing the client leaves the server and admitted work running. Explicit `headless`
uses that same native execution path. Redirected ordinary launches and stock CLI
subcommands are unsupported; no launch falls back to stock `codex`. Noninteractive
execution uses `mekugi codex headless --yolo` with a new thread and a stdin prompt.
Codex administrative commands run directly outside Mekugi. Native arguments
are explicitly mapped; unsupported options fail rather than acquiring a different
meaning. `--yolo` disables host approval and sandbox prompts only when supplied.
Resume retains the selected host thread and is unavailable to headless runs.

The interactive client submits intent and answers supported host requests through
the shared Session API. The server's Session Control owns the Codex app-server
connection; the renderer never connects to the JavaScript execution host or adds
model calls for UI plumbing. Explicit model/config/feature flags and
resume selectors retain their meaning. Without `--yolo`, configured host approval
and sandbox policy applies; with it, start/resume requests repeat `never` and
`danger-full-access`. Independent remote-write/sudo guards retain their owners.
There is one interactive UI. Public invocation options expose no response-mode
selector; internal adapter identifiers do not select another client.

The native UI excludes PTY emulation or screen scraping for Main, settings clones,
onboarding, cloud tasks, voice, Git-write UI actions and new provider authentication
flows. Remote human control uses the same server-owned Session API through the
connector; browser components and asset hosting have independent lifetimes.
These limits do not change
Codex-owned execution or explicitly invoked change-history tools.
Rewind's optional captured-edit reversal uses those tools through the host's
native shell under the [session-control contract](../sessioncontrol/SPEC.md).

`codex headless --yolo` selects the [headless adapter](../headless/SPEC.md), using
the same admitted runtime and Thread Control without a renderer. Its task-owned
family is joined on completion or cancellation. Launch rejects resume and
unsupported CLI options; explicit `--yolo` is required because the adapter cannot
answer approvals.

Explicit `-c bypass_hook_trust=true` or `false` applies to admitted thread startup
and its children. `--yolo` alone does not bypass configured hook trust.

## Codex profile overrides

`-p NAME` / `--profile NAME` imports the named profile's settings without upstream
Codex changes. Launch owns import, precedence and family lifetime; the
[provider-routing contract](../../../../docs/contracts/provider-routing.md#profile-defined-responses-routing)
owns interception, authentication, catalogs and transport. The import uses
invocation overrides, not native Codex profile layering.

### Import and precedence

Launch resolves `NAME.config.toml` under the invocation's effective Codex home.
Plain-name validation and flag forms match Codex; option operands and everything
after `--` remain literal. Missing, invalid or repeated selectors and unreadable,
non-regular or malformed files fail before inference with content-free diagnostics.

The complete parsed TOML becomes initial invocation-local `-c` overrides.
Typed values preserve nested tables, arrays and literal dotted keys rather than
flattening them into ambiguous override paths. Unrepresentable top-level dotted
keys fail rather than changing identity.

Imported values override ordinary base/project configuration. Explicit `-m`,
`-c`, feature and reasoning flags override imported values. Managed host policy
remains authoritative. This is CLI-override semantics, not native profile
layering: relative paths use the invocation workspace, and imported settings do
not establish a profile-specific configuration writer. No configuration files,
Codex home or credentials are replaced or rewritten.

Fixed execution capabilities belong to the native runtime, not profile settings
or Go launch overrides. Ordinary approval policy and headless's explicit `--yolo`
requirement remain caller-controlled.

### Admission and lifetime

Apply the same profile import to interactive and headless session launches,
headless, and wrapped exec/new/resume/fork. Non-session commands retain existing
host dispatch. A profile-selected custom provider supplies the required bare-launch route. A
profile without provider selection retains native OpenAI only for an explicit
`mekugi codex` invocation.

Read the profile once for a new invocation. Native admission carries the client's
effective Codex home and environment over the existing peer-admitted control
channel. This includes PATH and arbitrary authentication-command dependencies,
not only named API-key variables. Freeze them in the existing family setup,
apply normal invocation sanitization/instrumentation, and never mutate the daemon
or expose the snapshot in public frames, diagnostics or retained records.
No-profile and browser-created invocation behavior is unchanged.

Main, Codex children, Side, One-off and forked/rewound roots inherit the admitted
configuration and route. Explicit child model/effort choices override those
inherited fields without selecting a different provider by model-name prefix.
No per-turn profile reloads.

Fresh stopped-session resume reloads the selected profile; explicitly imported
fields override saved values, and explicit CLI fields override the profile.
Fields absent from the import retain ordinary resume precedence. This includes
context, compaction and provider settings, not only model and effort. In-family
stopped-child resume borrows the frozen family setup.

Live reattachment retains the running configuration and environment. A supplied
profile selector must match the admitted profile identity; a different selector
fails rather than silently reconfiguring an active session. Matching attachment
reports that it keeps the running configuration. Continuation commands preserve
`-p NAME` and original explicit options, never expanded profile contents or local
gateway addresses. A later invocation reloads the file. Resume stays product-local.

Existing user-requested configuration writes keep Codex's ordinary user-layer
destination. Imported overrides remain protected from ineffective lower-layer
preset writes. Profile-specific writeback and new persistence are not part of
this implementation. Literal secrets imported through argv have ordinary CLI
process-inspection exposure; prefer native environment/command authentication and
never print expanded argv in errors or continuation hints.

### Acceptance

Normal launch consumers cover:

- Equivalent flag forms, missing/repeated selectors, operands and `--` across
  native, redirected, headless and exec entrypoints.
- Typed TOML round trips, nested/literal keys, base/project/profile/explicit CLI
  precedence, host-policy conflicts and unchanged configuration files.
- Exact model/provider/context/compaction/effort settings through actual startup,
  including a model unknown to built-in Mekugi catalogs.
- A pre-existing daemon uses the new caller's environment, PATH and Codex home;
  simultaneous profiles and children do not mix settings or credentials.
- Fresh resume, live attach, child resume, Side, One-off and fork/rewind preserve
  the defined lifetime and explicitness rules; continuation reloads the profile.
- No-profile launches, approval behavior, headless completion and stock execution
  retain their existing contracts.

Installed-Codex fixtures use synthetic profiles, local providers and fake auth
commands for native auth, catalogs, tool execution and resume. Offline checks
alone do not establish that host boundary.

## Model catalog

Native OpenAI model discovery remains with the runtime. The shared start picker
reads `mekugi-models.json` shipped beside the native executable for offline choices;
it contains no retired
provider groups or aliases. An old saved picker namespace rejects rather than
selecting another provider. Static and remote custom catalogs preserve their
metadata while enforcing the supported JavaScript execution interface.

Profile imports pass supplied static catalogs to the native runtime without
rewriting them or creating derived copies. Bundled metadata reads are bounded to 8 MiB
and start no subprocess or authentication flow.
There is no provider-owned online catalog refresh or price snapshot in Launch.

## Host integration

Mekugi launches require the stock JavaScript `exec` interface. The native runtime
owns this tool mode independently of caller feature toggles and model metadata.
It also enables live model switching and reasoning-effort updates at its feature
owner. Unsupported
execution catalogs fail before inference under the
[backend authority contract](../../../../backends/codex/SPEC.md#execution-authority).
The gateway forwards model catalog bytes without rewriting tool mode. The native
runtime owns catalog validation and tool selection. Account discovery and refresh remain available;
Mekugi does not pin a bundled OpenAI catalog or edit the shared host cache.
Explicit caller-supplied static catalogs retain native selection semantics;
their tool-mode metadata does not change the required execution interface.

The native runtime disables `features.sleep_tool`, including caller feature
toggles. Thread-control waits and independent clock/timer behavior remain available.

Wrapped launches enable Codex's native rollout trace in a private, session-scoped
temporary directory. The recorder includes prompts and responses and has no
tool-only filter or storage cap. Session cleanup removes the directory; abrupt
process termination may leave temporary files. The override never changes user
configuration. [Execution evidence](../../../../backends/codex/SPEC.md#execution-evidence)
owns receipt interpretation and its independence from this temporary source.

In a Herdr-managed terminal, launch sets invocation-local `HERDR_AGENT=codex`
before starting session integration. It invokes no Herdr commands or pane APIs.

Interactive reasoning shortcuts `--low`, `--medium`, `--high`, `--xhigh`, `--max`
and `--ultra` become invocation-local reasoning-effort overrides. They also work
after `headless`; option operands, non-session subcommands and content after `--`
remain literal. The selected model determines supported efforts.

The native runtime disables Codex goals and collaboration-mode instruction injection.
The authenticated frontend catalog supplies its own guidance when
available. The [shell guard](../../../../modules/remotewriteapproval/SPEC.md)
installs only composed VCS/sudo registrations; conflicting hooks are warned about
and replaced for this invocation. Optional command tracking preserves caller hooks
when guarding is disabled. The host owns execution and permissions.

Approval and command-report channels have independent lifetimes. Guarded startup
fails closed if the approval channel cannot start; report acquisition or attachment
failure leaves approvals available and command output combined. Closing report
tracking never grants, cancels or completes a pending approval. The native hook's
invocation trust identity includes its status message: `Applying shell guard`
when guarding is enabled, otherwise `Tracking shell segments`. With guarding off,
explicit caller `PreToolUse` conflicts preserve caller settings and leave sh
untracked; Bash keeps its independent startup path.

The effective `deduplicate_output` setting controls only the
[model-input projection](../../../../modules/outputdeduplication/SPEC.md).
Runtime contracts belong to
[Responses integration](../../../../docs/contracts/responses-transport.md),
[session history](../../../../modules/sessionhistory/SPEC.md), and
[diagnostics](../../../../modules/diagnostics/SPEC.md). Debug launch passes its
selected AX read-log path to the executor and authenticated worker manifest,
preserving it across child environment changes. Explicit capture and
`MEKUGI_AX_OUTPUT` paths take precedence.

## Lifecycle and failure

Launch constructs invocation resources and the native attachment from their actual
owners, joins their lifetimes, and binds live and replay observations to the shared
session presentation. Module state stays with its composed owner.

Before terminal attachment, Ctrl-C cancels the client's startup wait. An already
admitted server runtime remains independent of that wait. Attached input uses the
existing semantic interrupt and exit controls; quitting or terminating the client
releases its renderer without stopping execution. The terminal-only `/detach`
command leaves the native attachment, including during active work, keeping the session
available to `--resume` and the WebUI. `/quit` retains the same local exit behavior.
The [native Herdr adapter](../herdr/SPEC.md) observes the client's active attachment
through its existing server connection. Native effects route panel selection and
temporary pane visibility through the same reattachment, draft and renderer owners.
Automatically started servers retain bounded private stderr and process-exit
evidence through a detached supervisor, independent of client lifetime. Collection
failure does not block startup; [process diagnostics](../../../../modules/diagnostics/SPEC.md#detached-server-evidence)
owns storage, privacy and retention.
Native resume attaches to an already-active logical thread; saved-thread resume
admits only durably enrolled Mekugi history through the
existing startup owner. Server shutdown cancels and joins its runtimes, allowing
Codex to clean up owned execution processes.

Redirected and noninteractive launches retain their invocation-local lifetime.
Before host handoff, Ctrl-C cancels startup and prevents launch, including an
interrupt already consumed by the startup receiver. After handoff, Codex owns
terminal Ctrl-C. SIGTERM cancels the child and services with bounded cleanup.
Unexpected invocation-server termination also stops Codex. Ordinary child exit
status is preserved; signal exits use `128 + signal`.

Startup errors, launch failure, cancellation and child exit release owned services
and temporary files. Cleanup failures are reported. Undelivered critical notices
and repetition summaries are printed after terminal restoration with control
sequences removed and readable text preserved. Debug
handoffs identify the retained private bundle without exposing its contents.
Catalog preparation prints content-free stderr progress immediately and every
ten seconds. Interactive output is width-bounded and cleared before handoff;
redirected output uses complete lines. Display failure stops progress only.
Cancellation/deadline failures take precedence over generic process/configuration
advice, and private bootstrap stderr is not exposed. Normal launch advertises no browser URL.

### Composer recall

Fresh interactive sessions load recent ordinary text from Codex’s shared composer
history. Acknowledged prompts, shell input and the ordinary exit draft are saved
under the selected host configuration. Question reply envelopes and secret
editors never enter this history. Storage runs independently of input and host
delivery; failures leave the composer usable and produce a visible notice.
History recall preserves the current draft and caret when returning past the
newest entry.

### Interactive exit

After terminal restoration, the client prints unsent drafts/notices, the latest
delivered bounded, deduplicated host stderr, Main's latest
cumulative host token usage and a shell-quoted continuation command for an established
thread. Input/total exclude cached input; cached/reasoning counts appear separately
when nonzero. Zero or unavailable usage omits the token line. Child usage and router
cost totals are not added. The continuation preserves the original executable,
router flags and Codex options before invocation rewrites, replacing a prior resume
selector with `resume THREAD_ID`. Color-capable output highlights commands in cyan,
respecting `NO_COLOR`.

Established sessions also offer `replay-session --session THREAD_ID`, with the
original executable and actual `--debug-dir` when debugging. No established thread
means no continuation/replay hint; successful archival instead prints
`Session archived: THREAD_ID`. Unexpected app-server exit preserves its diagnostic
and child status rather than looking like a clean quit. Headless execution uses
its own completion and failure output contract.

## Acceptance

Catalog assembly tests cover native v2 selection, unknown metadata, byte-preserving
forwarding, retired-provider rejection and invocation model identities. Actual subprocess and
PTY fixtures cover authentication, catalog selection, argument preservation,
private frontend dispatch, independent invocations,
startup cancellation and terminal handoff, signals, host failure and exit codes,
cleanup, redirected-launch rejection, headless admission, and debug handoff.
The executable entrypoint calls this same dispatcher; snapshot fixtures belong to
this owner and are checked without changing their rendered output.
