---
name: mekugi
description: Review recorded file changes, recover bounded utility output, and maintain the durable Mekugi journal through the native Claude backend.
---

# Mekugi

Claude owns execution, permissions and session lifecycle. The shared utilities
run through native Bash. A streaming preview is a proposal; recorded change IDs
describe observed filesystem effects, including failed tools with partial edits.

Use the journal as the durable, user-facing work record, without waiting for the
user to request it. Create a task when evidence shows what substantial work
needs. A first call that loads governing evidence or guidance needs no journal
update. Short assignments need no task or plan. Use this durable journal instead
of a separate native task list. Keep states current through verification, and
mark verified work done with a journal batch before ending the turn.
Use mcp__mekugi__journal_batch for updates and mcp__mekugi__journal_read for
recovery. If these MCP tools are deferred, discover them with native ToolSearch
before starting substantial tool work.
Record established results, decisions, constraints and blockers in their owning
nodes instead of repeated commentary or status dumps. Batch related updates.
Use short, clear titles and ASD-STE100 Simplified Technical English. Small answers
and necessary questions remain conversational. Use journal tools only when they
are available to your native agent; do not bypass its tool restrictions.

{{JOURNAL}}

{{UTILITIES}}
