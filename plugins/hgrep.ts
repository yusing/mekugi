import {RetainedRows} from "./retained_output.ts";
import {open, type FileHandle} from "node:fs/promises";
import {lineBounds, lineCount} from "mekugi:core/v1";
import {spawn} from "node:child_process";

import type {Tool} from "../internal/router/toolplugin/plugin.d.ts";
import {
  decodeUTF8,
  VERIFIED_ROW_MAX_TOKENS,
  MAX_POSSIBLE_GPT5_TOKEN_BYTES,
  errorText,
  createExecutorTool,
  readerFailureClass,
  stripOptionalFinalNewline,
  formatReaderRow,
  readerOptions,
  readerLimitDiagnostic,
  VerifiedRowOutput,
} from "./common.ts";

import type {ReaderOptions} from "./common.ts";

// JSON escaping and submatch metadata have their own wire bound, independent
// of the source-row/token limit.
const MAX_EVENT_BYTES = 16 * 1024 * 1024;
const MAX_SOURCE_BYTES = VERIFIED_ROW_MAX_TOKENS * MAX_POSSIBLE_GPT5_TOKEN_BYTES;

const MAX_STDERR_BYTES = 64 * 1024;

const silentLongOptions = new Set([
  "line-number",
  "no-column",
  "no-config",
  "no-heading",
  "no-json",
  "no-max-columns-preview",
  "no-stats",
  "no-trim",
  "with-filename",
]);
const warnedLongOptions = new Map<string, boolean>([
  ["block-buffered", false],
  ["column", false],
  ["count", false],
  ["count-matches", false],
  ["debug", false],
  ["files", false],
  ["files-with-matches", false],
  ["files-without-match", false],
  ["heading", false],
  ["include-zero", false],
  ["json", false],
  ["line-buffered", false],
  ["max-columns-preview", false],
  ["no-filename", false],
  ["no-ignore-messages", false],
  ["no-line-number", false],
  ["no-messages", false],
  ["null", false],
  ["only-matching", false],
  ["passthru", false],
  ["passthrough", false],
  ["pretty", false],
  ["quiet", false],
  ["stats", false],
  ["trace", false],
  ["trim", false],
  ["vimgrep", false],
  ["color", true],
  ["colors", true],
  ["context-separator", true],
  ["field-context-separator", true],
  ["field-match-separator", true],
  ["hyperlink-format", true],
  ["max-columns", true],
  ["path-separator", true],
  ["replace", true],
]);
const forbiddenLongOptions = new Set([
  "binary",
  "encoding",
  "generate",
  "help",
  "hostname-bin",
  "multiline",
  "multiline-dotall",
  "no-binary",
  "no-text",
  "null-data",
  "pcre2-version",
  "pre",
  "pre-glob",
  "search-zip",
  "text",
  "type-list",
  "version",
]);
const longOptionsWithValue = new Set([
  "after-context",
  "before-context",
  "context",
  "dfa-size-limit",
  "engine",
  "file",
  "glob",
  "iglob",
  "ignore-file",
  "max-count",
  "max-depth",
  "max-filesize",
  "regex-size-limit",
  "regexp",
  "sort",
  "sortr",
  "threads",
  "type",
  "type-add",
  "type-clear",
  "type-not",
]);
const silentShortOptions = new Set("HnR");
const warnedShortOptions = new Map<string, boolean>([
  ["0", false],
  ["I", false],
  ["M", true],
  ["N", false],
  ["b", false],
  ["c", false],
  ["h", false],
  ["l", false],
  ["o", false],
  ["p", false],
  ["q", false],
  ["r", true],
]);
const forbiddenShortOptions = new Set("EUVaz");
const shortOptionsWithValue = new Set("ABCTdefgjmt");

type NormalizedArguments = {
  arguments: string[];
  warnings: string[];
};

type JSONText = {
  text?: string;
  bytes?: string;
};

type JSONEvent = {
  type?: string;
  data?: {
    path?: JSONText;
    lines?: JSONText;
    absolute_offset?: number;
  };
};

/**
 * splitArguments parses a shell-quoted ripgrep argument line into individual arguments.
 */
