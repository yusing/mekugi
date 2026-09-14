import {RetainedRows} from "./retained_output.ts";
import {resolverProcess, withResolverDeadline} from "./resolver.ts";
import {spawn} from "node:child_process";
import {readFile, realpath, stat} from "node:fs/promises";
import path from "node:path";
import {fileURLToPath} from "node:url";

import type {ExecutionResult, Tool} from "../internal/router/toolplugin/plugin.d.ts";
import {
  formatVerifiedRow,
  hashLine,
  isGoIdentifier,
  parsePositiveInteger as parseCorePositiveInteger,
  parseRowReference,
  SharedCoreError,
} from "mekugi:core/v1";
import {
  byteLength,
  collect,
  createExecutorTool,
  readerFailureClass,
  decodeUTF8,
  errorText,
  isOutsideWorkspace,
  stripOptionalFinalNewline,
  VERIFIED_ROW_LIMIT_DIAGNOSTIC,
  VerifiedRowOutput,
} from "./common.ts";
import {
  declarationRange,
  goDeclarationRange,
  LineMap,
  sourceFormat,
  symbolOffsets,
} from "./inspect_file.ts";
import type {SourceFormat} from "./inspect_file.ts";
import {runLSPQuery} from "./lsp.ts";
import type {LSPLocation} from "./lsp.ts";

type QueryMode = "def" | "refs";
type Resolver = "gopls" | "typescript" | "python";

type Query = {
  workspace?: string;
  mode: QueryMode;
  path: string;
  line: number;
  hash: string | null;
  identifier: string;
  occurrence: number | null;
};

type SourceFile = {
  path: string;
  source: string;
  lines: LineMap;
  format: SourceFormat;
};

type SourceFailureReason =
  | "outside workspace"
  | "unavailable"
  | "not regular"
  | "not Go"
  | "not TypeScript"
  | "not Python"
  | "not UTF-8";

type GoplsDefinition = {
  kind: "gopls-definition";
  path: string;
  line: number;
  startOffset: number;
  endOffset: number;
};

type LineLocation = {
  kind: "line";
  path: string;
  line: number;
};

type LanguageServerLocation = {
  kind: "lsp";
  location: LSPLocation;
};

type BackendLocation = GoplsDefinition | LineLocation | LanguageServerLocation;

type BackendResult = {
  resolver: Resolver;
  locations: BackendLocation[];
  stderr: string;
};

type GoplsResult = {
  stdout: string;
  stderr: string;
};

class HSymbolFailure extends Error {}

class SourceFailure extends Error {
  constructor(readonly reason: SourceFailureReason, message: string) {
    super(message);
  }
}

/**
 * parsePositiveInteger parses a positive integer using the shared-core parser and wraps errors with a contextual label.
 */
function parsePositiveInteger(value: string, label: string): number {
  try {
    return parseCorePositiveInteger(value);
  } catch (error) {
    if (error instanceof SharedCoreError && error.code === "integer_out_of_range") {
      throw new HSymbolFailure(`${label} is too large`);
    }
    throw new HSymbolFailure(`${label} must be a positive decimal integer`);
  }
}

/**
 * validGoIdentifier checks whether value is a valid non-keyword Go identifier using the shared-core validator.
 */
function validGoIdentifier(value: string): boolean {
  return isGoIdentifier(value);
}

/**
 * parseQuery validates and parses the hsymbol argv into a structured query object.
 */
function parseQuery(argv: string[]): Query {
  let workspace: string | undefined;
  if (argv[0] === "--workspace") {
    workspace = argv[1];
    if (workspace === undefined || workspace === "" || workspace.includes("\0")) {
      throw new HSymbolFailure("--workspace requires a usable directory");
    }
    argv = argv.slice(2);
  }
  if (argv.length !== 4 && argv.length !== 5) {
    throw new HSymbolFailure("usage: hsymbol [--workspace ROOT] (def|refs) PATH (LINE|LINE:HASH) SYMBOL [N]");
  }
  const [mode, inputPath, row, identifier, occurrenceText] = argv;
  if (mode !== "def" && mode !== "refs") {
    throw new HSymbolFailure("mode must be def or refs");
  }
  if (inputPath === "" || inputPath.includes("\0")) {
    throw new HSymbolFailure("path must be usable");
  }
  let parsedRow;
  try {
    parsedRow = /^[1-9][0-9]*$/u.test(row)
      ? {line: parsePositiveInteger(row, "line"), hash: null}
      : parseRowReference(row);
  } catch (error) {
    if (error instanceof SharedCoreError && error.code === "integer_out_of_range") {
      throw new HSymbolFailure("line is too large");
    }
    throw new HSymbolFailure("row must be a positive LINE or LINE:HASH with a lowercase four-digit hash");
  }
  return {
    mode,
    workspace,
    path: inputPath,
    line: parsedRow.line,
    hash: parsedRow.hash,
    identifier,
    occurrence: occurrenceText === undefined ? null : parsePositiveInteger(occurrenceText, "N"),
  };
}

