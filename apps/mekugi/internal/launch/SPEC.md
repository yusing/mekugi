# Invocation launch

## Purpose and scope

`mekugi` dispatches authenticated frontend calls and offline inspection before
starting a live invocation. Live launch validates options, composes services,
binds a private IPv4 loopback endpoint on an OS-assigned port, and starts Codex
from PATH only after initialization succeeds. `mekugi server` owns long-lived
interactive invocations behind a private, peer-admitted Unix WebSocket.
Client disconnection leaves execution running; explicit end or server shutdown
closes the owned runtime. The existing Session Control ledger remains the sole
host dispatcher. Native renderer attachment and remote publication borrow the
same running owners. `mekugi web` serves only the embedded browser application;
it starts no execution runtime and owns no connector credentials.

[Thread Control](../../../../modules/threadcontrol/SPEC.md) creates independent
native child runtimes through the Mekugi server. Launch reuses the captured
provider/configuration selection and supplies each child’s actual scoped owners.
Codex executes each thread through its own Session Control connection; logical
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

- `mekugi [flags] codex [arguments...]` uses Codex authentication and models.
  Third-party credentials do not add models or trigger catalog preparation.
- `mekugi [flags] [arguments...]` uses authenticated Grok and configured OpenCode
  Go/Zen routes through stock Codex, without Codex login, retaining their `grok:`
  and `opencode*:` model namespaces. Startup requires at
  least one usable credential source. Explicit model selection wins, followed by
  the configured Grok default, Go, then Zen. Key validity is established by the
  provider response; Grok OAuth validation belongs to its provider.
- `mekugi grok` selects Grok alone with unprefixed model names.

Mekugi flags precede `codex`. Provider-selection arguments are rejected;
invocation-local overrides select the application endpoint and preserve Codex's
built-in OpenAI provider capabilities, including native tools, reasoning updates
and internal request metadata. Stock WebSockets and prewarming remain available;
Codex owns retries and HTTP fallback. Third-party launches disable OpenAI
requests and use provider-owned credentials. Configuration files remain untouched.

Noninteractive `exec resume` and `exec fork` receive the same automatic startup
default as new launches. Stock exec's provider override skips saved-model merging;
an explicit invocation model still wins. Interactive saved-model restoration
retains its separate session contract.

Interactive terminals use a renderer attached to the local server. The client
starts that server when absent through one startup lock and readiness check;
closing the client leaves the server and admitted work running. Redirected and noninteractive
commands retain stock cwd, arguments, environment and streams, augmented with the
private frontend PATH and invocation-owned integration settings. Native arguments
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

`codex headless --yolo` selects the [headless client](../headless/SPEC.md).
Launch rejects resume and unsupported CLI options; explicit
`--yolo` is required because the client cannot answer approvals.

## Model catalog

Launch assembles a private third-party catalog from selected provider metadata
and `codex debug models --bundled`, without authenticated OpenAI discovery.
The catalog contains only enabled Grok/OpenCode models in the selected invocation
namespace. Rebuilding removes stale entries and disabled providers. Entries combine
provider model IDs and supported capabilities with native v2 instruction/executor
metadata, preserving unknown catalog fields. Missing native v2 support
fails startup. OpenAI-only transport, search, service tiers and upgrade schedules
are excluded from third-party entries.

The private `model_catalog_json` path is pinned in the final invocation layer,
independent of user catalogs and other processes' shared caches. Profiles and
`exec --ignore-user-config` retain host-owned selection semantics. Invalid
catalogs, command failure or private-file failure prevent the main host launch.
Preparation is bounded to one minute and 8 MiB; its directory is private (0700)
and file mode is 0600, with both removed on exit or launch failure. User
configuration and shared caches remain untouched by catalog preparation. Codex
retains its catalog command behavior. Generated catalogs use `code_mode_only`;
OpenAI discovery follows the [catalog transport contract](../../../../docs/contracts/responses-transport.md#catalog-and-auxiliary-exchanges).

## Host integration

Mekugi launches require the stock JavaScript `exec` interface. Final invocation
overrides enable `features.code_mode` and `features.code_mode_only`, including
normalizing conflicting caller feature toggles without changing prompt operands
after `--`. Unsupported execution catalogs fail before inference under the
[backend authority contract](../../../../backends/codex/SPEC.md#execution-authority).
OpenAI discovery also sets model metadata to `code_mode_only`, since metadata
overrides those feature flags. Account discovery and refresh remain available;
Mekugi does not pin a bundled OpenAI catalog or edit the shared host cache.
Explicit caller-supplied static catalogs retain host selection semantics and
must expose the supported interface.

Final invocation overrides disable `features.sleep_tool`, including caller feature
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

Final invocation overrides disable Codex goals and collaboration-mode instruction
injection. The authenticated frontend catalog supplies its own guidance when
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
and child status rather than looking like a clean quit. Redirected/noninteractive
commands retain Codex's own exit output.

## Acceptance

Catalog assembly tests cover native v2 selection, unknown metadata, idempotent
rebuilding, provider exclusion and invocation namespaces. Actual subprocess and
PTY fixtures cover authentication, catalog selection, argument preservation,
private frontend dispatch, independent invocations,
startup cancellation and terminal handoff, signals, host failure and exit codes,
cleanup, native versus redirected launch, headless rejection, and debug handoff.
The executable entrypoint calls this same dispatcher; snapshot fixtures belong to
this owner and are checked without changing their rendered output.
