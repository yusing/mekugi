import {decodeQuotedOperand} from "mekugi:core/v1";
import path from "node:path";
import {countGPT5Tokens} from "./tokens.ts";
export {countGPT5Tokens, MAX_POSSIBLE_GPT5_TOKEN_BYTES} from "./tokens.ts";
import type {ExecutionContext, ExecutionResult, ReaderFailureClass, Tool} from "../internal/router/toolplugin/plugin.d.ts";

export const READ_DEFAULT_TOKENS = 4_000;
export const MAX_READER_TOKENS = 15_500;

export function byteLength(value: string): number {
  return Buffer.byteLength(value, "utf8");
}

export function isOutsideWorkspace(root: string, target: string): boolean {
  const relative = path.relative(root, target);
  return relative === ".." || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative);
}

// Private translation inputs use JSON-quoted operands; executed shell commands
// already arrive as argv and never pass through this parser.
export function readerArguments(input: string): string[] {
  let remaining = stripOptionalFinalNewline(input);
  const result: string[] = [];
  while (remaining !== "") {
    remaining = remaining.replace(/^[ \t]+/u, "");
    if (remaining === "") break;
    if (remaining.startsWith("\"")) {
      const decoded = decodeQuotedOperand(remaining);
      result.push(decoded.value.startsWith("-") ? `./${decoded.value}` : decoded.value);
      if (decoded.rest !== "" && !/^[ \t]/u.test(decoded.rest)) throw new Error("expected an argument separator");
      remaining = decoded.rest;
    } else {
      const match = remaining.match(/^[^ \t]+/u)!;
      if (/[\r\n"]/u.test(match[0])) throw new Error("invalid bare reader argument");
      result.push(match[0]);
      remaining = remaining.slice(match[0].length);
    }
  }
  return result;
}

export type ReaderOptions = {maxTokens?: number; previewBytes?: number; tail?: boolean; maxLines?: number};

export function readerOptions(argv: string[], allowTail = false, takesValue: (arg: string) => boolean = () => false, preserveTerminator = false, allowPreview = true, defaultMaxTokens = READ_DEFAULT_TOKENS): {options: ReaderOptions; rest: string[]; indices: number[]} {
  const options: ReaderOptions = {};
  const rest: string[] = [];
  const indices: number[] = [];
  let offset = 0;
  while (offset < argv.length) {
    const argument = argv[offset];
    const inlineBudget = argument.startsWith("--max-tokens=");
    const name = inlineBudget ? "--max-tokens" : argument;
    if (name === "--") {
      if (!preserveTerminator) offset++;
      rest.push(...argv.slice(offset));
      indices.push(...argv.slice(offset).map((_, i) => offset + i));
      break;
    }
    if (name !== "--max-tokens" && !(allowPreview && name === "--preview-bytes")
        && !(allowTail && (name === "--tail" || name === "-n"))) {
      rest.push(name);
      indices.push(offset++);
      if (takesValue(name) && offset < argv.length) {
        rest.push(argv[offset]);
        indices.push(offset++);
      }
      continue;
    }
    if (name === "--tail") {
      if (options.tail) {
        throw new Error("--tail cannot repeat");
      }
      options.tail = true;
      offset += 1;
      continue;
    }
    const key = name === "-n" ? "maxLines" : name === "--max-tokens" ? "maxTokens" : "previewBytes";
    const maximum = key === "maxLines" ? Number.MAX_SAFE_INTEGER : key === "maxTokens" ? MAX_READER_TOKENS : 65_536;
    const raw = inlineBudget ? argument.slice("--max-tokens=".length) : argv[offset + 1] ?? "";
    const value = Number(raw);
    if (options[key] !== undefined || !/^[1-9][0-9]*$/u.test(raw)
        || !Number.isSafeInteger(value) || value > maximum) {
      throw new Error(`${name} requires one integer from 1 to ${maximum} and cannot repeat`);
    }
    options[key] = value;
    offset += inlineBudget ? 1 : 2;
  }
  if (options.tail && options.maxTokens === undefined && options.maxLines === undefined) {
    throw new Error("--tail requires -n or --max-tokens");
  }
  if (options.maxTokens === undefined && options.maxLines === undefined) options.maxTokens = defaultMaxTokens;
  return {options, rest, indices};
}

export function readerLimitDiagnostic(options: ReaderOptions): string {
  if (options.maxLines !== undefined) {
    return `output incomplete: ${options.maxLines}-line limit${options.maxTokens === undefined ? "" : ` or ${options.maxTokens}-token limit`} reached\n`;
  }
  return `output incomplete: ${options.maxTokens ?? READ_DEFAULT_TOKENS}-token limit reached\n`;
}

export class BoundedTextOutput {
  current = "";
  incomplete = false;
  constructor(private readonly maxTokens = READ_DEFAULT_TOKENS) {}

  append(currentRow: string): boolean {
    if (this.incomplete) return false;
    const candidate = this.current + currentRow;
    // Each token needs at least one byte, so small candidates need no tokenization.
    const tokens = byteLength(candidate) <= this.maxTokens
      ? byteLength(candidate)
      : countGPT5Tokens(candidate);
    if (tokens > this.maxTokens) {
      this.incomplete = true;
      return false;
    }
    this.current = candidate;
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
  argv(input: string): string[] | Promise<string[]>;
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
    parse(input) {
      return options.argv(input);
    },
    argv(input) {
      return input;
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
