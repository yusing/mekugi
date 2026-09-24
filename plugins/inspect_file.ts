import {MAX_RETAINED_BYTES} from "./retained_output.ts";
import {readFile, stat} from "node:fs/promises";
import path from "node:path";

import type {ExecutionResult, Tool} from "../internal/router/toolplugin/plugin.d.ts";
import {classifySourcePath} from "mekugi:core/v1";
import {
  readerArguments,
  readerOptions,
  READ_DEFAULT_TOKENS,
  countGPT5Tokens,
  byteLength,
  decodeUTF8,
  createExecutorTool,
  BoundedTextOutput,
  errorText,
} from "./common.ts";
import {codeOutline, codeTree} from "./inspect_file_code.ts";
import {
  jsonOutline,
  jsonTree,
  markdownOutline,
  markdownTree,
} from "./inspect_file_document.ts";
import {
  hasParseError,
  parseErrorEntries,
  LineMap,
  type FileKind,
  type Language,
  type LocatedEntry,
  type PublicOutlineEntry,
  type SourceFormat,
} from "./inspect_file_support.ts";

export {declarationRange, goDeclarationRange, symbolOffsets} from "./inspect_file_code.ts";
export {LineMap, type SourceFormat} from "./inspect_file_support.ts";

const OUTPUT_BYTES = 64 * 1024;

type ErrorCode =
  | "usage"
  | "not_found"
  | "not_regular"
  | "not_utf8"
  | "read"
  | "parse"
  | "output_limit";
type InspectionData = {
  path: string;
  kind: FileKind;
  language: Language | null;
  size_bytes: number;
  line_count: number | null;
  parse_complete: boolean;
  outline: PublicOutlineEntry[];
};

class InspectFailure extends Error {
  constructor(readonly code: ErrorCode, message: string) {
    super(message);
  }
}

function inspectionFailureClass(code: ErrorCode): ExecutionResult["failureClass"] {
  return code === "usage" ? "invalid_arguments" : code === "not_found" ? "not_found"
    : code === "output_limit" ? "output_limit" : "reader_error";
}
function ordered(entries: LocatedEntry[]): LocatedEntry[] {
  return entries.sort((left, right) => left.offset - right.offset || left.order - right.order);
}

/**
 * sourceFormat classifies a file path using the shared-core classifier and returns the outline-capable source format.
 */
export function sourceFormat(filePath: string): SourceFormat | null {
  const capabilities = classifySourcePath(filePath);
  if (capabilities === null || capabilities.outline !== true) {
    return null;
  }
  return {
    kind: capabilities.kind,
    language: capabilities.language ?? null,
    ...(capabilities.jsx === true ? {jsx: true} : {}),
  };
}

function publicOutline(lines: LineMap, outline: LocatedEntry[]): PublicOutlineEntry[] {
  const verified = new Set<number>();
  return outline.map(({entry}) => {
    for (const line of [entry.line, entry.line_end]) {
      if (!verified.has(line)) {
        if (lines.logicalLine(line) === null) {
          throw new InspectFailure("parse", `missing line ${line}`);
        }
        verified.add(line);
      }
    }
    return entry;
  });
}

function parseContent(
  source: string,
  format: SourceFormat,
): {parseComplete: boolean; outline: PublicOutlineEntry[]; lineCount: number} {
  const lines = new LineMap(source);
  try {
    if (format.kind === "code") {
      const tree = format.language === "go" || format.language === "typescript" ? undefined : codeTree(source, format);
      const outline = codeOutline(source, lines, format, tree);
      if (tree && format.language !== "typescript") outline.push(...parseErrorEntries(tree, lines));
      return {
        parseComplete: !outline.some(item => item.entry.kind === "parse_error"),
		outline: publicOutline(lines, ordered(outline)),
        lineCount: lines.count,
      };
    }
    if (format.kind === "markdown") {
      const tree = markdownTree(source);
      const outline = markdownOutline(source, lines, tree);
      return {
        parseComplete: !hasParseError(tree) && outline.parseComplete,
		outline: publicOutline(lines, ordered([...outline.entries, ...parseErrorEntries(tree, lines)])),
        lineCount: lines.count,
      };
    }
    const tree = jsonTree(source);
    return {
      parseComplete: !hasParseError(tree),
	  outline: publicOutline(lines, ordered([...jsonOutline(source, lines, tree), ...parseErrorEntries(tree, lines)])),
      lineCount: lines.count,
    };
  } catch (error) {
    throw new InspectFailure("parse", `parser failed: ${errorText(error)}`);
  }
}

