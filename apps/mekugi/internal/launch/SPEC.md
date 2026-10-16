# Invocation launch

## Purpose and scope

`mekugi` dispatches authenticated frontend calls and offline inspection before
starting a live invocation. Live launch validates options, composes services,
binds a private IPv4 loopback endpoint on an OS-assigned port, and starts Codex
from PATH only after initialization succeeds. No daemon, fixed-listener option,
standalone router command, or persistent configuration change is introduced.

## Invocation selection

Launch owns option parsing and invocation-local settings. The same parser serves
initial launch and resumed model-selection checks, preserving option operands and
the command delimiter. Parsed values can be reused across invocations; generated
output paths and resources belong to each run. Configuration errors identify the
failure without quoting credential-bearing file contents.

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
Third-party catalogs use the selected provider metadata and `codex debug models
--bundled`, requiring its native v2 template without authenticated OpenAI discovery.
The application pins the private `model_catalog_json` path in the final invocation
layer, independent of user catalogs and other processes' shared caches. Profiles
and `exec --ignore-user-config` retain host-owned selection semantics. Invalid
catalogs, command failure or private-file failure prevent the main host launch.
Preparation is bounded to one minute and 8 MiB; its directory is private (0700)
and file mode is 0600, with both removed on exit or launch failure. The application
does not rewrite user configuration or shared caches. Codex retains its catalog
command behavior, and `/v1/models` remains unchanged rather than injecting models
or synthetic ETags.

Noninteractive `exec resume` and `exec fork` receive the same automatic startup
default as new launches. Stock exec's provider override skips saved-model merging;
an explicit invocation model still wins. Interactive saved-model restoration
retains its separate session contract.

Interactive terminals use the native client. Redirected and noninteractive
commands retain stock cwd, arguments, environment and streams, augmented with the
private frontend PATH and invocation-owned integration settings. Native arguments
are explicitly mapped; unsupported options fail rather than acquiring a different
meaning. `--yolo` disables host approval and sandbox prompts only when supplied.
Resume retains the selected host thread and is unavailable to headless runs.

The interactive client connects only to Codex app-server, submitting intent and
answering supported host requests; it never connects to the JavaScript execution
host or adds model calls for UI plumbing. Explicit model/config/feature flags and
resume selectors retain their meaning. Without `--yolo`, configured host approval
and sandbox policy applies; with it, start/resume requests repeat `never` and
`danger-full-access`. Independent remote-write/sudo guards retain their owners.
There is one interactive UI, with no fallback client selection.

The UI excludes PTY emulation or screen scraping for Main, settings clones,
onboarding, cloud tasks, voice, Git-write or edit-rollback UI actions, browser
frontends/remote hosting, and new authentication flows. These limits do not change
Codex-owned execution or explicitly invoked change-history tools.

`codex headless --yolo` selects the [headless client](../headless/SPEC.md).
Launch rejects resume, unsupported CLI options, and passthrough mode; explicit
`--yolo` is required because the client cannot answer approvals.

## Host integration

Mekugi launches require the stock JavaScript `exec` interface. Final invocation
overrides enable `features.code_mode` and `features.code_mode_only`, including
normalizing conflicting caller feature toggles without changing prompt operands
after `--`. Unsupported execution catalogs fail before inference under the
[backend authority contract](../../../../backends/codex/SPEC.md#execution-authority).

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

The `--deduplicate-output` flag defaults to true and controls only the
[model-input projection](../../../../modules/outputdeduplication/SPEC.md).
Passthrough does not apply it. Runtime contracts belong to
[Responses integration](../../../../docs/contracts/responses-transport.md),
[session history](../../../../modules/sessionhistory/SPEC.md), and
[diagnostics](../../../../modules/diagnostics/SPEC.md). Debug launch passes its
selected AX read-log path to the executor and authenticated worker manifest,
preserving it across child environment changes. Explicit capture and
`MEKUGI_AX_OUTPUT` paths take precedence.

## Lifecycle and failure

Before host handoff, Ctrl-C cancels startup and prevents launch, including an
interrupt already consumed by the startup receiver. After handoff, Codex owns
terminal Ctrl-C; the wrapper neither forwards it twice nor stops the router.
SIGTERM cancels the child and services with bounded process cleanup. Unexpected
router termination also stops Codex. Ordinary child exit status is preserved;
signal exits use `128 + signal`.

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

### Interactive exit

After terminal restoration, the client prints unsent drafts/notices, Main's latest
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

Actual subprocess and PTY fixtures cover authentication and catalog selection,
argument preservation, private frontend dispatch, independent invocations,
startup cancellation and terminal handoff, signals, host failure and exit codes,
cleanup, native versus redirected launch, headless rejection, and debug handoff.
The executable entrypoint calls this same dispatcher; snapshot fixtures belong to
this owner and are checked without changing their rendered output.