export function splitArguments(rawInput: string): string[] {
  const input = stripOptionalFinalNewline(rawInput);
  if (input === "") {
    throw new Error("input must not be empty");
  }
  const argumentsValue: string[] = [];
  let offset = 0;
  while (offset < input.length) {
    while (input[offset] === " " || input[offset] === "\t") {
      offset += 1;
    }
    if (offset === input.length) {
      break;
    }

    let argument = "";
    while (offset < input.length && input[offset] !== " " && input[offset] !== "\t") {
      const character = input[offset];
      if (character === "\r" || character === "\n") {
        throw new Error("input must contain one argument line");
      }
      if (character === "'" || character === "\"") {
        const quote = character;
        offset += 1;
        while (offset < input.length && input[offset] !== quote) {
          if (input[offset] === "\r" || input[offset] === "\n") {
            throw new Error("quoted argument must not contain a newline");
          }
          if (quote === "\"" && input[offset] === "\\") {
            offset += 1;
            if (offset === input.length) {
              throw new Error("double-quoted argument ends with an escape");
            }
          }
          argument += input[offset];
          offset += 1;
        }
        if (offset === input.length) {
          throw new Error("unterminated quoted argument");
        }
        offset += 1;
        continue;
      }
      if (character === "\\") {
        offset += 1;
        if (offset === input.length) {
          throw new Error("argument ends with an escape");
        }
        argument += input[offset];
        offset += 1;
        continue;
      }
      argument += character;
      offset += 1;
    }
    argumentsValue.push(argument);
  }
  if (argumentsValue.length === 0) {
    throw new Error("input must contain at least one argument");
  }
  return argumentsValue;
}

/**
 * normalizeArguments filters ripgrep arguments for verified-row output compatibility.
 */
function normalizeArguments(input: string[]): NormalizedArguments {
  let options = true;
  let patternFromOption = false;
  let positionals = 0;
  const normalized: string[] = [];
  const warnings: string[] = [];
  const warned = new Set<string>();
  const addWarning = (option: string): void => {
    if (!warned.has(option)) {
      warned.add(option);
      warnings.push(option);
    }
  };

  for (let index = 0; index < input.length; index += 1) {
    const argument = input[index];
    if (options && argument === "--") {
      options = false;
      normalized.push(argument);
      continue;
    }
    if (!options || argument === "-" || !argument.startsWith("-")) {
      positionals += 1;
      normalized.push(argument);
      continue;
    }
    if (argument.startsWith("--")) {
      const option = argument.slice(2);
      const separator = option.indexOf("=");
      const name = separator < 0 ? option : option.slice(0, separator);
      const attached = separator >= 0;
      if (silentLongOptions.has(name)) {
        continue;
      }
      const warnedHasValue = warnedLongOptions.get(name);
      if (warnedHasValue !== undefined) {
        addWarning(`--${name}`);
        if (warnedHasValue && !attached) {
          if (index + 1 === input.length) {
            throw new Error(`ripgrep option --${name} requires a value`);
          }
          index += 1;
        }
        continue;
      }
      if (forbiddenLongOptions.has(name)) {
        throw new Error(`ripgrep option --${name} is incompatible with verified-row output`);
      }
      normalized.push(argument);
      if (name === "regexp" || name === "file") {
        patternFromOption = true;
      }
      if (longOptionsWithValue.has(name) && !attached) {
        if (index + 1 === input.length) {
          throw new Error(`ripgrep option --${name} requires a value`);
        }
        index += 1;
        normalized.push(input[index]);
      }
      continue;
    }

    const short = [...argument.slice(1)];
    let kept = "";
    let keptValue: string | null = null;
    for (let offset = 0; offset < short.length; offset += 1) {
      const option = short[offset];
      if (silentShortOptions.has(option)) {
        continue;
      }
      const warnedHasValue = warnedShortOptions.get(option);
      if (warnedHasValue !== undefined) {
        addWarning(`-${option}`);
        if (warnedHasValue) {
          if (offset === short.length - 1) {
            if (index + 1 === input.length) {
              throw new Error(`ripgrep option -${option} requires a value`);
            }
            index += 1;
          }
          break;
        }
        continue;
      }
      if (forbiddenShortOptions.has(option)) {
        throw new Error(`ripgrep option -${option} is incompatible with verified-row output`);
      }
      kept += option;
      if (option === "e" || option === "f") {
        patternFromOption = true;
      }
      if (shortOptionsWithValue.has(option)) {
        if (offset === short.length - 1) {
          if (index + 1 === input.length) {
            throw new Error(`ripgrep option -${option} requires a value`);
          }
          index += 1;
          keptValue = input[index];
        } else {
          kept += short.slice(offset + 1).join("");
        }
        break;
      }
    }
    if (kept !== "") {
      normalized.push(`-${kept}`);
      if (keptValue !== null) {
        normalized.push(keptValue);
      }
    }
  }

  if (patternFromOption) {
    if (positionals === 0) {
      normalized.push(".");
    }
    return {arguments: normalized, warnings};
  }
  if (positionals === 0) {
    throw new Error("ripgrep search requires a pattern");
  }
  if (positionals === 1) {
    normalized.push(".");
  }
  return {arguments: normalized, warnings};
}

