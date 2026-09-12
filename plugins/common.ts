import {formatVerifiedRow, hashLine} from "mekugi:core/v1";
import path from "node:path";
import {countGPT5Tokens} from "./tokens.ts";
export {countGPT5Tokens, MAX_POSSIBLE_GPT5_TOKEN_BYTES} from "./tokens.ts";
import type {ExecutionContext, ExecutionResult, ReaderFailureClass, Tool, TranslationContext} from "../internal/router/toolplugin/plugin.d.ts";

const VERIFIED_ROW_SOFT_TOKENS = 15_000;
export const VERIFIED_ROW_MAX_TOKENS = 15_500;
export const VERIFIED_ROW_LIMIT_DIAGNOSTIC = "output incomplete: 15,000-token limit reached\n";

export function byteLength(value: string): number {
  return Buffer.byteLength(value, "utf8");
}

export function isOutsideWorkspace(root: string, target: string): boolean {
  const relative = path.relative(root, target);
  return relative === ".." || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative);
}

export type ReaderOptions = {maxTokens?: number; previewBytes?: number; tail?: boolean; maxLines?: number};

export function readerOptions(argv: string[], allowTail = false): {options: ReaderOptions; rest: string[]; offset: number} {
  const options: ReaderOptions = {};
  let offset = 0;
  while (argv[offset] === "--max-tokens" || argv[offset] === "--preview-bytes"
      || (allowTail && (argv[offset] === "--tail" || argv[offset] === "-n"))) {
    const name = argv[offset];
    if (name === "--tail") {
      if (options.tail) {
        throw new Error("--tail cannot repeat");
      }
      options.tail = true;
      offset += 1;
      continue;
    }
    const key = name === "-n" ? "maxLines" : name === "--max-tokens" ? "maxTokens" : "previewBytes";
    const maximum = key === "maxLines" ? Number.MAX_SAFE_INTEGER : key === "maxTokens" ? VERIFIED_ROW_MAX_TOKENS : 65_536;
    const raw = argv[offset + 1] ?? "";
    const value = Number(raw);
    if (options[key] !== undefined || !/^[1-9][0-9]*$/u.test(raw)
        || !Number.isSafeInteger(value) || value > maximum) {
      throw new Error(`${name} requires one integer from 1 to ${maximum} and cannot repeat`);
    }
    options[key] = value;
    offset += 2;
  }
  if (options.tail && options.maxTokens === undefined && options.maxLines === undefined) {
    throw new Error("--tail requires -n or --max-tokens");
  }
  return {options, rest: argv.slice(offset), offset};
}

export function readerLimitDiagnostic(options: ReaderOptions): string {
  if (options.maxLines !== undefined) {
    return `output incomplete: ${options.maxLines}-line limit${options.maxTokens === undefined ? "" : ` or ${options.maxTokens}-token limit`} reached\n`;
  }
  return options.maxTokens === undefined
    ? VERIFIED_ROW_LIMIT_DIAGNOSTIC
    : `output incomplete: ${options.maxTokens}-token limit reached\n`;
}

export function utf8SourcePrefix(content: string, maxBytes: number): {text: string; source_bytes: number; omitted_bytes: number} {
  const bytes = Buffer.from(content, "utf8");
  let end = Math.min(bytes.length, maxBytes);
  while (end > 0 && end < bytes.length && (bytes[end] & 0xc0) === 0x80) {
    end -= 1;
  }
  return {
    text: bytes.subarray(0, end).toString("utf8"),
    source_bytes: bytes.length,
    omitted_bytes: bytes.length - end,
  };
}

// Preview records never impersonate exact source rows. Their identity still
// hashes the entire logical row through the portable core.
export function formatReaderRow(line: number, content: string, options: ReaderOptions, path?: string): string {
  if (options.previewBytes === undefined) {
    return `${path === undefined ? "" : `${JSON.stringify(path)}:`}${formatVerifiedRow(line, content)}`;
  }
  const {text: preview, source_bytes, omitted_bytes} = utf8SourcePrefix(content, options.previewBytes);
  return `${JSON.stringify({
    ...(path === undefined ? {} : {path}),
    row: `${line}:${hashLine(content)}`,
    preview,
    source_bytes,
    omitted_bytes,
  })}\n`;
}

export class VerifiedRowOutput {
  current = "";
  incomplete = false;
  #sealed = false;

  constructor(private readonly maxTokens?: number) {}

  append(currentRow: string): boolean {
    if (this.#sealed) {
      this.incomplete = true;
      return false;
    }
    const candidate = this.current + currentRow;
    const tokens = countGPT5Tokens(candidate);
    if (tokens > (this.maxTokens ?? VERIFIED_ROW_MAX_TOKENS)) {
      this.incomplete = true;
      return false;
    }
    this.current = candidate;
    this.#sealed = this.maxTokens === undefined && tokens > VERIFIED_ROW_SOFT_TOKENS;
    return true;
  }
}

export function errorText(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }
  return String(error);
}

export function decodeUTF8(value: Uint8Array, label: string): string {
  try {
    return new TextDecoder("utf-8", {fatal: true, ignoreBOM: true}).decode(value);
  } catch {
    throw new Error(`${label} is not UTF-8`);
  }
}

export function stripOptionalFinalNewline(value: string): string {
  if (value.endsWith("\r\n")) {
    return value.slice(0, -2);
  }
  if (value.endsWith("\n")) {
    return value.slice(0, -1);
  }
  return value;
}

type ExecutorToolOptions = {
  name: string;
  description: string;
  grammar: string;
  argv(input: string, context: TranslationContext): string[] | Promise<string[]>;
  execute(argv: string[], context: ExecutionContext): ExecutionResult | Promise<ExecutionResult>;
};

export function createExecutorTool(options: ExecutorToolOptions): Tool<string[]> {
  return {
    specification: {
      type: "custom",
      name: options.name,
      description: options.description,
      format: {type: "grammar", syntax: "regex", definition: options.grammar},
    },
    parse(input, context) {
      return options.argv(input, context);
    },
    argv(input) {
      return input;
    },
    translate(input, api) {
      return api.exec();
    },
    execute: options.execute,
  };
}

export async function collect(stream: AsyncIterable<Uint8Array>): Promise<Uint8Array> {
  const chunks: Uint8Array[] = [];
  let length = 0;
  try {
    for await (const chunk of stream) {
      chunks.push(chunk);
      length += chunk.byteLength;
    }
  } catch (error) {
    if (!(error instanceof Error) || !("code" in error) || error.code !== "ERR_STREAM_PREMATURE_CLOSE") {
      throw error;
    }
  }
  return Buffer.concat(chunks, length);
}

// Classify structured filesystem errors; never inspect or retain diagnostic text.
export function readerFailureClass(error: unknown, fallback: ReaderFailureClass = "reader_error"): ReaderFailureClass {
  if (error instanceof Error && "code" in error) {
    switch (error.code) {
      case "ENOENT": case "ENOTDIR": return "not_found";
      case "EACCES": case "EPERM": return "permission_denied";
      case "EISDIR": return "not_regular";
    }
  }
  return fallback;
}
