# Change inspection options

Use `hchanges ID[..ID] ... [--summary|--history] [--workspace DIR] [--max-tokens N] [-- PATH ...]`.
`--summary` returns an aligned diffstat with paths, change bars, and totals; skip it when
already reading the diff. `--history` includes the full recovery chain.
Workspace file paths accept recorded, workspace-relative, or absolute spellings.

Matching files appear once per evaluation. Counts describe each evaluation, not a combined
net change. These are historical evaluated diffs, not editable current row references or a
record of shell edits. Unconfirmed results are not proof of application.
