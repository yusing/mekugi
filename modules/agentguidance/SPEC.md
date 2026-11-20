# Agent guidance

## Requirements

- Keep long sessions and fresh delegates grounded in selected current sources rather than lossy
  LLM handover summaries: Codex resets context without a summary-generation request, and delegates
  can preload selected sources before their first inference.
- Deliver measured context-usage notices (directing agents to compact near window exhaustion) and
  up-front OS/Git workspace facts without overriding user/caller instruction authority.
- Keep model-facing guidance minimal in tokens while preserving meaning.

## Current approach (replaceable)

### Model, tool, and skill guidance

- Native runtime owns bundled model templates (`models-manager`), local shell helper descriptions
  on `tools.exec_command` (`core/src/mekugi_guidance`, derived from shell `PATH`), and the
  `child_reply_summary` (`## Summary`) rule. Caller base instructions, resumed history, and
  explicit catalogs stay intact; uninstalled plugins are not advertised proactively.
- In explicitly trusted local workspaces with a `skills-mgr` selection, native context assembly
  replaces Codex's stock skill catalog with the manager's XML catalog (controlled by
  `skills.include_instructions`) and records resolved skill prompts as compact `<skill name="…"/>`
  identities. Failed managed reads warn without substituting stale native instructions.

### Environment snapshot and context usage notices

- Native `ENVIRONMENT_CONTEXT` includes platform/OS version, kernel, user home, a snapshot-reuse
  hint, and a read-only Git snapshot (branch/HEAD, default branch, `core.hooksPath`, bounded
  status, 5 recent commits, unpushed count; excludes account identity/URLs). Reused across
  continuations and refreshed on reset or workspace change. `MEKUGI_ENVIRONMENT_FACTS=1` signals
  availability to app-server hooks (stripped from tool shells).
- Native `token_budget.rs` emits developer-role usage notices at 50/60/70/80/90% (90% adds
  `You must compact now.`) plus a user-visible transcript row; milestones reset after compaction.

### Compaction context

- Manual `/compact`, automatic token-budget reset, and agent checkpoints (`functions.new_context`)
  all perform summary-free reset with no cost-estimate veto. `reload:{sources:[...]}` replaces the
  retained selection, omission reuses it, and `sources:[]` clears it (persisted across resume/fork).
- Nested references inside selected `.md`, `.json`, and `.txt` sources (and skill files) expand up
  to depth 3 (root = depth 1); reads execute under the receiving thread's permissions, keeping
  individual failures visible without blocking other sources.
- Go `modules/agentguidance` retains shared preload-selection helpers and read-only support for
  historical saved compaction summaries. Stock Codex startup may still read a caller-configured
  `experimental_compact_prompt_file` even though native reset does not use it.

### Source-reference syntax

Preload, reload, and user attachments share one reference syntax (markers escaped here so selecting
this file does not trigger expansion): files (`\@path`, `\@path#L2-L4`), skills (`\$skill`,
`\$skill/references/file.md`), and composed captured changes (`\mchanges:blue1,blue3..7`). Quoted
paths preserve spaces and trailing punctuation; `AGENTS.md` is always excluded (Codex supplies it).

Preload and reload take `{"sources":[...]}` with string entries only, for example
`{"sources":["\u0040file#L2-L4","\u0024skill-name","\u006dchanges:blue1,blue3..7,birch2"]}`.
