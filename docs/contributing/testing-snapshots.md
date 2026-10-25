# Terminal snapshot procedures

Terminal appearance changes require actual-renderer `TestUISnapshot` fixtures through
`testsupport/uisnapshot` in the owner's `testdata/snapshots/`. Fix time, theme, dimensions, and other
nondeterministic inputs. Run commands from the repository root using the
[common test runner](../../CONTEXT-TESTS.md#runner). The staged-tree snapshot commit gate is
documented in [Build and test](development.md#build-and-test).

```sh
make test-ui-snapshots SNAPSHOT_PACKAGES=./apps/mekugi/internal/launch SNAPSHOT='^TestUISnapshotNativeSessionShortcuts$'
```

Checks are offline, include the snapshot harness, and need neither Codex nor model requests.
Omit `SNAPSHOT_PACKAGES` to discover snapshot-containing packages across the workspace before
compiling them, or `SNAPSHOT` for all UI snapshot cases. Discovery includes `TestUISnapshot`
and harness `TestSnapshot` declarations, including platform-tagged files; Go selects runnable
tests. `SNAPSHOT` filters test names within selected packages. Explicit package paths bypass discovery;
`SNAPSHOT_PACKAGES=@workspace` compiles every member package. The same selections apply to
`make update-ui-snapshots`.

- `testsupport/uisnapshot.Assert` strips ANSI only: spacing, blank lines, wrapping and borders
  remain exact. Text comparisons do not prove color/style correctness.
- `AssertTerminal` paints into a fixed-width VT emulator and retains canonical ANSI as quoted rows,
  covering colors, emphasis, links, padding and style restoration independently of equivalent escape
  sequences. Include a following plain row to detect style leaks. Theme-sensitive cases cover
  terminal/dark/light; code fills also cover a reported background.
- A missing/changed fixture fails and writes a sibling `.txt.new` candidate plus a unified diff;
  the baseline stays unchanged. Review the candidate, then move it over the fixture or run the
  selected update recipe. Omitting `SNAPSHOT` updates all matching cases.
- Updates require `MEKUGI_UPDATE_UI_SNAPSHOTS=1`; check recipes clear it. A pass or explicit update
  removes stale candidates. Rerun checks after accepting a baseline.
- Keep copy/state/bounds/interaction/PTY checks separate. Git attributes exempt only snapshot text
  from trailing-space/final-blank-row warnings; ordinary sources still use `git diff --check`.
