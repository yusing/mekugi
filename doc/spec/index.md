---
pjdoc:
  version: 1
  kind: spec
  scope: root
  status: draft
  revision: "52"
  files:
    - journal.md
    - router.md
    - subagents.md
    - commentary.md
    - read.md
    - grep.md
    - symbol.md
    - inspect.md
    - plugin.md
    - diagnose.md
    - shell.md
    - ax.md
    - session.md
    - metrics.md
    - script.md
    - changes.md
    - correct.md
    - file.md
    - select.md
    - edit.md
    - output.md
    - mentor.md
    - guide.md
    - comparison.md
    - benchmark.md
    - ctp.md
---
# mekugi specification

Each listed file owns one interface requirement and its acceptance cases. Related facts are cited by stable ID or linked; they are not copied.

## Inventory

- [`REQ-JOURNAL-001`](journal.md): durable per-thread milestone journals
- [`REQ-ROUTER-001`](router.md): standalone and session-scoped Codex launch
- [`REQ-SUBAGENTS-001`](subagents.md): Grok-backed native subagents
- [`REQ-COMMENTARY-001`](commentary.md): user-only subagent activity details
- [`REQ-READ-001`](read.md): routed verified-row reading and bounded ranges
- [`REQ-GREP-001`](grep.md): routed ripgrep search with directly editable verified rows
- [`REQ-SYMBOL-001`](symbol.md): routed semantic symbol lookup with directly editable verified rows
- [`REQ-INSPECT-001`](inspect.md): routed structural file inspection with copyable `LINE:HASH` spans
- [`REQ-PLUGIN-001`](plugin.md): router-local custom tools and Code Mode carrier translation
- [`REQ-DIAGNOSE-001`](diagnose.md): opt-in agent issue reports
- [`REQ-SHELL-001`](shell.md): installable free-form script tool and interpreter selection
- [`REQ-AX-001`](ax.md): runtime reads and evidence-backed AX reporting
- [`REQ-SESSION-001`](session.md): offline logical session inspection
- [`REQ-METRICS-001`](metrics.md): in-process captured Responses metrics
- [`REQ-SCRIPT-001`](script.md): HPATCH/2 grammar and target forms
- [`REQ-CHANGES-001`](changes.md): shared hpatch change IDs, recovery chains, and bounded review reads
- [`REQ-CORRECT-001`](correct.md): rejected-script recovery with ordinary verified-row edits
- [`REQ-FILE-001`](file.md): file scope and lifecycle
- [`REQ-SELECT-001`](select.md): immutable-baseline target resolution
- [`REQ-EDIT-001`](edit.md): target-bearing mutation behavior
- [`REQ-OUTPUT-001`](output.md): output, validation, and failure behavior
- [`REQ-MENTOR-001`](mentor.md): main and subagent Mentor Handoff schedule
- [`REQ-GUIDE-001`](guide.md): concise agent guidance
- [`REQ-COMPARE-001`](comparison.md): token comparison scenarios
- [`REQ-BENCH-001`](benchmark.md): historical-commit correctness and paired model evaluation
- [`REQ-CTP-001`](ctp.md): lossless token-positive model-visible data-plane encoding

All listed requirements are must-haves for this increment.