function resolverFor(format: SourceFormat): Resolver | null {
  if (format.language === "go") {
    return "gopls";
  }
  if (format.language === "python") {
    return "python";
  }
  if (format.language === "javascript" || format.language === "typescript" || format.kind === "json") {
    return "typescript";
  }
  return null;
}

function unsupportedReason(resolver: Resolver): SourceFailureReason {
  return resolver === "gopls" ? "not Go" : resolver === "python" ? "not Python" : "not TypeScript";
}

function sourceFailure(error: unknown): SourceFailure {
  if (error instanceof SourceFailure) {
    return error;
  }
  if (error instanceof Error && "code" in error && error.code === "ENOENT") {
    return new SourceFailure("unavailable", "path does not exist");
  }
  return new SourceFailure("unavailable", `cannot read path: ${errorText(error)}`);
}

async function loadSource(
  workspace: string,
  inputPath: string,
  cache: Map<string, SourceFile>,
  expectedResolver?: Resolver,
): Promise<SourceFile> {
  const resolved = path.resolve(workspace, inputPath);
  if (isOutsideWorkspace(workspace, resolved)) {
    throw new SourceFailure("outside workspace", "path is outside the workspace");
  }
  let canonicalPath: string;
  try {
    canonicalPath = await realpath(resolved);
  } catch (error) {
    throw sourceFailure(error);
  }
  if (isOutsideWorkspace(workspace, canonicalPath)) {
    throw new SourceFailure("outside workspace", "path resolves outside the workspace");
  }
  const cached = cache.get(canonicalPath);
  if (cached !== undefined) {
    if (expectedResolver !== undefined && resolverFor(cached.format) !== expectedResolver) {
      throw new SourceFailure(unsupportedReason(expectedResolver), "path has an unsupported source format");
    }
    return cached;
  }
  const format = sourceFormat(canonicalPath);
  const resolver = format === null ? null : resolverFor(format);
  if (format === null || resolver === null) {
    const reason = expectedResolver === undefined ? "not TypeScript" : unsupportedReason(expectedResolver);
    throw new SourceFailure(reason, "path has an unsupported hsymbol source format");
  }
  if (expectedResolver !== undefined && resolver !== expectedResolver) {
    throw new SourceFailure(unsupportedReason(expectedResolver), "path has an unsupported source format");
  }
  let info;
  try {
    info = await stat(canonicalPath);
  } catch (error) {
    throw sourceFailure(error);
  }
  if (!info.isFile()) {
    throw new SourceFailure("not regular", "path is not a regular file");
  }
  let bytes: Uint8Array;
  try {
    bytes = await readFile(canonicalPath);
  } catch (error) {
    throw sourceFailure(error);
  }
  let source: string;
  try {
    source = decodeUTF8(bytes, "source file");
  } catch {
    throw new SourceFailure("not UTF-8", "path is not UTF-8");
  }
  const loaded = {path: canonicalPath, source, lines: new LineMap(source), format};
  cache.set(canonicalPath, loaded);
  return loaded;
}

