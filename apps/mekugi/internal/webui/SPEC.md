# Web UI

## Requirements

- Create, follow, and control threads across multiple explicitly selected execution machines with
  no default/implicit connection; the UI host may run separately from execution machines.
- Consume the reusable [Session API](../sessionapi/CONTRACT.md) with terminal semantic-control and
  dialog parity.
- Support browser file uploads and execution-machine path attachments within the 32 MiB
  decoded-upload budget per message, with image previews.
- Preserve queued edits across browser and native clients, including retraction for editing and
  steering queued input without losing text or attachments.
- Keep pickers, commands, live diffs, saved changes, images, and shell highlighting responsive on
  desktop and phone layouts (light and dark), showing worktree directory names and omitting
  expansion controls on empty items.
- Show Main/delegate public reasoning summaries, spawn assignments, and per-agent usage/cost
  without raw reasoning or duplicate completion entries.
- Keep work server-owned: closing the browser or connector leaves threads running; ending a thread
  is explicit.

## Current availability gap

Secret prompts (`sudo`/GPG) and native-local filesystem/editor actions remain native-terminal-only.

## Current approach (replaceable)

- `mekugi web` serves embedded Svelte assets built with `make webui` (returns 503 if unbuilt) and
  holds no machine credentials, runtimes, or proxy routes. Browser connections talk directly to
  each machine's [Connector](../connector/SPEC.md) over the [Web UI contract](CONTRACT.md).
- **Machines & PWA**: machine names, connector URLs, and bearer tokens store in origin-scoped
  browser storage (never URLs/cookies); installable as an online standalone PWA over HTTPS or
  localhost.
- **Chat layout & controls**: sidebar lists threads per machine; composer shares `/`, `@`, `$`,
  `!`, presets, model/effort/Fast pickers, inline selection Reference, active-process bar with Stop,
  and Retry/Edit/Discard recovery for rejected submissions. Right panel (sheet on phone) hosts
  Agents roster, Changes (per-caller composed diffs), and Details; live edits replace the
  transcript while streaming with independent per-card scrolling.
- **Replay**: opens read-only server-owned playback on the selected machine with shared transport
  controls across viewers (mutually exclusive with a live owner for the same thread).
- See [Server, connector and browser guide](../../../../docs/guide/remote.md) for deployment and
  Docker Compose setup.
