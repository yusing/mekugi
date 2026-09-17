import {RetainedRows} from "./retained_output.ts";
import {constants} from "node:fs";
import {open} from "node:fs/promises";

import type {Tool} from "../internal/router/toolplugin/plugin.d.ts";
import {
  decodeQuotedOperand,
  parsePositiveInteger,
} from "mekugi:core/v1";

import {
  countGPT5Tokens,
  byteLength,
  errorText,
  createExecutorTool,
  readerFailureClass,
  MAX_POSSIBLE_GPT5_TOKEN_BYTES,
  stripOptionalFinalNewline,
  formatReaderRow,
  readerArguments,
  readerOptions,
  readerLimitDiagnostic,
  VERIFIED_ROW_MAX_TOKENS,
  VerifiedRowOutput,
} from "./common.ts";

import type {ReaderOptions} from "./common.ts";

const READ_BUFFER_BYTES = 32 * 1024;

/**
 * conciseErrorText extracts a concise error message without syscall details.
 */
function conciseErrorText(error: unknown): string {
  const message = errorText(error);
  if (!(error instanceof Error) || !("syscall" in error) || typeof error.syscall !== "string") {
    return message;
  }
  const detailsStart = message.indexOf(`, ${error.syscall}`);
  return detailsStart < 0 ? message : message.slice(0, detailsStart);
}

type ReadSpec = {
  path: string;
  startLine: number;
  endLine: number;
};


/**
 * parseQuotedPath decodes a quoted hcat path operand and returns the unconsumed trailing text.
 */
function parseQuotedPath(input: string): {path: string; trailing: string} {
  try {
    const decoded = decodeQuotedOperand(input);
    return {path: decoded.value, trailing: decoded.rest};
  } catch (error) {
    throw new Error(`invalid hcat path: ${errorText(error)}`);
  }
}

/**
 * parseReadSpec parses an hcat input specification into path and optional line range.
 */