function selectSymbol(file: SourceFile, query: Query): number {
  const logicalLine = file.lines.logicalLine(query.line);
  if (logicalLine === null) {
    throw new HSymbolFailure(`line ${query.line} is past EOF`);
  }
  if (query.hash !== null && hashLine(logicalLine.text) !== query.hash) {
    throw new HSymbolFailure(`stale row ${query.line}:${query.hash}`);
  }
  if (file.format.language === "go" && !validGoIdentifier(query.identifier)) {
    throw new HSymbolFailure("SYMBOL must be a non-keyword Go identifier");
  }
  const offsets = symbolOffsets(file.source, file.lines, file.format, query.line, query.identifier);
  if (offsets.length === 0) {
    throw new HSymbolFailure(`${query.identifier} is not a symbol token on the selected line`);
  }
  if (query.occurrence === null) {
    if (offsets.length !== 1) {
      throw new HSymbolFailure(`${query.identifier} is ambiguous on the selected line; supply N`);
    }
    return offsets[0];
  }
  const selected = offsets[query.occurrence - 1];
  if (selected === undefined) {
    throw new HSymbolFailure(`symbol occurrence ${query.occurrence} is missing`);
  }
  return selected;
}

function conciseGoplsError(stderr: string, exitCode: number | null): string {
  const line = stderr.trim().split(/\r?\n/u).find((candidate) => candidate.trim() !== "");
  return line === undefined ? `gopls exited with status ${exitCode ?? "unknown"}` : line.trim();
}

async function runGopls(workspace: string, mode: QueryMode, position: string): Promise<GoplsResult> {
  return withResolverDeadline(async (deadline) => {
    const argumentsValue = mode === "def"
      ? ["definition", "-json", position]
      : ["references", "-d", position];
    const child = spawn("gopls", argumentsValue, {cwd: workspace, stdio: ["ignore", "pipe", "pipe"]});
    const lifecycle = resolverProcess(child);
    const stdoutPromise = collect(child.stdout);
    const stderrPromise = collect(child.stderr);
    try {
      const completed = await Promise.race([lifecycle.exited, deadline]);
      await lifecycle.finish();
      if (completed.error !== undefined) {
        await Promise.allSettled([stdoutPromise, stderrPromise]);
        if ("code" in completed.error && completed.error.code === "ENOENT") {
          throw new HSymbolFailure("gopls is unavailable");
        }
        throw new HSymbolFailure(`cannot start gopls: ${errorText(completed.error)}`);
      }
      const [stdoutBytes, stderrBytes] = await Promise.all([stdoutPromise, stderrPromise]);
      const stdout = decodeUTF8(stdoutBytes, "gopls stdout");
      const stderr = decodeUTF8(stderrBytes, "gopls stderr");
      if (completed.exitCode !== 0) {
        throw new HSymbolFailure(conciseGoplsError(stderr, completed.exitCode));
      }
      return {stdout, stderr};
    } catch (error) {
      child.kill("SIGKILL");
      await lifecycle.finish();
      if (error instanceof HSymbolFailure) {
        throw error;
      }
      throw new HSymbolFailure(`gopls query failed: ${errorText(error)}`);
    }
  });
}

function parseDefinition(stdout: string): GoplsDefinition {
  let value: unknown;
  try {
    value = JSON.parse(stdout);
  } catch (error) {
    throw new HSymbolFailure(`invalid gopls definition output: ${errorText(error)}`);
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new HSymbolFailure("invalid gopls definition output");
  }
  const span = (value as {span?: unknown}).span;
  if (span === null || typeof span !== "object" || Array.isArray(span)) {
    throw new HSymbolFailure("invalid gopls definition span");
  }
  const uri = (span as {uri?: unknown}).uri;
  const start = (span as {start?: unknown}).start;
  const end = (span as {end?: unknown}).end;
  if (
    typeof uri !== "string"
    || start === null || typeof start !== "object" || Array.isArray(start)
    || end === null || typeof end !== "object" || Array.isArray(end)
  ) {
    throw new HSymbolFailure("invalid gopls definition span");
  }
  const line = (start as {line?: unknown}).line;
  const startOffset = (start as {offset?: unknown}).offset;
  const endByte = (end as {offset?: unknown}).offset;
  if (
    !Number.isSafeInteger(line) || (line as number) < 1
    || !Number.isSafeInteger(startOffset) || (startOffset as number) < 0
    || !Number.isSafeInteger(endByte) || (endByte as number) < (startOffset as number)
  ) {
    throw new HSymbolFailure("invalid gopls definition span");
  }
  let definitionPath: string;
  try {
    definitionPath = fileURLToPath(uri);
  } catch {
    throw new SourceFailure("outside workspace", "definition is not a workspace file");
  }
  return {
    kind: "gopls-definition",
    path: definitionPath,
    line: line as number,
    startOffset: startOffset as number,
    endOffset: endByte as number,
  };
}

