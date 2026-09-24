import {afterAll, expect, test} from "bun:test";
import {spawnSync} from "node:child_process";
import {mkdtemp, readFile, rm, writeFile} from "node:fs/promises";
import {tmpdir} from "node:os";
import path from "node:path";

import {countGPT5Tokens} from "../../../../plugins/common.ts";
import {
  codeOutline,
  codeTree,
  declarationRange,
  goDeclarationRange,
} from "../../../../plugins/inspect_file_code.ts";
import {createInspectFileTool, LineMap, sourceFormat} from "../../../../plugins/inspect_file.ts";
import type {LocatedEntry} from "../../../../plugins/inspect_file_support.ts";

const executionContext = {
  stdinFD: null,
  scriptReadFD: null,
  scriptWriteFD: null,
  outputBudgetBytes: 16 * 1024 * 1024,
};
const temporaryDirectories: string[] = [];

afterAll(async () => {
  await Promise.all(temporaryDirectories.splice(0).map((directory) => rm(directory, {recursive: true, force: true})));
});

async function temporaryDirectory(prefix: string): Promise<string> {
  const directory = await mkdtemp(path.join(tmpdir(), prefix));
  temporaryDirectories.push(directory);
  return directory;
}

test("inspect_file defaults to exact compact rows and preserves the single-file JSON result", async () => {
  const directory = await temporaryDirectory("inspect-compact-");
  const file = path.join(directory, "sample.ts");
  const source = [
    'import {first} from "one";',
    'import {second} from "two";',
    "function visible() {",
    "  return 1;",
    "}",
    "",
    "class Box {",
    "  method() {}",
    "}",
  ].join("\n");
  await writeFile(file, source, "utf8");
  const tool = createInspectFileTool("");

  const compact = await tool.execute([file], executionContext);
  expect(compact).toMatchObject({
    exitCode: 0,
    stdout: "1-2 import\n3-5 function visible\n7-9 class Box\n8-8 method Box.method\n",
  });

  const json = await tool.execute(["--json", file], executionContext);
  expect(json.exitCode).toBe(0);
  expect(JSON.parse(json.stdout!)).toEqual({
    ok: true,
    data: {
      path: file,
      kind: "code",
      language: "typescript",
      size_bytes: Buffer.byteLength(source),
      line_count: 9,
      parse_complete: true,
      outline: [
        {kind: "import", name: "first", line: 1, line_end: 1},
        {kind: "import", name: "second", line: 2, line_end: 2},
        {kind: "function", name: "visible", line: 3, line_end: 5},
        {kind: "class", name: "Box", line: 7, line_end: 9},
        {kind: "method", name: "method", receiver: "Box", line: 8, line_end: 8},
      ],
    },
    truncated: false,
    truncation: null,
  });
});

test("inspect_file prints one header per compact path and compresses msymbol.ts by at least 70%", async () => {
  const directory = await temporaryDirectory("inspect-multipath-");
  const first = path.join(directory, "first.go");
  const second = path.join(directory, "second.go");
  await Promise.all([
    writeFile(first, "package p\nfunc First() {}\n", "utf8"),
    writeFile(second, "package p\nfunc Second() {}\n", "utf8"),
  ]);
  const tool = createInspectFileTool("");
  const multiple = await tool.execute([first, second], executionContext);
  expect(multiple).toMatchObject({
    exitCode: 0,
    stdout: `--- ${first} ---\n2-2 function First\n--- ${second} ---\n2-2 function Second\n`,
  });

  const compact = await tool.execute(["plugins/msymbol.ts"], executionContext);
  const json = await tool.execute(["--json", "plugins/msymbol.ts"], executionContext);
  expect(compact.exitCode).toBe(0);
  expect(json.exitCode).toBe(0);
  const compactTokens = countGPT5Tokens(compact.stdout!);
  const jsonTokens = countGPT5Tokens(json.stdout!);
  expect(compactTokens).toBeLessThanOrEqual(Math.floor(jsonTokens * 0.3));
});

test("multi-path JSON shares its budget and retains complete JSONL records", async () => {
  const directory = await temporaryDirectory("inspect-jsonl-budget-");
  const first = path.join(directory, "first.go");
  const second = path.join(directory, "second.go");
  await Promise.all([
    writeFile(first, "package p\nfunc First() {}\n", "utf8"),
    writeFile(second, "package p\nfunc Second() {}\n", "utf8"),
  ]);
  const tool = createInspectFileTool("");
  const complete = await tool.execute(["--json", "--max-tokens", "15500", first, second], executionContext);
  const records = complete.stdout!.split("\n").filter(Boolean);
  expect(complete.exitCode).toBe(0);
  expect(records).toHaveLength(2);
  expect(records.map((record) => JSON.parse(record).data.path)).toEqual([first, second]);

  const firstRecord = `${records[0]}\n`;
  const secondRecord = `${records[1]}\n`;
  const budget = countGPT5Tokens(firstRecord);
  const bounded = await tool.execute(
    ["--json", "--max-tokens", String(budget), first, second],
    executionContext,
  );
  expect(bounded.exitCode).toBe(1);
  expect(bounded.stdout).toBe(firstRecord);
  expect(bounded.omittedOutput?.stdout).toBe(secondRecord);
});

