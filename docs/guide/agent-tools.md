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
| `msymbol` | Batched definitions/references | Go: `gopls`; JS/TS/JSON: TypeScript 7 `tsc`; Python: `pyright-langserver`; Rust: see below |
| `inspect_file` | Structural outlines | None |

```sh
mchanges amber1..amber3 --summary
mchanges revert amber2
mcat --number source.ts 10-20 40:60
mread REF
```

### Rust semantic queries

Rust queries require a working `rust-analyzer`, the Cargo project's selected installed
Cargo/Rust toolchain, matching `rust-src`, cached dependencies and a current `Cargo.lock`.
A rustup analyzer shim alone does not establish that the analyzer is installed.
Prepare dependencies separately, from the Cargo project directory:

```sh
rustup component add rust-src
cargo fetch --locked
```

If the analyzer reports missing standard-library metadata dependencies, prepare that
selected toolchain's source dependency cache too. Its manifest may require Cargo's
unstable manifest features even on a stable toolchain; fetching does not build it:

```sh
RUSTC_BOOTSTRAP=1 cargo fetch --locked --manifest-path "$(rustc --print sysroot)/lib/rustlib/src/rust/library/Cargo.toml"
```

Queries themselves are offline and install nothing. Build scripts, procedural macros
and checks stay disabled. Results apply to the selected target and default features;
incomplete coverage is nonzero even when useful source rows are returned. Large cold
queries can exceed the shared 30-second deadline. See the
[semantic-query contract](../../modules/symbolquery/SPEC.md#rust-cargo-analysis).

## Captured changes

Captured edits are not a live Git diff. Named ignored files are included; unknown-target
commands capture observed effects, possibly from other writers. Previews are
provisional; missing evidence is labeled. Ignored untracked files require explicit named
targets to be retained and reverted/reapplied. Revert merges later edits, may create conflicts, and
is captured too. See [changes](../../modules/changehistory/SPEC.md), [capture coverage](../contracts/change-evidence.md), and [retained reads](../../modules/outputrecovery/SPEC.md).