function parseReferences(stdout: string): LineLocation[] {
  const locations: LineLocation[] = [];
  for (const rawLine of stdout.split("\n")) {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    if (line === "") {
      continue;
    }
    const match = /^(.*):([1-9][0-9]*):([1-9][0-9]*)-([1-9][0-9]*)$/u.exec(line);
    if (match === null) {
      throw new HSymbolFailure("invalid gopls references output");
    }
    locations.push({kind: "line", path: match[1], line: parsePositiveInteger(match[2], "reference line")});
  }
  return locations;
}

function lspLanguageID(format: SourceFormat): string {
  if (format.kind === "json") {
    return "json";
  }
  if (format.language === "python") {
    return "python";
  }
  if (format.language === "typescript") {
    return format.jsx === true ? "typescriptreact" : "typescript";
  }
  return format.jsx === true ? "javascriptreact" : "javascript";
}

async function queryBackend(
  workspace: string,
  file: SourceFile,
  query: Query,
  selectedOffset: number,
  onResolverStart: () => void,
): Promise<BackendResult> {
  const resolver = resolverFor(file.format);
  if (resolver === null) {
    throw new HSymbolFailure("path has an unsupported hsymbol source format");
  }
  onResolverStart();
  if (resolver === "gopls") {
    const position = `${file.path}:#${byteLength(file.source.slice(0, selectedOffset))}`;
    const result = await runGopls(workspace, query.mode, position);
    return {
      resolver,
      locations: query.mode === "def" ? [parseDefinition(result.stdout)] : parseReferences(result.stdout),
      stderr: result.stderr,
    };
  }
  const logicalLine = file.lines.logicalLine(query.line);
  if (logicalLine === null) {
    throw new HSymbolFailure(`line ${query.line} is past EOF`);
  }
  const command = resolver === "typescript" ? "tsc" : "pyright-langserver";
  const result = await runLSPQuery({
    command,
    args: resolver === "typescript" ? ["--lsp", "--stdio"] : ["--stdio"],
    workspace,
    path: file.path,
    languageID: lspLanguageID(file.format),
    source: file.source,
    position: {line: query.line - 1, character: selectedOffset - logicalLine.from},
    mode: query.mode,
  });
  return {
    resolver,
    locations: result.locations.map((location) => ({kind: "lsp", location})),
    stderr: result.stderr,
  };
}

function lspOffset(file: SourceFile, line: number, character: number): number | null {
  const logicalLine = file.lines.logicalLine(line + 1);
  return logicalLine === null || character > logicalLine.text.length
    ? null
    : logicalLine.from + character;
}

async function materializeLocation(
  workspace: string,
  cache: Map<string, SourceFile>,
  resolver: Resolver,
  location: BackendLocation,
): Promise<{file: SourceFile; line: number; definitionFrom?: number; definitionTo?: number; goDefinition?: GoplsDefinition}> {
  if (location.kind === "gopls-definition") {
    const file = await loadSource(workspace, location.path, cache, resolver);
    return {file, line: location.line, goDefinition: location};
  }
  if (location.kind === "line") {
    const file = await loadSource(workspace, location.path, cache, resolver);
    return {file, line: location.line};
  }
  let locationPath: string;
  try {
    locationPath = fileURLToPath(location.location.uri);
  } catch {
    throw new SourceFailure("outside workspace", "location is not a workspace file");
  }
  const file = await loadSource(workspace, locationPath, cache, resolver);
  const definitionFrom = lspOffset(
    file,
    location.location.range.start.line,
    location.location.range.start.character,
  );
  const definitionTo = lspOffset(
    file,
    location.location.range.end.line,
    location.location.range.end.character,
  );
  if (definitionFrom === null || definitionTo === null || definitionTo < definitionFrom) {
    throw new SourceFailure("unavailable", "location range is unavailable");
  }
  return {
    file,
    line: location.location.range.start.line + 1,
    definitionFrom,
    definitionTo,
  };
}

