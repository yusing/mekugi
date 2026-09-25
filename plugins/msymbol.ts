import {MAX_RETAINED_BYTES, RetainedRows} from "./retained_output.ts";
import {resolverProcess, ResolverTimeout, withResolverDeadline} from "./resolver.ts";
import {spawn} from "node:child_process";
import {readFile, realpath, stat} from "node:fs/promises";
import path from "node:path";
import {fileURLToPath} from "node:url";

import type {ExecutionResult, Tool} from "../internal/router/toolplugin/plugin.d.ts";
import {
  isGoIdentifier,
  parsePositiveInteger as parseCorePositiveInteger,
  SharedCoreError,
} from "mekugi:core/v1";
import {
  byteLength,
  collect,
  createExecutorTool,
  readerArguments,
  readerOptions,
  READ_DEFAULT_TOKENS,
  readerFailureClass,
  decodeUTF8,
  errorText,
  isOutsideWorkspace,
  BoundedTextOutput,
} from "./common.ts";
import {
  declarationRange,
  goDeclarationRange,
  LineMap,
  sourceFormat,
  symbolOffsets,
} from "./inspect_file.ts";
import type {SourceFormat} from "./inspect_file.ts";
import {runLSPQueries} from "./lsp.ts";
import {codeOutline, symbolLines} from "./inspect_file_code.ts";
import type {LSPLocation} from "./lsp.ts";

type QueryMode = "def" | "refs";
type Resolver = "gopls" | "typescript" | "python";

type Query = {
  maxTokens: number;
  workspace?: string;
  mode: QueryMode;
  path: string;
  line: number | null;
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

class MSymbolFailure extends Error {}
class MSymbolOutputLimit extends MSymbolFailure {}

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
      throw new MSymbolFailure(`${label} is too large`);
    }
    throw new MSymbolFailure(`${label} must be a positive decimal integer`);
  }
}

/**
 * validGoIdentifier checks whether value is a valid non-keyword Go identifier using the shared-core validator.
 */
function validGoIdentifier(value: string): boolean {
  return isGoIdentifier(value);
}

const symbolUsage = "msymbol [--max-tokens N] [--workspace ROOT] (def|refs) PATH [LINE] SYMBOL [N] [(def|refs) PATH [LINE] SYMBOL [N] ...]";

