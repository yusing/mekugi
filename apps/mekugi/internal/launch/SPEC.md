# Invocation launch

## Purpose and scope

`mekugi` dispatches authenticated frontend calls and offline inspection before
starting a live invocation. Live launch validates options, composes services,
binds a private IPv4 loopback endpoint on an OS-assigned port, and starts Codex
from PATH only after initialization succeeds. No daemon, fixed-listener option,
standalone router command, or persistent configuration change is introduced.

## Invocation selection

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
Third-party catalogs use private bundled host metadata and the selected provider
catalog; their temporary files are removed after the invocation.

Interactive terminals use the native client. Redirected and noninteractive
commands retain stock cwd, arguments, environment and streams, augmented with the
private frontend PATH and invocation-owned integration settings. Native arguments
are explicitly mapped; unsupported options fail rather than acquiring a different
meaning. `--yolo` disables host approval and sandbox prompts only when supplied.
Resume retains the selected host thread and is unavailable to headless runs.

`codex headless --yolo` runs one nonempty stdin prompt in a new thread, with JSONL
host evidence on stdout and diagnostics on stderr. Its execution and failure
contract remains in [headless behavior](../../../../doc/spec/router.md#headless-app-server-frontend).

## Host integration

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

The `--deduplicate-output` flag defaults to true and controls only the
[model-input projection](../../../../modules/outputdeduplication/SPEC.md).
Passthrough does not apply it. Runtime transport, storage and diagnostic behavior
belong to [session integration](../../../../doc/spec/router.md).

## Lifecycle and failure

Before host handoff, Ctrl-C cancels startup and prevents launch, including an
interrupt already consumed by the startup receiver. After handoff, Codex owns
terminal Ctrl-C; the wrapper neither forwards it twice nor stops the router.
SIGTERM cancels the child and services with bounded process cleanup. Unexpected
router termination also stops Codex. Ordinary child exit status is preserved;
signal exits use `128 + signal`.

Startup errors, launch failure, cancellation and child exit release owned services
and temporary files. Cleanup failures are reported. Undelivered critical notices
are printed after terminal restoration with control sequences removed. Debug
handoffs identify the retained private bundle without exposing its contents.
Catalog progress is bounded and auxiliary: a display failure cannot fail catalog
preparation or cancel startup. Normal launch advertises no browser URL.

## Acceptance

Actual subprocess and PTY fixtures cover authentication and catalog selection,
argument preservation, private frontend dispatch, independent invocations,
startup cancellation and terminal handoff, signals, host failure and exit codes,
cleanup, native versus redirected launch, headless rejection, and debug handoff.
The executable entrypoint calls this same dispatcher; snapshot fixtures belong to
this owner and are checked without changing their rendered output.