/**
 * decodeJSONText extracts text or base64-encoded bytes from ripgrep JSON output.
 */
function decodeJSONText(value: JSONText | undefined, label: string): string {
  if (typeof value?.text === "string") {
    return value.text;
  }
  if (typeof value?.bytes !== "string" || value.bytes === "") {
    throw new Error(`rg ${label} has no text`);
  }
  if (!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u.test(value.bytes)) {
    throw new Error(`decode base64 ${label}`);
  }
  return decodeUTF8(Buffer.from(value.bytes, "base64"), `rg ${label}`);
}

/**
 * collectStderr accumulates stderr output up to the maximum retained size.
 */
async function collectStderr(stream: AsyncIterable<Uint8Array>): Promise<string> {
  const chunks: Buffer[] = [];
  let retained = 0;
  for await (const chunk of stream) {
    if (retained >= MAX_STDERR_BYTES) {
      continue;
    }
    const buffer = Buffer.from(chunk);
    const keep = buffer.subarray(0, MAX_STDERR_BYTES - retained);
    chunks.push(keep);
    retained += keep.length;
  }
  return Buffer.concat(chunks).toString("utf8");
}

/**
 * conciseDiagnostic extracts the most relevant error message from ripgrep stderr.
 */
function conciseDiagnostic(diagnostic: string): string {
  const lines = diagnostic.split("\n");
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    const line = lines[index].trim();
    if (line === "") {
      continue;
    }
    const operationMarker = ": IO error for operation on ";
    const operationStart = line.indexOf(operationMarker);
    if (line.startsWith("rg: ") && operationStart >= 0) {
      const path = line.slice("rg: ".length, operationStart);
      const messageStart = `${operationMarker}${path}: `;
      const details = line.slice(operationStart);
      if (details.startsWith(messageStart)) {
        return details.slice(messageStart.length);
      }
    }
    // Older ripgrep versions omit the repeated operation path.
    const osError = /^rg: .*: ([^:\r\n]+ \(os error [0-9]+\))$/u.exec(line);
    if (osError !== null) {
      return osError[1];
    }
    return line;
  }
  return diagnostic;
}

// Walk the actual bytes monotonically, rather than trusting rg's LF-only row
// numbering. Retain only a read buffer and the current bounded event span.
class SearchSource {
  private offset = 0;
  private row = 1;
  private previousCR = false;
  private readonly decoder = new TextDecoder("utf-8", {fatal: true, ignoreBOM: true});
  private readonly buffer = Buffer.alloc(64 * 1024);

  constructor(private readonly handle: FileHandle) {}

