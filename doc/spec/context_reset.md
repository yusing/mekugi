# Inactive instruction loading

## REQ-CONTEXT-RESET-001 — Retained instruction-file loading

The instruction-file loader remains in source for future context-reset work.
Normal application requests do not invoke it. Manual and context-full compaction
use the host/provider path; there is no local-reset option or post-compaction
injection hook.

The retained logic identifies previously loaded instruction sources and selected
skills, snapshots eligible regular files, preserves source identity and ordering,
and bounds the collected content. Unavailable sources produce explicit notices.
It performs no model inference and selects no task or follow-up work.

Task trees, authored recovery summaries, completion markers, continuation
schedulers, recovery receipt stores, and synthetic completed-tool replay are not
part of this retained component. Existing stored user data is not purged.

MCP has its own reserved [transport contract](mcp.md). The instruction loader does
not register tools, start a server, or control the host lifecycle.

Acceptance for the completed cleanup is an inactive loader with no production
callers, reserved MCP component tests, and ordinary host compaction without local
recovery injection. Offline component checks cover the retained loader;
installed-host runtime verification remains pending.