function parseQueries(argv: string[]): Query[] {
  let workspace: string | undefined;
  const terminator = argv.indexOf("--");
  const workspaceIndex = argv.findIndex((arg, i) => arg === "--workspace" && (terminator < 0 || i < terminator));
  if (workspaceIndex >= 0) {
    workspace = argv[workspaceIndex + 1];
    if (workspace === undefined || workspace === "" || workspace.includes("\0")) {
      throw new MSymbolFailure("--workspace requires a usable directory");
    }
    argv = [...argv.slice(0, workspaceIndex), ...argv.slice(workspaceIndex + 2)];
  }
  const parsed = readerOptions(argv);
  if (parsed.options.previewBytes !== undefined) throw new MSymbolFailure("use mrun with a byte-oriented command for source previews");
  const rest = parsed.rest;
  const queries: Query[] = [];
  let index = 0;
  while (index < rest.length) {
    const mode = rest[index++];
    if (mode !== "def" && mode !== "refs") throw new MSymbolFailure("mode must be def or refs");
    let inputPath = rest[index++];
    if (!inputPath || inputPath.includes("\0")) throw new MSymbolFailure("path must be usable");
    let line: number | null = null;
    const combined = /^(.*):([1-9][0-9]*)$/u.exec(inputPath);
    if (combined !== null) {
      inputPath = combined[1];
      if (inputPath.startsWith('"')) {
        try { inputPath = JSON.parse(inputPath); } catch { throw new MSymbolFailure("invalid quoted PATH:LINE"); }
      }
      if (typeof inputPath !== "string" || !inputPath || inputPath.includes("\0")) throw new MSymbolFailure("path must be usable");
      line = parsePositiveInteger(combined[2], "line");
    } else if (/^[0-9]+$/u.test(rest[index] ?? "")) {
      line = parsePositiveInteger(rest[index++], "line");
    }
    const selector = rest[index++];
    if (!selector) throw new MSymbolFailure(`usage: ${symbolUsage}`);
    const identifier = selector.split(".").at(-1)!;
    if (!identifier) throw new MSymbolFailure("SYMBOL must end with a usable name");
    const occurrence = /^[0-9]+$/u.test(rest[index] ?? "") ? parsePositiveInteger(rest[index++], "N") : null;
    if (line === null && occurrence !== null) throw new MSymbolFailure("N requires an explicit LINE");
    queries.push({maxTokens: parsed.options.maxTokens ?? READ_DEFAULT_TOKENS, workspace,
      mode, path: inputPath, line, identifier, occurrence});
  }
  if (queries.length === 0) throw new MSymbolFailure(`usage: ${symbolUsage}`);
  return queries;
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
    throw new SourceFailure(reason, "path has an unsupported msymbol source format");
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
  if (query.line === null) {
    const matches = codeOutline(file.source, file.lines, file.format).filter(item =>
      item.entry.kind !== "parse_error" && "name" in item.entry && item.entry.name === query.identifier
      && item.nameFrom !== undefined);
    if (matches.length > 1) {
      const lines = matches.map(item => file.lines.lineAt(item.nameFrom!)).join(", ");
      throw new MSymbolFailure(`${query.identifier} has ${matches.length} outline matches (lines ${lines}); supply LINE`);
    }
    if (matches.length === 0) {
      const tokens = symbolLines(file.source, file.lines, file.format, query.identifier).sort((a, b) => a - b).slice(0, 5);
      throw new MSymbolFailure(`${query.identifier} has no outline declaration; ${tokens.length ? `supply LINE, such as a token line: ${tokens.join(", ")}` : "no matching token in this file"}`);
    }
    if (matches[0].complete === false) throw new MSymbolFailure(`${query.identifier} has an incomplete outline declaration; supply LINE`);
    const offset = matches[0].nameFrom!;
    query.line = file.lines.lineAt(offset);
    return offset;
  }
  const logicalLine = file.lines.logicalLine(query.line);
  if (logicalLine === null) {
    throw new MSymbolFailure(`line ${query.line} is past EOF`);
  }
  if (file.format.language === "go" && !validGoIdentifier(query.identifier)) {
    throw new MSymbolFailure("SYMBOL must be a non-keyword Go identifier");
  }
  const offsets = symbolOffsets(file.source, file.lines, file.format, query.line, query.identifier);
  if (offsets.length === 0) {
    const nearby = symbolLines(file.source, file.lines, file.format, query.identifier)
      .sort((a, b) => Math.abs(a - query.line!) - Math.abs(b - query.line!)).slice(0, 5).sort((a, b) => a - b);
    throw new MSymbolFailure(`${query.identifier} is not a symbol token on the selected line; ${nearby.length ? `nearby lines: ${nearby.join(", ")}` : "no matching token in this file"}`);
  }
  if (query.occurrence === null) {
    if (offsets.length !== 1) {
      throw new MSymbolFailure(`${query.identifier} is ambiguous on the selected line (${offsets.length} occurrences); supply N`);
    }
    return offsets[0];
  }
  const selected = offsets[query.occurrence - 1];
  if (selected === undefined) {
    throw new MSymbolFailure(`symbol occurrence ${query.occurrence} is missing`);
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
          throw new MSymbolFailure("gopls is unavailable");
        }
        throw new MSymbolFailure(`cannot start gopls: ${errorText(completed.error)}`);
      }
      const [stdoutBytes, stderrBytes] = await Promise.all([stdoutPromise, stderrPromise]);
      const stdout = decodeUTF8(stdoutBytes, "gopls stdout");
      const stderr = decodeUTF8(stderrBytes, "gopls stderr");
      if (completed.exitCode !== 0) {
        throw new MSymbolFailure(conciseGoplsError(stderr, completed.exitCode));
      }
      return {stdout, stderr};
    } catch (error) {
      child.kill("SIGKILL");
      await lifecycle.finish();
      if (error instanceof ResolverTimeout) throw error;
      if (error instanceof MSymbolFailure) {
        throw error;
      }
      throw new MSymbolFailure(`gopls query failed: ${errorText(error)}`);
    }
  });
}