/**
 * verifiedSourceRow formats a verified reference, absolute when workspace is null.
 */
function verifiedSourceRow(workspace: string | null, file: SourceFile, line: number): string | null {
  const logicalLine = file.lines.logicalLine(line);
  return logicalLine === null
    ? null
    : `${JSON.stringify(workspace === null ? file.path : path.relative(workspace, file.path))}:${formatVerifiedRow(line, logicalLine.text)}`;
}

function skippedDiagnostic(skipped: Map<SourceFailureReason, number>): string {
  const parts = [...skipped].map(([reason, count]) => `${count} ${count === 1 ? "location" : "locations"} ${reason}`);
  return parts.length === 0 ? "" : `hsymbol: skipped ${parts.join(", ")}\n`;
}

async function executeQuery(query: Query, onResolverStart: () => void): Promise<ExecutionResult> {
  let workspace: string;
  try {
    workspace = await realpath(query.workspace ?? process.cwd());
    if (!(await stat(workspace)).isDirectory()) {
      throw new Error("workspace is not a directory");
    }
  } catch (error) {
    throw new HSymbolFailure(`cannot resolve workspace: ${errorText(error)}`);
  }
  const cache = new Map<string, SourceFile>();
  let inputFile: SourceFile;
  try {
    inputFile = await loadSource(workspace, query.path, cache);
  } catch (error) {
    throw sourceFailure(error);
  }
  const selectedOffset = selectSymbol(inputFile, query);
  const backend = await queryBackend(workspace, inputFile, query, selectedOffset, onResolverStart);
  cache.clear();
  let currentInput: SourceFile;
  try {
    currentInput = await loadSource(workspace, inputFile.path, cache);
  } catch {
    throw new HSymbolFailure("input changed during query");
  }
  if (currentInput.source !== inputFile.source) {
    throw new HSymbolFailure("input changed during query");
  }
  const output = new VerifiedRowOutput();
  const retainedRows = new RetainedRows();
  const skipped = new Map<SourceFailureReason, number>();
  const skip = (reason: SourceFailureReason): void => {
    skipped.set(reason, (skipped.get(reason) ?? 0) + 1);
  };
  const seen = new Set<string>();

  for (const location of backend.locations) {
    try {
      const materialized = await materializeLocation(workspace, cache, backend.resolver, location);
      let startLine = materialized.line;
      let endLine = materialized.line;
      if (query.mode === "def") {
        const expanded = materialized.goDefinition === undefined
          ? materialized.definitionFrom === undefined || materialized.definitionTo === undefined
            ? null
            : declarationRange(
              materialized.file.source,
              materialized.file.lines,
              materialized.file.format,
              materialized.definitionFrom,
              materialized.definitionTo,
            )
          : goDeclarationRange(
            materialized.file.source,
            materialized.goDefinition.startOffset,
            materialized.goDefinition.endOffset,
          );
        startLine = expanded?.line ?? materialized.line;
        endLine = expanded?.line_end ?? materialized.line;
      }
      for (let line = startLine; line <= endLine; line += 1) {
        const row = verifiedSourceRow(query.workspace === undefined ? workspace : null, materialized.file, line);
        if (row === null) {
          skip("unavailable");
          break;
        }
        const key = `${materialized.file.path}\0${line}`;
        if (seen.has(key)) {
          continue;
        }
        seen.add(key);
        if (!retainedRows.append(row)) {
          return {stderr: "hsymbol: complete reference output exceeds the 16 MiB retention bound\n", exitCode: 1, failureClass: "output_limit"};
        }
        if (!output.incomplete) {
          output.append(row);
        }
      }
    } catch (error) {
      if (!(error instanceof SourceFailure)) {
        throw error;
      }
      skip(error.reason);
    }
  }

  let stderr = backend.stderr;
  if (stderr !== "" && !stderr.endsWith("\n")) {
    stderr += "\n";
  }
  if (query.hash === null) {
    const inputLine = inputFile.lines.logicalLine(query.line)!;
    stderr += `hsymbol: input ${JSON.stringify(query.workspace === undefined ? path.relative(workspace, inputFile.path) : inputFile.path)}:${query.line}:${hashLine(inputLine.text)} (current snapshot)\n`;
  }
  stderr += skippedDiagnostic(skipped);
  if (query.mode === "def" && output.current === "" && !output.incomplete) {
    stderr += "hsymbol: definition has no editable workspace location\n";
    return {
      ...(stderr === "" ? {} : {stderr}),
      exitCode: 1,
      failureClass: "no_editable_location",
    };
  }
  if (output.incomplete) {
    const retainedPath = retainedRows.save();
    const unreadLine = output.current.split("\n").length;
    stderr += `hsymbol: ${VERIFIED_ROW_LIMIT_DIAGNOSTIC}`;
    stderr += `hsymbol: complete emitted rows retained at ${JSON.stringify(retainedPath)}; unread result rows start at ${unreadLine}. Read bounded ranges with sed; do not rerun the resolver. Skipped locations above remain unresolved. Files remain until removed.\n`;
  }
  return {
    stdout: output.current,
    ...(stderr === "" ? {} : {stderr}),
    exitCode: output.incomplete ? 1 : 0,
    ...(output.incomplete ? {failureClass: "output_limit" as const} : {}),
  };
}

