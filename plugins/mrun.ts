import {selectReadOutput} from "./read_output.ts";
import type {NativeTool} from "../internal/router/toolplugin/plugin.d.ts";
import type {ExecutionOutput} from "../internal/router/toolplugin/plugin.d.ts";
import {countGPT5Tokens, encodeGPT5, tokenBytes} from "./tokens.ts";

export function createMRunTool(): NativeTool {
  return {
    specification: {
      type: "custom",
      name: "mrun",
      description: "Bound one foreground command's output, retaining its beginning or end. Use for noisy commands when a bounded head or tail is sufficient; output outside the selected window is discarded. Only an emitted mread reference recovers retained delivery overflow; ordinary exec_command truncation has no mread recovery. Usage: `mrun (-n N|--max-tokens N) [--tail] -- COMMAND [ARG...]`. Stock yielded sessions and write_stdin still own interactive continuation.",
    },
    nativeExecutor: "mrun",
  };
}

export function selectMRunText(value: string, budget: number, tail: boolean): {text: string; tokens: number} {
  if (budget === 0) {
    return {text: "", tokens: 0};
  }
  const tokens = encodeGPT5(value);
  if (tokens.length <= budget) {
    return {text: value, tokens: tokens.length};
  }
  const decoder = new TextDecoder("utf-8", {fatal: true, ignoreBOM: true});
  for (let keep = budget; keep > 0;) {
    let bytes = Buffer.concat((tail ? tokens.slice(-keep) : tokens.slice(0, keep)).map(tokenBytes));
    let text = "";
    // Encoding valid text can still split a character at the selected token edge.
    while (bytes.length > 0) {
      try {
        text = decoder.decode(bytes);
        break;
      } catch {
        bytes = tail ? bytes.subarray(1) : bytes.subarray(0, -1);
      }
    }
    const count = countGPT5Tokens(text);
    if (count <= budget) {
      return {text, tokens: count};
    }
    keep -= Math.max(1, count - budget);
  }
  return {text: "", tokens: 0};
}

function framedTokens(value: string): number {
  return value === "" ? 0 : countGPT5Tokens(JSON.stringify(JSON.stringify(value)));
}

// Account for native-result JSON inside a Code Mode string. Never cut a reader
// row when the display limit, rather than a producer boundary, chooses the end.
function selectShellText(value: string, budget: number): {text: string; tokens: number} {
  let text = selectMRunText(value, budget, false).text;
  for (let tokens = framedTokens(text); tokens > budget; tokens = framedTokens(text)) {
    const smaller = Math.max(0, Math.floor(countGPT5Tokens(text) * budget / tokens) - 1);
    text = selectMRunText(text, smaller, false).text;
  }
  if (text.length < value.length) {
    text = text.slice(0, text.lastIndexOf("\n") + 1);
  }
  return {text, tokens: framedTokens(text)};
}

// Shared output-formatting operation. It never starts a command.
export function formatMRunOutput(argv: string[]): ExecutionOutput {
  const [rawBudget, mode, stdout, stderr] = argv;
  const budget = Number(rawBudget);
  if (argv.length !== 4 || !/^[1-9][0-9]*$/u.test(rawBudget)
      || budget > 15_500 || (mode !== "head" && mode !== "tail" && mode !== "shell" && mode !== "read")) {
    throw new Error("invalid mrun output selection");
  }
  if (mode === "read") {
    return {stdout: JSON.stringify(selectReadOutput(JSON.parse(stdout), budget)), exitCode: 0};
  }
  if (mode === "shell") {
    return {stdout: JSON.stringify(selectShellText(stdout, budget)), exitCode: 0};
  }
  const error = selectMRunText(stderr, budget, mode === "tail");
  const output = selectMRunText(stdout, budget - error.tokens, mode === "tail");
  return {stdout: output.text, stderr: error.text, exitCode: 0};
}