  async advance(end: number, retain = false): Promise<Buffer> {
    if (end < this.offset) {
      throw new Error("rg returned out-of-order source offsets");
    }
    const chunks: Buffer[] = [];
    while (this.offset < end) {
      const {bytesRead} = await this.handle.read(this.buffer, 0, Math.min(this.buffer.length, end - this.offset), this.offset);
      if (bytesRead === 0) {
        if (end !== Infinity) {
          throw new Error("rg source changed during search");
        }
        this.decoder.decode();
        break;
      }
      const bytes = this.buffer.subarray(0, bytesRead);
      try {
        this.decoder.decode(bytes, {stream: true});
      } catch {
        throw new Error("search source is not UTF-8");
      }
      for (const byte of bytes) {
        if (byte === 13 || (byte === 10 && !this.previousCR)) {
          this.row += 1;
        }
        this.previousCR = byte === 13;
      }
      this.offset += bytesRead;
      if (retain) {
        chunks.push(Buffer.from(bytes));
      }
    }
    return Buffer.concat(chunks);
  }

  async read(offset: number, expected: Buffer): Promise<number> {
    await this.advance(offset);
    const row = this.row;
    const actual = await this.advance(offset + expected.length, true);
    if (!actual.equals(expected)) {
      throw new Error("rg result does not match source bytes");
    }
    return row;
  }

  async close(): Promise<void> {
    await this.handle.close();
  }
}

type ComparedOutput = {
  current: string;
  incomplete: boolean;
  limitReason?: string;
};

/**
 * runRipgrep executes ripgrep with verified-row output and token-budget enforcement.
 */
async function runRipgrep(argumentsValue: string[], options: ReaderOptions): Promise<ComparedOutput> {
  const child = spawn("rg", ["--json", "--no-config", "--encoding", "none", ...argumentsValue], {
    stdio: ["ignore", "pipe", "pipe"],
  });
  const completion = new Promise<number | null>((resolve, reject) => {
    child.once("error", reject);
    child.once("close", (code) => resolve(code));
  });
  const stderrPromise = collectStderr(child.stderr);

  const output = new VerifiedRowOutput(options.maxTokens);
  const retainedRows = new RetainedRows();
  let pending: Buffer[] = [];
  let pendingBytes = 0;
  const seen = new Set<string>();
  const sources = new Map<string, SearchSource>();
  let limitReason: string | undefined;
  const processEvent = async (raw: Buffer): Promise<boolean> => {
    let event: JSONEvent;
    try {
      event = JSON.parse(decodeUTF8(raw, "rg output"));
    } catch (error) {
      throw new Error(`decode rg output: ${errorText(error)}`);
    }
    if (event.type === "end") {
      const path = decodeJSONText(event.data?.path, "path");
      const source = sources.get(path);
      if (source) {
        await source.advance(Infinity);
        await source.close();
        sources.delete(path);
      }
      return true;
    }
    if (event.type !== "match" && event.type !== "context") {
      return true;
    }
    const path = decodeJSONText(event.data?.path, "path");
    const bytes = Buffer.from(decodeJSONText(event.data?.lines, "result"), "utf8");
    if (bytes.length > MAX_SOURCE_BYTES) {
      output.incomplete = true;
      limitReason = `output incomplete: rg source event exceeds the ${MAX_SOURCE_BYTES}-byte inspection bound\n`;
      return false;
    }
    const offset = event.data?.absolute_offset;
    if (!Number.isSafeInteger(offset) || offset < 0) {
      throw new Error("rg returned an invalid source offset");
    }
    let source = sources.get(path);
    if (!source) {
      const handle = await open(path);
      if (!(await handle.stat()).isFile()) {
        await handle.close();
        throw new Error("search source is not a regular file");
      }
      source = new SearchSource(handle);
      sources.set(path, source);
    }
    const firstRow = await source.read(offset, bytes);
    const count = lineCount(bytes);
    for (let index = 1; index <= count; index += 1) {
      const bounds = lineBounds(bytes, index)!;
      const lineNumber = firstRow + index - 1;
      const key = `${path}\u0000${lineNumber}`;
      if (seen.has(key)) {
        continue;
      }
      seen.add(key);
      const content = decodeUTF8(bytes.subarray(bounds.byteStart, bounds.byteContentEnd), "rg result");
      const row = formatReaderRow(lineNumber, content, options, path);
      if (!retainedRows.append(row)) {
        output.incomplete = true;
        limitReason = "output incomplete: result exceeds the 16 MiB retention bound\n";
        return false;
      }
      if (!output.incomplete) {
        output.append(row);
      }
    }
    return true;
  };
  const takePending = (): Buffer => {
    const raw = pending.length === 1 ? pending[0] : Buffer.concat(pending, pendingBytes);
    pending = [];
    pendingBytes = 0;
    return raw;
  };

  let exitCode: number | null;
  let stderr: string;
  try {
    outer: for await (const rawChunk of child.stdout) {
      const chunk = Buffer.from(rawChunk);
      let offset = 0;
      while (offset < chunk.length) {
        const newline = chunk.indexOf(0x0a, offset);
        const end = newline < 0 ? chunk.length : newline + 1;
        const fragment = chunk.subarray(offset, end);
        if (pendingBytes + fragment.length > MAX_EVENT_BYTES) {
          output.incomplete = true;
          limitReason = `output incomplete: rg JSON event exceeds the ${MAX_EVENT_BYTES}-byte wire bound\n`;
          pending = [];
          pendingBytes = 0;
          child.kill("SIGKILL");
          break outer;
        }
        pending.push(fragment);
        pendingBytes += fragment.length;
        offset = end;
        if (newline < 0) {
          break;
        }
        if (!(await processEvent(takePending()))) {
          child.kill("SIGKILL");
          break outer;
        }
      }
    }
    if (limitReason === undefined && pendingBytes !== 0 && !(await processEvent(takePending()))) {
      child.kill("SIGKILL");
    }
    exitCode = await completion;
    stderr = await stderrPromise;
  } catch (error) {
    child.kill("SIGKILL");
    await completion.catch(() => null);
    await stderrPromise.catch(() => "");
    throw error;
  } finally {
    await Promise.all([...sources.values()].map((source) => source.close()));
  }

  if (limitReason !== undefined) {
    return {current: output.current, incomplete: true, limitReason};
  }
  if (exitCode === 0 || exitCode === 1) {
    if (output.incomplete) {
      const retainedPath = retainedRows.save();
      const unreadLine = output.current.split("\n").length;
      const notice = `hgrep: complete emitted rows retained at ${JSON.stringify(retainedPath)}; unread result rows start at ${unreadLine}. Read bounded ranges with sed; do not rerun rg. Files remain until removed.\n`;
      return {current: output.current, incomplete: true, limitReason: readerLimitDiagnostic(options) + notice};
    }
    return {current: output.current, incomplete: false};
  }
  const diagnostic = conciseDiagnostic(stderr.trim());
  if (diagnostic !== "") {
    throw new Error(diagnostic);
  }
  throw new Error(`execute rg: exit status ${exitCode ?? "unknown"}`);
}

