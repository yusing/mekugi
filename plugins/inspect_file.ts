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
  LineMap,
  type FileKind,
  type Language,
  type LocatedEntry,
  type PublicOutlineEntry,
  type SourceFormat,
} from "./inspect_file_support.ts";
import {inspectFileShapeSchemaJSON} from "./inspect_file_schema.ts";

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
      const tree = codeTree(source, format);
      return {
        parseComplete: !hasParseError(tree),
		outline: publicOutline(lines, ordered(codeOutline(source, lines, format, tree))),
        lineCount: lines.count,
      };
    }
    if (format.kind === "markdown") {
      const tree = markdownTree(source);
      const outline = markdownOutline(source, lines, tree);
      return {
        parseComplete: !hasParseError(tree) && outline.parseComplete,
		outline: publicOutline(lines, ordered(outline.entries)),
        lineCount: lines.count,
      };
    }
    const tree = jsonTree(source);
    return {
      parseComplete: !hasParseError(tree),
	  outline: publicOutline(lines, ordered(jsonOutline(source, lines, tree))),
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

export function createInspectFileTool(grammar: string): Tool<string[]> {
  return createExecutorTool({
    name: "inspect_file",
    description: `Inspect one host-readable regular file and return bounded JSON metadata and a structural outline. --max-tokens N sets the shared strict 1–15500 ceiling (default 4000). Recover omitted entries with mread. Outline line and line_end are one-based source line numbers.

Result shape schema:
${inspectFileShapeSchemaJSON}`,
    grammar,
    argv: readerArguments,
    async execute(argv) {
      let suppliedPath: string | null = null;
      try {
        const parsed = readerOptions(argv);
        if (parsed.options.previewBytes !== undefined) throw new InspectFailure("usage", "inspect_file does not accept source preview flags");
        argv = parsed.rest;
        if (argv.length !== 1) {
          throw new InspectFailure("usage", "inspect_file expects PATH");
        }
        suppliedPath = argv[0];
        return success(await inspect(suppliedPath), parsed.options.maxTokens ?? READ_DEFAULT_TOKENS);
      } catch (error) {
        const cause = error instanceof InspectFailure
          ? error
          : new InspectFailure(suppliedPath === null ? "usage" : "read", `cannot inspect file: ${errorText(error)}`);
        return {stdout: failure(suppliedPath, cause.code, cause.message), exitCode: 1,
          failureClass: cause.code === "usage" ? "invalid_arguments" : cause.code === "not_found" ? "not_found"
            : cause.code === "output_limit" ? "output_limit" : "reader_error"};
      }
    },
  });
}