function failure(pathValue: string | null, code: ErrorCode, message: string): string {
  return `${JSON.stringify({ok: false, path: pathValue, error: {code, message}})}\n`;
}

function success(data: InspectionData, maxTokens: number): ExecutionResult {
  const complete = `${JSON.stringify({ok: true, data, truncated: false, truncation: null})}\n`;
  const fits = (text: string): boolean => byteLength(text) <= OUTPUT_BYTES && countGPT5Tokens(text) <= maxTokens;
  if (fits(complete)) {
    return {stdout: complete, exitCode: 0};
  }
  const reason = byteLength(complete) > OUTPUT_BYTES ? "output_bytes" : "output_tokens";
  const render = (count: number): string => `${JSON.stringify({
    ok: true,
    data: {...data, outline: data.outline.slice(0, count)},
    truncated: true,
    truncation: {reason, after_entries: count},
  })}\n`;

  let low = 0;
  let high = data.outline.length - 1;
  let selected = -1;
  while (low <= high) {
    const middle = Math.floor((low + high) / 2);
    if (fits(render(middle))) {
      selected = middle;
      low = middle + 1;
    } else {
      high = middle - 1;
    }
  }
  if (selected < 0) {
    throw new InspectFailure("output_limit", "minimum success result exceeds the output budget; increase --max-tokens or shorten the path");
  }
  const stdout = render(selected);
  const omitted = JSON.stringify(data.outline.slice(selected));
  if (byteLength(omitted) > MAX_RETAINED_BYTES) {
    return {stdout, exitCode: 1, failureClass: "output_limit",
      stderr: "inspect_file: recovery unavailable: omitted outline exceeds the 16 MiB recovery bound; use bounded mcat reads\n"};
  }
  return {stdout, exitCode: 1, failureClass: "output_limit",
    omittedOutput: {stdout: omitted, stderr: "", stdoutKind: "json"}};
}

function normalizeInputPath(input: string): string {
  if (input === "" || input.includes("\0")) {
    throw new InspectFailure("usage", "inspect_file expects exactly one usable path");
  }
  return path.normalize(input);
}

function filesystemFailure(error: unknown): InspectFailure {
  if (error instanceof InspectFailure) {
    return error;
  }
  if (error instanceof Error && "code" in error && error.code === "ENOENT") {
    return new InspectFailure("not_found", "path does not exist");
  }
  return new InspectFailure("read", `cannot inspect path: ${errorText(error)}`);
}

async function inspect(input: string): Promise<InspectionData> {
  const normalized = normalizeInputPath(input);
  const target = path.resolve(normalized);

  let info;
  try {
    info = await stat(target);
  } catch (error) {
    throw filesystemFailure(error);
  }
  if (!info.isFile()) {
    throw new InspectFailure("not_regular", "path is not a regular file");
  }

  const resultPath = normalized.split(path.sep).join("/");
  const format = sourceFormat(normalized);
  if (format === null) {
    return {
      path: resultPath,
      kind: "none",
      language: null,
      size_bytes: info.size,
      line_count: null,
      parse_complete: true,
      outline: [],
    };
  }

  let bytes: Uint8Array;
  try {
    bytes = await readFile(target);
  } catch (error) {
    throw filesystemFailure(error);
  }
  let source: string;
  try {
    source = decodeUTF8(bytes, "source file");
  } catch {
    throw new InspectFailure("not_utf8", "supported file is not valid UTF-8");
  }
  const parsed = parseContent(source, format);
  return {
    path: resultPath,
    kind: format.kind,
    language: format.language,
    size_bytes: bytes.byteLength,
    line_count: parsed.lineCount,
    parse_complete: parsed.parseComplete,
    outline: parsed.outline,
  };
}

function displayOutlineName(value: string): string {
  return /[\u0000-\u001f\u007f]/u.test(value) ? JSON.stringify(value) : value;
}

function compactOutline(data: InspectionData): string[] {
  const imports = data.outline.filter(entry => entry.kind === "import");
  let importsShown = false;
  const rows: string[] = [];
  for (const entry of data.outline) {
    if (entry.kind === "import") {
      if (!importsShown) {
        const first = imports.reduce((line, item) => Math.min(line, item.line), Infinity);
        const last = imports.reduce((line, item) => Math.max(line, item.line_end), 0);
        rows.push(`${first}-${last} import\n`);
      }
      importsShown = true;
      continue;
    }
    const name = entry.kind === "method" ? `${entry.receiver}.${entry.name}`
      : entry.kind === "json" ? `${entry.pointer || "/"} ${entry.value_type}` : entry.name;
    rows.push(`${entry.line}-${entry.line_end} ${entry.kind} ${displayOutlineName(name)}\n`);
  }
  return rows.length ? rows : ["(no outline)\n"];
}

