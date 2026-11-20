# Headless application client

## Requirements

- Provide noninteractive single-prompt automation (`mekugi codex headless --yolo`) keeping Codex as
  root execution authority and preserving stock editing, execution, JS batching, preloads, and
  native collaboration.

## Current approach (replaceable)

- Accepts one non-whitespace stdin prompt up to 16 MiB and requires explicit `--yolo` (since
  interactive approvals and questions cannot be answered; unhandled requests fail with instructions
  to continue interactively). Resume and positional prompts are rejected.
- Runs on the same server-owned runtime and native coordination as the interactive client with a
  task-owned family and no renderer.
- Streams host messages as JSONL on stdout followed by a final `mekugi/headless/completed` event
  after Main finishes and family shutdown joins. Diagnostics go to stderr; turn failure,
  interruption, cancellation, or shutdown failure exits nonzero.
