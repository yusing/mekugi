# Sudo approval

## Purpose

Classify sudo approval and authentication modes and retain exact session grants.

## Scope

The composed interactive shell guard participates in the shared native hook.
Removing this module removes its instrumentation, accepted request kinds, and
session grants. The other approval module and command tracking remain independent.
No module registration exposes an agent-facing tool.

## Behavior

Interactive Mekugi command execution asks approval before running each applicable
sudo invocation, including with cached credentials or passwordless policy.
The approval dock offers approval once, approval for the exact expanded argument
list, workdir and resolved executable for the current UI session, or denial.
Session grants release queued duplicates and expire with the UI, as for the
[remote-write approval](../remotewriteapproval/SPEC.md). Denial fails only that
command with exit status 1. Listing privileges (`sudo -l` or `--list`), help,
version and timestamp removal remain outside command approval.
Explicit noninteractive, stdin-password and caller-provided askpass modes still
ask command approval but retain their native input policy.

Approval and password entry are separate. After approval, sudo's native policy
decides whether authentication is needed. Its askpass request opens only the
shared secret answer editor; a rejected password requests another answer without
another command approval. Cached credentials and passwordless commands need no
password dialog. Session command grants never answer password requests. Skipping
the password fails authentication. Large argument lists are explicitly abbreviated
in the password dialog; execution and command approval keep the original arguments.
Passwords travel only through the private local approval channel
and askpass pipe. They never enter host tool arguments, model input, input history,
activity answer records or retained replay. Hiding the dialog keeps its draft;
command cancellation, UI shutdown and the approval timeout withdraw it.

The shared `--shell-guard` toggle controls this support and remote-write approval.
Codex's approval policy remains independent. It does
not grant sandbox permissions or change sudo policy. It requires the interactive
UI, the installed `mekugi-exec` helper and access to its local approval channel;
headless and passthrough commands retain their existing behavior. Direct sudo
names and executable paths through supported command wrappers are instrumented
by the native command hook. Shell functions keep their existing lookup.
Supported nested shell command strings retain sudo guarding through environment-
clearing wrappers, including absolute sudo paths.
Inside script files, inherited PATH guards cover bare sudo names. Bash startup
also covers direct known absolute sudo paths when shell guarding is enabled.
Scripts keep their original bytes, interpreter, arguments and exit status;
absolute paths invoked through wrappers inside script files retain the same
limits as the VCS guard's Bash path functions.

## Lifecycle

The application creates one module-owned grant set for each attached UI lifetime.
Grants are invocation-local and discarded on detach; changing the displayed thread
does not create another set. Fresh resume creates a new set. The application owns
the shared local decision channel and joins it independently of segment reporting.

## Codex connection

The binding registers this guard's participation and its session-grant factory.
The application composes only registered resources into the backend's native hook.
The helper evaluates the module policy against expanded arguments at execution;
Authentication remains sudo-owned; the module validates secret answers before delivery.
Host hook-shell launch and timeout policy remain Codex-owned. The helper does not
compose the application or duplicate host execution.

## Records

None. Grants and secret answers are never persisted.

## Failure

An unavailable decision channel cannot authorize a command. Requests for an absent
module are rejected before UI presentation. The remaining guard keeps its own
resources, including nested-shell instrumentation when only sudo is present.

## Acceptance
Native helper and UI acceptance covers command approval, independent password requests, exact grants, cancellation, secret-only delivery, and unchanged execution.

Module acceptance exercises policy and isolated grant lifetimes through the
scripted binder. Application acceptance covers leave-one-out resources, request
admission, exact queued grants, secret entry, and native execution. Installed-host
checks remain distinct from offline fixtures.