export function createInspectFileTool(grammar: string): Tool<string[]> {
  return createExecutorTool({
    name: "inspect_file",
    description: `Inspect host-readable regular files and return compact structural rows: START-END KIND NAME. Imports collapse to one range; parse_error rows locate syntax errors. When only structure is needed, prefer an outline to a full-file read; read source only for information missing from the outline or current context.
Usage: inspect_file [--json] [--max-tokens N] PATH [PATH ...]
Multiple files have path headers and share one budget. --json returns metadata and structured outline entries instead. Line ranges are one-based. Example: inspect_file src/main.go, then mcat src/main.go START:END for the relevant entry. Reuse known locations instead of outlining a file again.`,
    grammar,
    argv: readerArguments,
    async execute(argv) {
      let suppliedPath: string | null = null;
      let json = false;
      try {
        const optionEnd = argv.indexOf("--");
        json = argv.slice(0, optionEnd < 0 ? argv.length : optionEnd).includes("--json");
        const parsed = readerOptions(argv, false, () => false, true);
        if (parsed.options.previewBytes !== undefined) throw new InspectFailure("usage", "inspect_file does not accept source preview flags");
        argv = parsed.rest;
        const terminator = argv.indexOf("--");
        let jsonSeen = false;
        argv = argv.filter((argument, index) => {
          if (index === terminator) return false;
          if (argument !== "--json" || terminator >= 0 && index > terminator) return true;
          if (jsonSeen) throw new InspectFailure("usage", "--json cannot repeat");
          jsonSeen = true;
          return false;
        });
        if (argv.length === 0) {
          throw new InspectFailure("usage", "inspect_file expects PATH [PATH ...]");
        }
        const budget = parsed.options.maxTokens ?? READ_DEFAULT_TOKENS;
        if (json && argv.length === 1) {
          suppliedPath = argv[0];
          return success(await inspect(suppliedPath), budget);
        }
        const output = new BoundedTextOutput(budget);
        const omitted: string[] = [];
        const errors: string[] = [];
        let failed = false;
        let failureClass: ExecutionResult["failureClass"];
        for (const input of argv) {
          suppliedPath = input;
          try {
            const data = await inspect(input);
            const rows = json
              ? [`${JSON.stringify({ok: true, data, truncated: false, truncation: null})}\n`]
              : [...(argv.length > 1 ? [`--- ${displayOutlineName(data.path)} ---\n`] : []), ...compactOutline(data)];
            for (const row of rows) {
              if (byteLength(output.current) + byteLength(row) > OUTPUT_BYTES || !output.append(row)) {
                output.incomplete = true;
                omitted.push(row);
              }
            }
          } catch (error) {
            failed = true;
            const cause = filesystemFailure(error);
            failureClass ??= inspectionFailureClass(cause.code);
            if (json) {
              const row = failure(input, cause.code, cause.message);
              if (!output.append(row)) omitted.push(row);
            } else errors.push(`inspect_file: ${displayOutlineName(input)}: ${cause.message}\n`);
          }
        }
        const retained = omitted.join("");
        const stderr = errors.join("");
        if (byteLength(retained) > MAX_RETAINED_BYTES) {
          return {stdout: output.current, stderr: stderr + "inspect_file: recovery unavailable: omitted outline exceeds the 16 MiB recovery bound; use bounded mcat reads\n", exitCode: 1, failureClass: "output_limit"};
        }
        return {stdout: output.current, ...(stderr ? {stderr} : {}), exitCode: retained || failed ? 1 : 0,
          ...(failureClass ? {failureClass} : {}),
          ...(retained ? {failureClass: "output_limit" as const, omittedOutput: {stdout: retained, stderr: "", ...(json ? {} : {stdoutKind: "rows" as const})}} : {})};
      } catch (error) {
        const cause = error instanceof InspectFailure
          ? error
          : new InspectFailure(suppliedPath === null ? "usage" : "read", `cannot inspect file: ${errorText(error)}`);
        return {stdout: json ? failure(suppliedPath, cause.code, cause.message) : "",
          ...(!json ? {stderr: `inspect_file: ${suppliedPath === null ? "" : displayOutlineName(suppliedPath) + ": "}${cause.message}\n`} : {}), exitCode: 1,
          failureClass: inspectionFailureClass(cause.code)};
      }
    },
  });
}