test("declaration ranges survive unrelated syntax errors but reject malformed matched declarations", async () => {
  const typescript = [
    "function complete() {",
    "  return 1;",
    "}",
    "function malformed( {",
    "",
  ].join("\n");
  const typescriptFormat = sourceFormat("fixture.ts");
  expect(typescriptFormat).not.toBeNull();
  const typescriptLines = new LineMap(typescript);
  const completeFrom = typescript.indexOf("complete");
  expect(declarationRange(
    typescript,
    typescriptLines,
    typescriptFormat!,
    completeFrom,
    completeFrom + "complete".length,
  )).toEqual({line: 1, line_end: 3});
  const malformedFrom = typescript.indexOf("malformed");
  expect(declarationRange(
    typescript,
    typescriptLines,
    typescriptFormat!,
    malformedFrom,
    malformedFrom + "malformed".length,
  )).toBeNull();

  const validTypeQuery = 'const model: typeof import("x") | undefined = undefined;\nfunction broken( {\n';
  const typeQueryFrom = validTypeQuery.indexOf("model");
  expect(declarationRange(
    validTypeQuery,
    new LineMap(validTypeQuery),
    typescriptFormat!,
    typeQueryFrom,
    typeQueryFrom + "model".length,
  )).toEqual({line: 1, line_end: 1});

  const go = "package p\nfunc Complete() { println(1) }\nfunc Malformed( {\n";
  const goFrom = go.indexOf("Complete");
  expect(goDeclarationRange(
    go,
    Buffer.byteLength(go.slice(0, goFrom)),
    Buffer.byteLength(go.slice(0, goFrom + "Complete".length)),
  )).toEqual({line: 2, line_end: 2});
  const malformedGoFrom = go.indexOf("Malformed");
  expect(goDeclarationRange(
    go,
    Buffer.byteLength(go.slice(0, malformedGoFrom)),
    Buffer.byteLength(go.slice(0, malformedGoFrom + "Malformed".length)),
  )).toBeNull();

  const directory = await temporaryDirectory("inspect-parse-errors-");
  const file = path.join(directory, "fixture.ts");
  await writeFile(file, typescript, "utf8");
  const inspected = await createInspectFileTool("").execute(["--json", file], executionContext);
  const result = JSON.parse(inspected.stdout!);
  expect(result.data.parse_complete).toBe(false);
  expect(result.data.outline).toContainEqual({
    kind: "parse_error",
    name: "syntax error",
    line: 4,
    line_end: 4,
  });
});

test("recovers a valid type-query suffix after a malformed prefix without a false error row", async () => {
  const source = 'const broken = ;\nconst model: typeof import("x") | undefined = undefined;\n';
  const format = sourceFormat("fixture.ts");
  expect(format).not.toBeNull();
  const nameFrom = source.indexOf("model");
  expect(declarationRange(
    source,
    new LineMap(source),
    format!,
    nameFrom,
    nameFrom + "model".length,
  )).toEqual({line: 2, line_end: 2});

  const directory = await temporaryDirectory("inspect-local-suffix-");
  const file = path.join(directory, "fixture.ts");
  await writeFile(file, source, "utf8");
  const inspected = await createInspectFileTool("").execute(["--json", file], executionContext);
  const result = JSON.parse(inspected.stdout!);
  expect(result.data.parse_complete).toBe(false);
  expect(result.data.outline).toContainEqual({
    kind: "constant",
    name: "model",
    line: 2,
    line_end: 2,
  });
  expect(result.data.outline).toContainEqual({
    kind: "parse_error",
    name: "syntax error",
    line: 1,
    line_end: 1,
  });
  expect(result.data.outline).not.toContainEqual(expect.objectContaining({
    kind: "parse_error",
    line: 2,
  }));
});