/**
 * createHGrepTool creates the hgrep tool with bounded verified-row output.
 */
export function createHGrepTool(description: string, grammar: string): Tool<string[]> {
  return createExecutorTool({
    name: "hgrep",
    description,
    grammar,
    argv(input) {
      return splitArguments(input);
    },
    async execute(argv) {
      let options: ReaderOptions;
      let normalized: NormalizedArguments;
      try {
        const parsed = readerOptions(argv);
        options = parsed.options;
        normalized = normalizeArguments(parsed.rest);
      } catch (error) {
        return {stderr: `hgrep: ${errorText(error)}\n`, exitCode: 1, failureClass: "invalid_arguments"};
      }
      const warning = normalized.warnings.length === 0
        ? ""
        : `hgrep: warning: ignoring ripgrep options ${normalized.warnings.join(", ")}; output remains verified rows\n`;
      try {
        const result = await runRipgrep(normalized.arguments, options);
        const limitDiagnostic = result.incomplete
          ? `hgrep: ${result.limitReason ?? readerLimitDiagnostic(options)}`
          : "";
        const stderr = `${warning}${limitDiagnostic}`;
        return {
          stdout: result.current,
          stderr,
          exitCode: result.incomplete ? 1 : 0,
          ...(result.incomplete ? {failureClass: "output_limit" as const} : {}),
        };
      } catch (error) {
        return {stderr: `${warning}hgrep: ${errorText(error)}\n`, exitCode: 1, failureClass: readerFailureClass(error, "search_error")};
      }
    },
  });
}