function parseQuotedToken(input: string): {token: string; trailing: string} {
  let escaped = false;
  for (let index = 1; index < input.length; index += 1) {
    const character = input[index];
    if (escaped) {
      escaped = false;
      continue;
    }
    if (character === "\\") {
      escaped = true;
      continue;
    }
    if (character !== "\"") {
      continue;
    }
    const encoded = input.slice(0, index + 1);
    let token;
    try {
      token = JSON.parse(encoded);
    } catch (error) {
      throw new Error(`invalid quoted token: ${errorText(error)}`);
    }
    if (typeof token !== "string") {
      throw new Error("invalid quoted token");
    }
    return {token, trailing: input.slice(index + 1)};
  }
  throw new Error("invalid quoted token: unterminated quoted string");
}

function parseHSymbolArguments(input: string): string[] {
  const value = stripOptionalFinalNewline(input).trim();
  if (value === "") {
    return [];
  }
  const argv: string[] = [];
  let remaining = value;
  while (remaining !== "") {
    remaining = remaining.replace(/^[ \t]+/u, "");
    if (remaining === "") {
      break;
    }
    if (remaining.startsWith("\"")) {
      const {token, trailing} = parseQuotedToken(remaining);
      argv.push(token);
      remaining = trailing;
    } else {
      const separator = remaining.search(/[ \t]/u);
      if (separator < 0) {
        argv.push(remaining);
        break;
      }
      argv.push(remaining.slice(0, separator));
      remaining = remaining.slice(separator);
    }
  }
  return argv;
}

export function createHSymbolTool(description: string, grammar: string): Tool<string[]> {
  return createExecutorTool({
    name: "hsymbol",
    description,
    grammar,
    argv(input) {
      return parseHSymbolArguments(input);
    },
    async execute(argv) {
      let query: ReturnType<typeof parseQuery>;
      try {
        query = parseQuery(argv);
      } catch (error) {
        return {stderr: `hsymbol: ${errorText(error)}\n`, exitCode: 1, failureClass: "invalid_arguments"};
      }
      let resolverStarted = false;
      let result: ExecutionResult;
      try {
        result = await executeQuery(query, () => { resolverStarted = true; });
      } catch (error) {
        let message = error instanceof HSymbolFailure ? error.message : errorText(error);
        const prerequisites: Record<string, string> = {
          "gopls is unavailable": "expose gopls on the executor PATH",
          "tsc is unavailable": "expose TypeScript 7 tsc with --lsp support on the executor PATH",
          "pyright-langserver is unavailable": "expose pyright-langserver on the executor PATH",
        };
        const failureClass = prerequisites[message] !== undefined ? "dependency_unavailable"
          : error instanceof SourceFailure ? "invalid_source" : readerFailureClass(error, "resolver_error");
        if (prerequisites[message] !== undefined) {
          message += `; ${prerequisites[message]}`;
        }
        result = {stderr: `hsymbol: ${message}\n`, exitCode: 1, failureClass};
      }
      return resolverStarted ? {...result, terminationReason: "resolver_cleanup"} : result;
    },
  });
}