function parseReadSpec(input: string): ReadSpec {
  let path;
  let trailing;
  if (input.startsWith("\"")) {
    ({path, trailing} = parseQuotedPath(input));
  } else {
    const separator = input.indexOf(" ");
    path = separator < 0 ? input : input.slice(0, separator);
    trailing = separator < 0 ? "" : input.slice(separator);
    if (/[\u0000-\u0020"]/u.test(path)) {
      throw new Error("invalid bare hcat path");
    }
  }
  if (path === "") {
    throw new Error("hcat path must not be empty");
  }
  if (trailing === "") {
    return {path, startLine: 0, endLine: 0};
  }
  const match = trailing.match(/^ (0|[1-9][0-9]*):([1-9][0-9]*)$/u);
  if (match === null) {
    throw new Error("hcat input must be PATH or PATH START:END");
  }
  let requestedStartLine;
  let endLine;
  try {
    requestedStartLine = match[1] === "0" ? 0 : parsePositiveInteger(match[1]);
  } catch {
    throw new Error("hcat start line is out of range");
  }
  try {
    endLine = parsePositiveInteger(match[2]);
  } catch {
    throw new Error("hcat end line is out of range");
  }
  if (requestedStartLine > endLine) {
    throw new Error("hcat line range start exceeds end");
  }
  return {path, startLine: Math.max(1, requestedStartLine), endLine};
}


// Retain a byte-bounded suffix while scanning, then tokenize only the final
// candidates. Counting a full token window on every source row is unnecessary.
class VerifiedRowTail {
  #rows: string[] = [];
  #head = 0;
  #bytes = 0;
  incomplete = false;

  constructor(private readonly maxTokens?: number, private readonly maxLines?: number) {}

  append(row: string): void {
    this.#rows.push(row);
    this.#bytes += byteLength(row);
    while ((this.maxLines !== undefined && this.#rows.length - this.#head > this.maxLines)
        || (this.maxTokens !== undefined && this.#bytes > this.maxTokens * MAX_POSSIBLE_GPT5_TOKEN_BYTES)) {
      this.#bytes -= byteLength(this.#rows[this.#head]);
      this.#rows[this.#head++] = "";
      this.incomplete = true;
    }
    if (this.#head > 1024) {
      this.#rows = this.#rows.slice(this.#head);
      this.#head = 0;
    }
  }

  omitRow(): void {
    this.#rows = [];
    this.#head = 0;
    this.#bytes = 0;
    this.incomplete = true;
  }

  finish(): {current: string; incomplete: boolean} {
    if (this.maxTokens === undefined) {
      return {current: this.#rows.slice(this.#head).join(""), incomplete: this.incomplete};
    }
    let current = "";
    for (let index = this.#rows.length - 1; index >= this.#head; index -= 1) {
      const candidate = this.#rows[index] + current;
      if (this.maxTokens !== undefined && countGPT5Tokens(candidate) > this.maxTokens) {
        this.incomplete = true;
        break;
      }
      current = candidate;
    }
    return {current, incomplete: this.incomplete};
  }
}

type ComparedOutput = {
  current: string;
  incomplete: boolean;
  limitReason?: string;
  warning?: string;
  omitted?: string;
};


/**
 * readHashLines reads verified-row output from a file with token-budget enforcement.
 */
async function readHashLines(spec: ReadSpec, options: ReaderOptions): Promise<ComparedOutput> {
  const handle = await open(spec.path, constants.O_RDONLY | (constants.O_NONBLOCK ?? 0));

  try {
    const info = await handle.stat();
    if (!info.isFile()) {
      throw new Error("not a regular file");
    }

    const wholeFile = spec.startLine === 0 && spec.endLine === 0;
    let lineNumber = 1;
    let lineOpen = false;
    let pendingCR = false;
    let content = "";
    let limitReason: string | undefined;
    const retained = new RetainedRows();
    let retentionUnavailable = false;
    let contentBytes = 0;
    const tail = options.tail ? new VerifiedRowTail(options.maxTokens, options.maxLines) : undefined;
    let oversizedRow = false;
    const output = new VerifiedRowOutput(options.maxTokens);

    let selectedLines = 0;
    let lineOutput = "";
    const selected = () => wholeFile
      || (lineNumber >= spec.startLine && lineNumber <= spec.endLine);
    const appendContent = (text: string): void => {
      if (text === "") {
        return;
      }
      lineOpen = true;
      if (!selected() || oversizedRow || (retentionUnavailable && !tail && output.incomplete)) {
        return;
      }
      contentBytes += byteLength(text);
      if (!(options.maxLines !== undefined && options.maxTokens === undefined && options.previewBytes === undefined
          && (tail || selectedLines < options.maxLines))
          && contentBytes > VERIFIED_ROW_MAX_TOKENS * MAX_POSSIBLE_GPT5_TOKEN_BYTES) {
        // Bound candidate storage even when only a preview will be emitted.
        limitReason = `row ${lineNumber} exceeds the ${VERIFIED_ROW_MAX_TOKENS * MAX_POSSIBLE_GPT5_TOKEN_BYTES}-byte inspection bound; use a byte-window reader\n`;
        retentionUnavailable = true;
        content = "";
        oversizedRow = true;
        if (tail) {
          tail.omitRow();
        } else {
          output.incomplete = true;
        }
        return;
      }
      content += text;
    };
    const finishLine = (): void => {
      if (selected() && !oversizedRow && !(retentionUnavailable && !tail && output.incomplete)) {
        selectedLines += 1;
        const row = formatReaderRow(lineNumber, content, options);
        if (!retentionUnavailable && !retained.append(row)) {
          retentionUnavailable = true;
          limitReason = "result exceeds the 16 MiB recovery bound; narrow the source range\n";
        }
        if (!tail && options.maxLines !== undefined && selectedLines > options.maxLines) {
          output.incomplete = true;
        } else {
          if (tail) {
            tail.append(row);
          } else if (options.maxLines !== undefined && options.maxTokens === undefined) {
            lineOutput += row;
          } else if (!output.incomplete) {
            output.append(row);
          }
        }
      }
      oversizedRow = false;
      content = "";
      contentBytes = 0;
      lineNumber += 1;
      lineOpen = false;
    };
    const consume = (text: string): void => {
      let offset = 0;
      while (offset < text.length) {
        if (pendingCR) {
          pendingCR = false;
          finishLine();
          if (text[offset] === "\n") {
            offset += 1;
            continue;
          }
        }
        const cr = text.indexOf("\r", offset);
        const lf = text.indexOf("\n", offset);
        let end = text.length;
        if (cr >= 0) {
          end = cr;
        }
        if (lf >= 0 && lf < end) {
          end = lf;
        }
        appendContent(text.slice(offset, end));
        if (end === text.length) {
          break;
        }
        lineOpen = true;
        if (text[end] === "\r") {
          pendingCR = true;
        } else {
          finishLine();
        }
        offset = end + 1;
      }
    };

    const decoder = new TextDecoder("utf-8", {fatal: true, ignoreBOM: true});
    const stream = handle.createReadStream({
      autoClose: false,
      highWaterMark: READ_BUFFER_BYTES,
    });
    try {
      for await (const chunk of stream) {
        consume(decoder.decode(chunk, {stream: true}));
      }
      consume(decoder.decode());
    } catch (error) {
      if (error instanceof TypeError) {
        throw new Error("not UTF-8");
      }
      throw error;
    } finally {
      stream.destroy();
    }

    if (lineOpen) {
      finishLine();
    }
    const lineCount = lineNumber - 1;
    if (spec.startLine > lineCount) {
      throw new Error(`start line ${spec.startLine} is past EOF (${lineCount} lines)`);
    }
    const missingStartLine = Math.max(spec.startLine, lineCount + 1);
    const warning = !wholeFile && missingStartLine <= spec.endLine
      ? `hcat: ${missingStartLine}-${spec.endLine}: [out of range]\n`
      : undefined;
    const result = tail?.finish() ?? {current: options.maxLines !== undefined && options.maxTokens === undefined ? lineOutput : output.current, incomplete: output.incomplete};
    const omitted = result.incomplete && !retentionUnavailable
      ? tail ? retained.prefixBefore(result.current) : retained.remainder(result.current)
      : undefined;
    return {...result, incomplete: result.incomplete, warning, limitReason, omitted};
  } finally {
    await handle.close();
  }
}


/**
 * hcatArguments converts parsed hcat input to the internal argv representation.
 */
function hcatArguments(input: string): {argv: string[]; pathIndex: number} {
  const argv = readerArguments(input);
  const parsed = readerOptions(argv, true);
  const operands = parsed.rest;
  const spec = parseReadSpec(hcatInput(operands));
  const pathIndex = parsed.indices[0];
  if (spec.startLine !== 0) argv[parsed.indices[1]] = `${spec.startLine}:${spec.endLine}`;
  return {argv, pathIndex};
}


/**
 * hcatInput reconstructs the canonical input specification from argv.
 */
function hcatInput(argv: string[]): string {
  if (argv.length === 1 && argv[0] !== "") {
    return JSON.stringify(argv[0]);
  }
  if (
    argv.length === 2 &&
    argv[0] !== "" &&
    /^(?:0|[1-9][0-9]*):[1-9][0-9]*$/u.test(argv[1])
  ) {
    return `${JSON.stringify(argv[0])} ${argv[1]}`;
  }
  throw new Error("hcat expected PATH or PATH START:END");
}


/**
 * createHCatTool creates the hcat tool with bounded verified-row file output.
 */
export function createHCatTool(description: string, grammar: string): Tool<string[]> {
  return createExecutorTool({
    name: "hcat",
    description,
    grammar,
    argv(input) {
      const {argv, pathIndex} = hcatArguments(input);
      // Preserve the parsed boundary after flattening into executor argv.
      // Relative option-like names need a path spelling, not an option spelling.
      const path = argv[pathIndex];
      argv[pathIndex] = path.startsWith("-") ? `./${path}` : path;
      return argv;
    },
    async execute(argv) {
      let options: ReaderOptions;
      let spec: ReadSpec;
      try {
        const parsed = readerOptions(argv, true);
        options = parsed.options;
        spec = parseReadSpec(stripOptionalFinalNewline(hcatInput(parsed.rest)));
      } catch (error) {
        return {stderr: `hcat: ${conciseErrorText(error)}\n`, exitCode: 1, failureClass: "invalid_arguments"};
      }
      try {
        const result = await readHashLines(spec, options);
        const limitDiagnostic = result.incomplete
          ? `hcat: ${result.limitReason ?? readerLimitDiagnostic(options)}`
          : "";
        const stderr = `${result.warning ?? ""}${limitDiagnostic}`;
        return {
          stdout: result.current,
          ...(stderr === "" ? {} : {stderr}),
          exitCode: result.incomplete ? 1 : 0,
          ...(result.omitted === undefined ? {} : {omittedOutput: {stdout: result.omitted, stderr: "", stdoutKind: "rows" as const}}),
          ...(result.incomplete ? {failureClass: "output_limit" as const} : {}),
        };
      } catch (error) {
        return {stderr: `hcat: ${conciseErrorText(error)}\n`, exitCode: 1, failureClass: readerFailureClass(error)};
      }
    },
  });
}
