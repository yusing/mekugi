# Agent tools and change review

Helpers available to agents in wrapped sessions, and how captured changes behave.

## Wrapped-session helpers

Wrapped sessions put these on the agent's `PATH` and describe them in its
instructions. Ask for one by name when you want a specific read or review:

| Command | Purpose | Requirements |
| --- | --- | --- |
| `mread` | Continue retained output without rerunning | Replay directory access |
| `mrun` | Bound foreground output, optionally keeping its ending | The wrapped command |
| `mchanges` | Review/compose/revert/reapply captured edits by ID | Replay directory access |
| `mcat` | Batched UTF-8 reads, ranges, tails | None |
| `msymbol` | Batched definitions/references | Go: `gopls`; JS/TS/JSON: TypeScript 7 `tsc`; Python: `pyright-langserver` |
| `inspect_file` | Structural outlines | None |

```sh
mchanges amber1..amber3 --summary
mchanges revert amber2
mcat --number source.ts 10-20 40:60
mread REF
```

Captured edits are not a live Git diff. Named ignored files are included; unknown-target
commands capture observed effects, possibly from other writers. Previews are
provisional; missing evidence is labeled. Ignored untracked files require explicit named
targets to be retained and reverted/reapplied. Revert merges later edits, may create conflicts, and
is captured too. See [changes](../../modules/changehistory/SPEC.md), [capture coverage](../contracts/change-evidence.md), and [retained reads](../../modules/outputrecovery/SPEC.md).
