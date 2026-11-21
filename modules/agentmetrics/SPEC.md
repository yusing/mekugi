# Agent-experience evidence

## Requirements

Inherited from [product requirements](../../docs/product.md#user-requirements): support optional,
non-invasive offline inspection of executed reader/edit/command evidence without affecting execution
outcomes or mixing judgments with provider billing.

## Current approach (replaceable)

- Native `mcat`, `msymbol`, and `mfinspect` helpers append content-free `mekugi.ax.read.v2` JSONL
  read events when `MEKUGI_AX_OUTPUT` or `--debug` is active (private regular file, 64 MiB cap,
  cross-process locked; no paths, scripts, or content). Offline readers retain historical
  `inspect_file` compatibility.
- Contributes `--ax`, `--read-log`, and `--defects` reporting to `mekugi inspect-session`, pairing
  read logs, stock-patch re-emission counts, rollout command/completion timings, retained
  [request timing](../transportdiagnostics/SPEC.md), and optional artifact-backed defect verdicts
  without mutating stored state.
- Omitting the module disables AX instrumentation and reports while keeping ordinary execution and
  session inspection available.