function parseDefinition(stdout: string): GoplsDefinition {
  let value: unknown;
  try {
    value = JSON.parse(stdout);
  } catch (error) {
    throw new MSymbolFailure(`invalid gopls definition output: ${errorText(error)}`);
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new MSymbolFailure("invalid gopls definition output");
  }
  const span = (value as {span?: unknown}).span;
  if (span === null || typeof span !== "object" || Array.isArray(span)) {
    throw new MSymbolFailure("invalid gopls definition span");
  }
  const uri = (span as {uri?: unknown}).uri;
  const start = (span as {start?: unknown}).start;
  const end = (span as {end?: unknown}).end;
  if (
    typeof uri !== "string"
    || start === null || typeof start !== "object" || Array.isArray(start)
    || end === null || typeof end !== "object" || Array.isArray(end)
  ) {
    throw new MSymbolFailure("invalid gopls definition span");
  }
  const line = (start as {line?: unknown}).line;
  const startOffset = (start as {offset?: unknown}).offset;
  const endByte = (end as {offset?: unknown}).offset;
  if (
    !Number.isSafeInteger(line) || (line as number) < 1
    || !Number.isSafeInteger(startOffset) || (startOffset as number) < 0
    || !Number.isSafeInteger(endByte) || (endByte as number) < (startOffset as number)
  ) {
    throw new MSymbolFailure("invalid gopls definition span");
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
      throw new MSymbolFailure("invalid gopls references output");
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

type PreparedQuery = {query: Query; file: SourceFile; offset: number};

async function queryBackends(workspace: string, prepared: PreparedQuery[], onResolverStart: () => void): Promise<BackendResult[]> {
  const results: BackendResult[] = new Array(prepared.length);
  for (const resolver of ["gopls", "typescript", "python"] as const) {
    const group = prepared.map((item, index) => ({...item, index})).filter(item => resolverFor(item.file.format) === resolver);
    if (group.length === 0) continue;
    onResolverStart();
    if (resolver === "gopls" && group.length === 1) {
      const {file, query, offset, index} = group[0];
      const result = await runGopls(workspace, query.mode, `${file.path}:#${byteLength(file.source.slice(0, offset))}`);
      results[index] = {resolver, locations: query.mode === "def" ? [parseDefinition(result.stdout)] : parseReferences(result.stdout), stderr: result.stderr};
      continue;
    }
    const result = await runLSPQueries({
      command: resolver === "gopls" ? "gopls" : resolver === "typescript" ? "tsc" : "pyright-langserver",
      args: resolver === "gopls" ? ["serve"] : resolver === "typescript" ? ["--lsp", "--stdio"] : ["--stdio"],
      workspace,
    }, group.map(({file, query, offset}) => ({
      path: file.path, languageID: resolver === "gopls" ? "go" : lspLanguageID(file.format), source: file.source,
      position: {line: query.line! - 1, character: offset - file.lines.logicalLine(query.line!)!.from}, mode: query.mode,
    })));
    group.forEach(({index}, i) => {
      results[index] = {resolver, locations: result.locations[i].map(location => ({kind: "lsp", location})), stderr: i === 0 ? result.stderr : ""};
    });
  }
  return results;
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

function skippedDiagnostic(skipped: Map<SourceFailureReason, number>): string {
  const parts = [...skipped].map(([reason, count]) => `${count} ${count === 1 ? "location" : "locations"} ${reason}`);
  return parts.length === 0 ? "" : `msymbol: skipped ${parts.join(", ")}\n`;
}

async function executeQueries(queries: Query[], onResolverStart: () => void): Promise<ExecutionResult> {
  const first = queries[0];
  let workspace: string;
  try {
    workspace = await realpath(first.workspace ?? process.cwd());
    if (!(await stat(workspace)).isDirectory()) throw new Error("workspace is not a directory");
  } catch (error) {
    throw new MSymbolFailure(`cannot resolve workspace: ${errorText(error)}`);
  }
  const cache = new Map<string, SourceFile>();
  const prepared: PreparedQuery[] = [];
  for (const query of queries) {
    const file = await loadSource(workspace, query.path, cache);
    prepared.push({query, file, offset: selectSymbol(file, query)});
  }
  const backends = await queryBackends(workspace, prepared, onResolverStart);
  cache.clear();
  for (const {file} of prepared) {
    try {
      const current = await loadSource(workspace, file.path, cache);
      if (current.source !== file.source) throw new Error("changed");
    } catch { throw new MSymbolFailure("input changed during query"); }
  }
  const output = new BoundedTextOutput(first.maxTokens);
  const retainedRows = new RetainedRows();
  const skipped = new Map<SourceFailureReason, number>();
  const skip = (reason: SourceFailureReason): void => {
    skipped.set(reason, (skipped.get(reason) ?? 0) + 1);
  };
  const append = (row: string): void => {
    if (!retainedRows.append(row)) throw new MSymbolOutputLimit("complete reference output exceeds the 16 MiB retention bound");
    if (!output.incomplete) output.append(row);
  };
  let stderr = "";
  for (let index = 0; index < queries.length; index++) {
    const query = queries[index], backend = backends[index];
    const seen = new Set<string>();
    const references = new Map<string, string[]>();
    let emitted = false;
    let referenceBytes = 0;
    stderr += backend.stderr;
    if (stderr !== "" && !stderr.endsWith("\n")) stderr += "\n";
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
        let headerEnd = startLine - 1;
        for (let line = startLine; line <= endLine; line += 1) {
          const logicalLine = materialized.file.lines.logicalLine(line);
          if (logicalLine === null) {
            skip("unavailable");
            break;
          }
          const key = `${materialized.file.path}\0${line}`;
          if (seen.has(key)) {
            continue;
          }
          seen.add(key);
          const label = JSON.stringify(query.workspace === undefined ? path.relative(workspace, materialized.file.path) : materialized.file.path);
          if (query.mode === "def") {
            if (line > headerEnd) {
              headerEnd = line;
              while (headerEnd < endLine && !seen.has(`${materialized.file.path}\0${headerEnd + 1}`)) headerEnd++;
              append(`${label}:${line}-${headerEnd}\n`);
            }
            append(`${logicalLine.text}\n`);
          } else {
            const rows = references.get(label) ?? [];
            const row = `${line} ${logicalLine.text}\n`;
            referenceBytes += byteLength(row);
            if (referenceBytes > MAX_RETAINED_BYTES) throw new MSymbolOutputLimit("complete reference output exceeds the 16 MiB retention bound");
            rows.push(row);
            references.set(label, rows);
          }
          emitted = true;
        }
      } catch (error) {
        if (!(error instanceof SourceFailure)) {
          throw error;
        }
        skip(error.reason);
      }
    }

    for (const [label, rows] of references) {
      append(`${label}:\n`);
      for (const row of rows) append(row);
    }
    if (query.mode === "def" && !emitted) {
      return {stderr: `${stderr}${skippedDiagnostic(skipped)}msymbol: definition has no editable workspace location\n`, exitCode: 1, failureClass: "no_editable_location"};
    }
  }
  stderr += skippedDiagnostic(skipped);
  if (output.incomplete) {
    stderr += `msymbol: output incomplete: ${first.maxTokens}-token limit reached\n`;
  }
  return {
    stdout: output.current,
    ...(stderr === "" ? {} : {stderr}),
    exitCode: output.incomplete ? 1 : 0,
    ...(output.incomplete ? {omittedOutput: {stdout: retainedRows.remainder(output.current), stderr: "", stdoutKind: "rows" as const}} : {}),
    ...(output.incomplete ? {failureClass: "output_limit" as const} : {}),
  };
}

export function createMSymbolTool(grammar: string): Tool<string[]> {
  return createExecutorTool({
    name: "msymbol",
    description: "Resolve current Go, JavaScript, TypeScript, JSON, or Python symbol with compact definition bodies and references grouped by file. Before removing a field or changing a signature, use refs to acquire semantic references across affected packages and tests. Read all returned reference rows before dependent edits, continuing incomplete output; report skipped or unavailable coverage rather than treating text matches as complete caller coverage. Usage: `msymbol [--max-tokens N] [--workspace ROOT] (def|refs) PATH [LINE] SYMBOL [N] [(def|refs) PATH [LINE] SYMBOL [N] ...]`. PATH:LINE is also accepted. Without LINE, def and refs select the unique outline declaration named SYMBOL; locals and repeated names need LINE. Batched tuples share a server per language and one output budget. LINE selects the current snapshot. ROOT sets resolver scope and relative paths without changing shell state. N selects an exact language-token occurrence. Ambiguous selectors, unavailable language servers, input changes during the query, and definitions without an editable workspace location fail without stdout rows. An incomplete token-limited result retains complete rows, writes stderr, and exits nonzero.",
    grammar,
    argv(input) {
      return readerArguments(input.replace(/("(?:\\.|[^"\\])*"):([1-9][0-9]*)(?=\s|$)/gu,
        (_, quoted: string, line: string) => JSON.stringify(`${JSON.parse(quoted)}:${line}`)));
    },
    async execute(argv) {
      let queries: Query[];
      try {
        queries = parseQueries(argv);
      } catch (error) {
        return {stderr: `msymbol: ${errorText(error)}\n`, exitCode: 1, failureClass: "invalid_arguments"};
      }
      let resolverStarted = false;
      let result: ExecutionResult;
      try {
        result = await executeQueries(queries, () => { resolverStarted = true; });
      } catch (error) {
        let message = error instanceof MSymbolFailure ? error.message : errorText(error);
        const prerequisites: Record<string, string> = {
          "gopls is unavailable": "expose gopls on the executor PATH",
          "tsc is unavailable": "expose TypeScript 7 tsc with --lsp support on the executor PATH",
          "pyright-langserver is unavailable": "expose pyright-langserver on the executor PATH",
        };
        const failureClass = error instanceof MSymbolOutputLimit ? "output_limit" : error instanceof ResolverTimeout ? "resolver_timeout" : prerequisites[message] !== undefined ? "dependency_unavailable"
          : error instanceof SourceFailure ? "invalid_source" : readerFailureClass(error, "resolver_error");
        if (prerequisites[message] !== undefined) {
          message += `; ${prerequisites[message]}`;
        }
        result = {stderr: `msymbol: ${message}\n`, exitCode: 1, failureClass};
      }
      return resolverStarted ? {...result, terminationReason: "resolver_cleanup"} : result;
    },
  });
}