test("outlines and expands multiline TSDeclareMethod declarations", async () => {
  const source = [
    "abstract class AbstractThing {",
    "  abstract transform(",
    "    value: string,",
    "    fallback?: number,",
    "  ): Promise<string>;",
    "}",
    "",
    "declare class AmbientThing {",
    "  dispatch(",
    "    first: string,",
    "    second: number,",
    "  ): Promise<void>;",
    "}",
    "",
    "class Overloaded {",
    "  parse(",
    "    input: string,",
    "  ): Result;",
    "  parse(",
    "    input: Uint8Array,",
    "    encoding: string,",
    "  ): Result;",
    "  parse(",
    "    input: string | Uint8Array,",
    "    encoding?: string,",
    "  ): Result {",
    "    return {} as Result;",
    "  }",
    "}",
    "",
  ].join("\n");
  const directory = await temporaryDirectory("inspect-ts-declare-method-");
  const file = path.join(directory, "declarations.ts");
  await writeFile(file, source, "utf8");
  const inspected = await createInspectFileTool("").execute(["--json", file], executionContext);
  const result = JSON.parse(inspected.stdout!);
  expect(result.data.parse_complete).toBe(true);
  expect(result.data.outline.map((entry: Record<string, unknown>) => [
    entry.kind,
    entry.name,
    entry.receiver,
    entry.line,
    entry.line_end,
  ])).toEqual([
    ["class", "AbstractThing", undefined, 1, 6],
    ["method", "transform", "AbstractThing", 2, 5],
    ["class", "AmbientThing", undefined, 8, 13],
    ["method", "dispatch", "AmbientThing", 9, 12],
    ["class", "Overloaded", undefined, 15, 29],
    ["method", "parse", "Overloaded", 16, 18],
    ["method", "parse", "Overloaded", 19, 22],
    ["method", "parse", "Overloaded", 23, 28],
  ]);

  const format = sourceFormat(file);
  expect(format).not.toBeNull();
  const lines = new LineMap(source);
  for (const {line, name, expected} of [
    {line: 2, name: "transform", expected: {line: 2, line_end: 5}},
    {line: 9, name: "dispatch", expected: {line: 9, line_end: 12}},
    {line: 16, name: "parse", expected: {line: 16, line_end: 18}},
    {line: 19, name: "parse", expected: {line: 19, line_end: 22}},
    {line: 23, name: "parse", expected: {line: 23, line_end: 28}},
  ]) {
    const logical = lines.logicalLine(line);
    expect(logical).not.toBeNull();
    const start = logical!.from + logical!.text.indexOf(name);
    expect(declarationRange(source, lines, format!, start, start + name.length)).toEqual(expected);
  }
});

test("tracked Go and plugin TypeScript declarations have complete ranges", async () => {
  const repository = process.cwd();
  const listed = spawnSync(
    "git",
    ["-C", repository, "ls-files", "-z", "--", "*.go", "plugins/*.ts"],
    {encoding: "buffer"},
  );
  expect(listed.status, listed.stderr.toString("utf8")).toBe(0);
  const files = listed.stdout.toString("utf8").split("\0").filter(Boolean);
  expect(files.length).toBeGreaterThan(0);

  let goFiles = 0;
  let pluginTypeScriptFiles = 0;
  let checkedRanges = 0;
  const failures: string[] = [];
  for (const relativePath of files) {
    if (relativePath.endsWith(".go")) goFiles += 1;
    if (relativePath.startsWith("plugins/") && relativePath.endsWith(".ts")) pluginTypeScriptFiles += 1;
    const format = sourceFormat(relativePath);
    if (format?.kind !== "code") {
      failures.push(`${relativePath}: unsupported code source`);
      continue;
    }
    let source: string;
    try {
      source = await readFile(path.join(repository, relativePath), "utf8");
    } catch (error) {
      if (error instanceof Error && "code" in error && error.code === "ENOENT") continue;
      throw error;
    }
    const lines = new LineMap(source);
    const tree = codeTree(source, format);
    const outline = codeOutline(source, lines, format, tree);
    for (const {entry} of outline) {
      if (entry.kind === "parse_error") {
        if (entry.line < 1 || entry.line_end < entry.line || entry.line_end > lines.count) {
          failures.push(`${relativePath}: invalid parse-error row ${entry.line}-${entry.line_end}`);
        }
      } else if (entry.line < 1 || entry.line_end < entry.line || entry.line_end > lines.count) {
        failures.push(`${relativePath}: invalid ${entry.kind} row ${entry.line}-${entry.line_end}`);
      }
    }
    const candidate = outline.find(({entry, nameFrom, nameTo}: LocatedEntry) =>
      entry.kind !== "import" && entry.kind !== "parse_error"
      && nameFrom !== undefined && nameTo !== undefined,
    );
    if (candidate === undefined) continue;
    const {entry, nameFrom, nameTo} = candidate;
    const range = format.language === "go"
      ? goDeclarationRange(
        source,
        Buffer.byteLength(source.slice(0, nameFrom!)),
        Buffer.byteLength(source.slice(0, nameTo!)),
      )
      : declarationRange(source, lines, format, nameFrom!, nameTo!);
    checkedRanges += 1;
    if (range === null || range.line !== entry.line || range.line_end !== entry.line_end) {
      const errors: string[] = [];
      tree.iterate({enter(node) {
        if (node.type.isError && node.from >= candidate.offset && node.from <= candidate.end) {
          errors.push(`${node.from}-${node.to} ${JSON.stringify(source.slice(node.from, node.to))}`);
        }
      }});
      failures.push(`${relativePath}: ${entry.kind} ${entry.name} ${entry.line}-${entry.line_end} expected complete range, got ${range === null ? "none" : `${range.line}-${range.line_end}`}; local parser errors: ${errors.slice(0, 3).join(", ") || "none"}`);
    }
  }

  expect(goFiles).toBeGreaterThan(0);
  expect(pluginTypeScriptFiles).toBeGreaterThan(0);
  expect(checkedRanges, "tracked Go/TypeScript source declaration ranges").toBeGreaterThan(0);
  expect(
    {checkedRanges, failures: failures.slice(0, 8)},
    `${failures.length} tracked-source issue(s); first findings: ${failures.slice(0, 8).join("; ")}`,
  ).toMatchObject({failures: []});
});
