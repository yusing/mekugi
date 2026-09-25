import {afterEach, describe, expect, spyOn, test} from "bun:test";
import {chmod, mkdtemp, readFile, rm, writeFile} from "node:fs/promises";
import {tmpdir} from "node:os";
import path from "node:path";
import {pathToFileURL} from "node:url";

import {countGPT5Tokens} from "../../../../plugins/common.ts";
import {LineMap} from "../../../../plugins/inspect_file.ts";
import {createMSymbolTool} from "../../../../plugins/msymbol.ts";

const originalCWD = process.cwd();
const originalPATH = process.env.PATH;
const originalSetTimeout = globalThis.setTimeout;
const directories: string[] = [];
const executionContext = {stdinFD: null, scriptReadFD: null, scriptWriteFD: null, outputBudgetBytes: 16 * 1024 * 1024};

async function temporaryDirectory(prefix: string): Promise<string> {
  const directory = await mkdtemp(path.join(tmpdir(), prefix));
  directories.push(directory);
  return directory;
}

type LSPFixture = {
  executable: string;
  log: string;
};

type FixtureResponses = {
  definition: Array<{uri: string; range: {start: {line: number; character: number}; end: {line: number; character: number}}}>
    | {uri: string; range: {start: {line: number; character: number}; end: {line: number; character: number}}};
  references: Array<{uri: string; range: {start: {line: number; character: number}; end: {line: number; character: number}}}>;
  hangOnInitialize?: boolean;
  cliOutput?: string;
  mutateOnMethod?: string;
  mutatePath?: string;
  mutatedSource?: string;
};

async function installLSPFixture(
  command: "gopls" | "tsc",
  responses: FixtureResponses,
): Promise<LSPFixture> {
  const directory = await temporaryDirectory(`msymbol-${command}-protocol-`);
  const executable = path.join(directory, command);
  const log = path.join(directory, "protocol.jsonl");
  const source = `#!/usr/bin/env bun
import {appendFileSync, writeFileSync} from "node:fs";

const logPath = ${JSON.stringify(log)};
const responses = ${JSON.stringify(responses)};
appendFileSync(logPath, JSON.stringify({event: "start", args: process.argv.slice(2)}) + "\\n");
let pending = Buffer.alloc(0);

function send(message) {
  const body = Buffer.from(JSON.stringify(message));
  process.stdout.write(Buffer.from("Content-Length: " + body.length + "\\r\\n\\r\\n"));
  process.stdout.write(body);
}

function handle(message) {
  appendFileSync(logPath, JSON.stringify({event: "message", message}) + "\\n");
  if (message.method === responses.mutateOnMethod) {
    writeFileSync(responses.mutatePath, responses.mutatedSource);
  }
  if (message.method === "initialize" && responses.hangOnInitialize) return;
  if (message.method === "initialize") {
    send({jsonrpc: "2.0", id: message.id, result: {capabilities: {positionEncoding: "utf-16"}}});
  } else if (message.method === "textDocument/definition") {
    send({jsonrpc: "2.0", id: message.id, result: responses.definition});
  } else if (message.method === "textDocument/references") {
    send({jsonrpc: "2.0", id: message.id, result: responses.references});
  } else if (message.method === "shutdown") {
    send({jsonrpc: "2.0", id: message.id, result: null});
  } else if (message.method === "exit") {
    process.exit(0);
  }
}

if (process.argv[2] !== "serve" && ${JSON.stringify(command)} === "gopls") {
  process.stdout.write(responses.cliOutput ?? "", () => process.exit(0));
} else {
  process.stdin.on("data", (chunk) => {
    pending = Buffer.concat([pending, chunk]);
    for (;;) {
      const boundary = pending.indexOf("\\r\\n\\r\\n");
      if (boundary < 0) return;
      const headers = pending.subarray(0, boundary).toString("ascii");
      const length = Number(headers.match(/Content-Length: (\\d+)/i)?.[1]);
      if (!Number.isSafeInteger(length) || length < 0 || pending.length < boundary + 4 + length) return;
      const body = pending.subarray(boundary + 4, boundary + 4 + length);
      pending = pending.subarray(boundary + 4 + length);
      handle(JSON.parse(body.toString("utf8")));
    }
  });
  process.stdin.on("end", () => process.exit(0));
}
`;
  await Promise.all([
    writeFile(log, "", "utf8"),
    writeFile(executable, source, "utf8"),
  ]);
  await chmod(executable, 0o700);
  process.env.PATH = `${directory}${path.delimiter}${originalPATH ?? ""}`;
  return {executable, log};
}

function location(uri: string, line: number, startCharacter: number, endCharacter: number) {
  return {
    uri,
    range: {
      start: {line, character: startCharacter},
      end: {line, character: endCharacter},
    },
  };
}

function protocolLog(text: string): Array<{event: string; args?: string[]; message?: {method?: string}}> {
  return text.trim().split("\n").filter(Boolean).map((row) => JSON.parse(row));
}

afterEach(async () => {
  process.chdir(originalCWD);
  if (originalPATH === undefined) delete process.env.PATH;
  else process.env.PATH = originalPATH;
  await Promise.all(directories.splice(0).map((directory) => rm(directory, {recursive: true, force: true})));
});

describe("msymbol batched queries", () => {
  test("parses PATH:LINE and a JSON-quoted PATH:LINE, and selects the last qualified-name segment", async () => {
    const directory = await temporaryDirectory("msymbol-operands-");
    process.chdir(directory);
    const source = "export function Pick() {\n  return 1;\n}\n";
    const files = ["sample.ts", "path with spaces.ts"];
    await Promise.all(files.map((file) => writeFile(file, source, "utf8")));
    const tool = createMSymbolTool("start: TEST");
    expect(await tool.parse('def "path with spaces.ts":1 pkg.Pick', {})).toEqual([
      "def", "path with spaces.ts:1", "pkg.Pick",
    ]);

    for (const [file, operand] of [
      [files[0], "sample.ts:1"],
      [files[1], '"path with spaces.ts":1'],
    ] as const) {
      const uri = pathToFileURL(path.join(directory, file)).href;
      await installLSPFixture("tsc", {
        definition: location(uri, 0, 16, 20),
        references: [location(uri, 0, 16, 20)],
      });
      const result = await tool.execute(["def", operand, "pkg.Pick"], executionContext);
      expect(result.exitCode).toBe(0);
      expect(result.stdout).toBe(`"${file}":1-3\n${source}`);
      expect(result.stderr).toBeUndefined();
    }
  });

  test("resolves a unique definition from an outline without a line number", async () => {
    const directory = await temporaryDirectory("msymbol-outline-lookup-");
    process.chdir(directory);
    const file = "sample.ts";
    const source = "export function Pick() {\n  return 1;\n}\n";
    await writeFile(file, source, "utf8");
    const uri = pathToFileURL(path.join(directory, file)).href;
    await installLSPFixture("tsc", {
      definition: location(uri, 0, 16, 20),
      references: [],
    });

    const result = await createMSymbolTool("start: TEST").execute(["def", file, "Pick"], executionContext);
    expect(result).toEqual({stdout: `"${file}":1-3\n${source}`, exitCode: 0, terminationReason: "resolver_cleanup"});
  });

  test("resolves a unique Go outline definition without a line number", async () => {
    const directory = await temporaryDirectory("msymbol-go-outline-lookup-");
    process.chdir(directory);
    const file = "sample.go";
    const source = "package p\nfunc Pick() {\n  println(1)\n}\n";
    await writeFile(file, source, "utf8");
    const uri = pathToFileURL(path.join(directory, file)).href;
    const fixture = await installLSPFixture("gopls", {
      definition: location(uri, 1, 5, 9),
      references: [],
      cliOutput: `${JSON.stringify({span: {
        uri,
        start: {line: 2, column: 6, offset: Buffer.byteLength(source.slice(0, source.indexOf("Pick")))},
        end: {line: 2, column: 10, offset: Buffer.byteLength(source.slice(0, source.indexOf("Pick") + "Pick".length))},
      }})}\n`,
    });

    const result = await createMSymbolTool("start: TEST").execute(["def", file, "pkg.Pick"], executionContext);
    expect(result).toEqual({
      stdout: `"${file}":2-4\n${source.split("\n").slice(1, 4).join("\n")}\n`,
      exitCode: 0,
      terminationReason: "resolver_cleanup",
    });
    const log = protocolLog(await readFile(fixture.log, "utf8"));
    expect(log.filter((entry) => entry.event === "start")).toEqual([{
      event: "start",
      args: ["definition", "-json", `${path.join(directory, file)}:#${Buffer.byteLength(source.slice(0, source.indexOf("Pick")))}`],
    }]);
    expect(log.filter((entry) => entry.message?.method === "initialize")).toHaveLength(0);
  });

  test("resolves unique Go outline references without a line number", async () => {
    const directory = await temporaryDirectory("msymbol-go-outline-refs-");
    process.chdir(directory);
    const file = "sample.go";
    const source = "package p\nfunc Pick() {}\nfunc Use() { Pick() }\n";
    await writeFile(file, source, "utf8");
    const absolute = path.join(directory, file);
    const fixture = await installLSPFixture("gopls", {
      definition: [],
      references: [],
      cliOutput: `${absolute}:2:6-10\n${absolute}:3:14-18\n`,
    });

    const result = await createMSymbolTool("start: TEST").execute(["refs", file, "Pick"], executionContext);
    expect(result).toEqual({
      stdout: `"${file}":\n2 func Pick() {}\n3 func Use() { Pick() }\n`,
      exitCode: 0,
      terminationReason: "resolver_cleanup",
    });
    const log = protocolLog(await readFile(fixture.log, "utf8"));
    expect(log.filter((entry) => entry.event === "start")).toEqual([{
      event: "start",
      args: ["references", "-d", `${absolute}:#${Buffer.byteLength(source.slice(0, source.indexOf("Pick")))}`],
    }]);
  });

  test("reports absent and ambiguous no-line outline selections", async () => {
    const directory = await temporaryDirectory("msymbol-outline-errors-");
    process.chdir(directory);
    await Promise.all([
      writeFile("duplicate.ts", "interface Pick {}\nfunction Pick() {}\n", "utf8"),
      writeFile("absent.go", "package p\nfunc Other() {}\n", "utf8"),
      writeFile("local.go", "package p\nfunc Other() {\n  value := 1\n  println(value)\n}\n", "utf8"),
    ]);
    const tool = createMSymbolTool("start: TEST");

    const ambiguous = await tool.execute(["def", "duplicate.ts", "Pick"], executionContext);
    expect(ambiguous).toEqual({stderr: "msymbol: Pick has 2 outline matches (lines 1, 2); supply LINE\n", exitCode: 1, failureClass: "resolver_error"});
    const absent = await tool.execute(["def", "absent.go", "Missing"], executionContext);
    expect(absent).toEqual({stderr: "msymbol: Missing has no outline declaration; no matching token in this file\n", exitCode: 1, failureClass: "resolver_error"});
    const local = await tool.execute(["refs", "local.go", "value"], executionContext);
    expect(local).toEqual({stderr: "msymbol: value has no outline declaration; supply LINE, such as a token line: 3, 4\n", exitCode: 1, failureClass: "resolver_error"});
  });

  test("batches Go definition and references in one gopls LSP session with exact grouped output", async () => {
    const directory = await temporaryDirectory("msymbol-go-batch-");
    process.chdir(directory);
    const input = "package p\nfunc Pick() {\n  println(1)\n}\nfunc Use() { Pick() }\n";
    const other = "package p\nfunc Other() { Pick() }\n";
    await Promise.all([
      writeFile("sample.go", input, "utf8"),
      writeFile("other.go", other, "utf8"),
    ]);
    const inputURI = pathToFileURL(path.join(directory, "sample.go")).href;
    const otherURI = pathToFileURL(path.join(directory, "other.go")).href;
    const fixture = await installLSPFixture("gopls", {
      definition: location(inputURI, 1, 5, 9),
      references: [location(inputURI, 1, 5, 9), location(inputURI, 4, 13, 17), location(otherURI, 1, 15, 19)],
    });

    const result = await createMSymbolTool("start: TEST").execute(
      ["def", "sample.go:2", "pkg.Pick", "refs", '"sample.go":5', "pkg.Pick"],
      executionContext,
    );
    expect(result).toEqual({
      stdout: [
        `"sample.go":2-4\n${input.split("\n").slice(1, 4).join("\n")}\n`,
        '"sample.go":\n2 func Pick() {\n5 func Use() { Pick() }\n',
        '"other.go":\n2 func Other() { Pick() }\n',
      ].join(""),
      exitCode: 0,
      terminationReason: "resolver_cleanup",
    });
    const log = protocolLog(await readFile(fixture.log, "utf8"));
    expect(log.filter((entry) => entry.event === "start")).toEqual([{event: "start", args: ["serve"]}]);
    expect(log.filter((entry) => entry.message?.method === "initialize")).toHaveLength(1);
    expect(log.filter((entry) => entry.message?.method === "textDocument/definition")).toHaveLength(1);
    expect(log.filter((entry) => entry.message?.method === "textDocument/references")).toHaveLength(1);
    expect(result.stderr).toBeUndefined();
  });

  test("batches TypeScript tuples in one tsc LSP session", async () => {
    const directory = await temporaryDirectory("msymbol-typescript-batch-");
    process.chdir(directory);
    const file = "sample.ts";
    const source = "export function Pick() {\n  return 1;\n}\nPick();\n";
    await writeFile(file, source, "utf8");
    const uri = pathToFileURL(path.join(directory, file)).href;
    const fixture = await installLSPFixture("tsc", {
      definition: location(uri, 0, 16, 20),
      references: [location(uri, 0, 16, 20), location(uri, 3, 0, 4)],
    });

    const result = await createMSymbolTool("start: TEST").execute(
      ["def", file, "1", "Pick", "refs", file, "4", "Pick"],
      executionContext,
    );
    expect(result).toEqual({
      stdout: `"sample.ts":1-3\n${source.split("\n").slice(0, 3).join("\n")}\n"sample.ts":\n1 export function Pick() {\n4 Pick();\n`,
      exitCode: 0,
      terminationReason: "resolver_cleanup",
    });
    const log = protocolLog(await readFile(fixture.log, "utf8"));
    expect(log.filter((entry) => entry.event === "start")).toEqual([{event: "start", args: ["--lsp", "--stdio"]}]);
    expect(log.filter((entry) => entry.message?.method === "initialize")).toHaveLength(1);
    expect(log.filter((entry) => entry.message?.method === "textDocument/definition")).toHaveLength(1);
    expect(log.filter((entry) => entry.message?.method === "textDocument/references")).toHaveLength(1);
    expect(result.stderr).toBeUndefined();
  });

  test("labels only unseen runs for overlapping TypeScript definition spans", async () => {
    const directory = await temporaryDirectory("msymbol-overlapping-definitions-");
    process.chdir(directory);
    const file = "sample.ts";
    const source = "class Widget {\n  method() {\n    return 1;\n  }\n}\n";
    await writeFile(file, source, "utf8");
    const uri = pathToFileURL(path.join(directory, file)).href;
    await installLSPFixture("tsc", {
      definition: [location(uri, 1, 2, 8), location(uri, 0, 6, 12)],
      references: [],
    });

    const result = await createMSymbolTool("start: TEST").execute(
      ["def", file, "2", "method"],
      executionContext,
    );
    expect(result).toEqual({
      stdout: [
        '"sample.ts":2-4\n  method() {\n    return 1;\n  }\n',
        '"sample.ts":1-1\nclass Widget {\n',
        '"sample.ts":5-5\n}\n',
      ].join(""),
      exitCode: 0,
      terminationReason: "resolver_cleanup",
    });
  });

  test("shares one output budget across tuples and retains the remaining concatenated output", async () => {
    const directory = await temporaryDirectory("msymbol-shared-output-limit-");
    process.chdir(directory);
    const file = "sample.ts";
    const source = "export function Pick() {\n  return 1;\n}\nPick();\n";
    await writeFile(file, source, "utf8");
    const uri = pathToFileURL(path.join(directory, file)).href;
    await installLSPFixture("tsc", {
      definition: location(uri, 0, 16, 20),
      references: [location(uri, 0, 16, 20), location(uri, 3, 0, 4)],
    });
    const definitionOutput = `"sample.ts":1-3\n${source.split("\n").slice(0, 3).join("\n")}\n`;
    const referencesOutput = '"sample.ts":\n1 export function Pick() {\n4 Pick();\n';
    const tokenLimit = countGPT5Tokens(definitionOutput);

    const result = await createMSymbolTool("start: TEST").execute(
      ["--max-tokens", String(tokenLimit), "def", file, "1", "Pick", "refs", file, "4", "Pick"],
      executionContext,
    );
    expect(result).toEqual({
      stdout: definitionOutput,
      stderr: `msymbol: output incomplete: ${tokenLimit}-token limit reached\n`,
      exitCode: 1,
      omittedOutput: {stdout: referencesOutput, stderr: "", stdoutKind: "rows"},
      failureClass: "output_limit",
      terminationReason: "resolver_cleanup",
    });
  });

  test("reports nearby token lines and the exact same-line ambiguity count", async () => {
    const directory = await temporaryDirectory("msymbol-token-diagnostics-");
    process.chdir(directory);
    const source = "package p\nfunc Pick() {}\nfunc Other() {}\nfunc Use() { Pick(); Pick() }\n";
    await writeFile("sample.go", source, "utf8");
    const tool = createMSymbolTool("start: TEST");

    const missing = await tool.execute(["refs", "sample.go", "3", "Pick"], executionContext);
    expect(missing).toEqual({
      stderr: "msymbol: Pick is not a symbol token on the selected line; nearby lines: 2, 4\n",
      exitCode: 1,
      failureClass: "resolver_error",
    });

    const ambiguous = await tool.execute(["refs", "sample.go", "4", "Pick"], executionContext);
    expect(ambiguous).toEqual({
      stderr: "msymbol: Pick is ambiguous on the selected line (2 occurrences); supply N\n",
      exitCode: 1,
      failureClass: "resolver_error",
    });

    const typescript = "function Pick() {}\nfunction Other() {}\nfunction Use() { Pick(); Pick(); }\n";
    await writeFile("sample.ts", typescript, "utf8");
    const tsMissing = await tool.execute(["refs", "sample.ts", "2", "Pick"], executionContext);
    expect(tsMissing).toEqual({
      stderr: "msymbol: Pick is not a symbol token on the selected line; nearby lines: 1, 3\n",
      exitCode: 1,
      failureClass: "resolver_error",
    });

    const tsAmbiguous = await tool.execute(["refs", "sample.ts", "3", "Pick"], executionContext);
    expect(tsAmbiguous).toEqual({
      stderr: "msymbol: Pick is ambiguous on the selected line (2 occurrences); supply N\n",
      exitCode: 1,
      failureClass: "resolver_error",
    });
  });

  test("finds nearby true tokens once despite many comment and string mentions", async () => {
    const directory = await temporaryDirectory("msymbol-nearby-token-scan-");
    process.chdir(directory);
    const comments = Array.from({length: 2_000}, (_, index) => `// Widget is only mentioned here ${index}`);
    const source = [
      ...comments,
      'const message = "Widget is only mentioned in this string";',
      "function Outer() {",
      "  const value = Widget;",
      "}",
      "function Later() {",
      "  Widget();",
      "}",
      "",
    ].join("\n");
    await writeFile("sample.ts", source, "utf8");
    const logicalLine = spyOn(LineMap.prototype, "logicalLine");
    try {
      const result = await createMSymbolTool("start: TEST").execute(
        ["refs", "sample.ts", "1000", "Widget"],
        executionContext,
      );
      expect(result).toEqual({
        stderr: "msymbol: Widget is not a symbol token on the selected line; nearby lines: 2003, 2006\n",
        exitCode: 1,
        failureClass: "resolver_error",
      });
      // The selected row is read for validation and token selection; the 2,000
      // comment rows must not trigger a per-candidate source traversal.
      expect(logicalLine).toHaveBeenCalledTimes(2);
    } finally {
      logicalLine.mockRestore();
    }
  });

  test.skipIf(Bun.which("tsc") === null)("runs a real TypeScript definition and reference batch", async () => {
    const directory = await temporaryDirectory("msymbol-real-typescript-batch-");
    process.chdir(directory);
    const target = [
      "export function Pick(value: number) {",
      "  return value + 1;",
      "}",
      "",
    ].join("\n");
    const input = [
      'import {Pick} from "./target";',
      "export const result = Pick(1);",
      "",
    ].join("\n");
    await Promise.all([
      writeFile("tsconfig.json", JSON.stringify({compilerOptions: {strict: true}, include: ["*.ts"]}), "utf8"),
      writeFile("target.ts", target, "utf8"),
      writeFile("input.ts", input, "utf8"),
    ]);
    const result = await createMSymbolTool("start: TEST").execute(
      ["def", "input.ts", "2", "Pick", "refs", "input.ts", "2", "Pick"],
      executionContext,
    );
    expect(result.exitCode).toBe(0);
    expect(result.stdout).toBe([
      `"target.ts":1-3\n${target.split("\n").slice(0, 3).join("\n")}\n`,
      '"input.ts":\n1 import {Pick} from "./target";\n2 export const result = Pick(1);\n',
      '"target.ts":\n1 export function Pick(value: number) {\n',
    ].join(""));
  }, 30_000);

  test("fails a batch atomically when a later tuple has invalid input", async () => {
    const directory = await temporaryDirectory("msymbol-invalid-batch-");
    process.chdir(directory);
    await writeFile("sample.ts", "export function Pick() {}\n", "utf8");
    const result = await createMSymbolTool("start: TEST").execute(
      ["def", "sample.ts", "1", "Pick", "refs", "sample.ts", "99", "Pick"],
      executionContext,
    );
    expect(result.exitCode).toBe(1);
    expect(result.stdout).toBeUndefined();
  });

  test("fails a batch without output when an input changes during resolver work", async () => {
    const directory = await temporaryDirectory("msymbol-changing-batch-");
    process.chdir(directory);
    const file = "sample.ts";
    const source = "export function Pick() {}\nPick();\n";
    const changedSource = source.replace("Pick();", "Pick(); Pick();");
    const target = path.join(directory, file);
    await writeFile(target, source, "utf8");
    const uri = pathToFileURL(target).href;
    await installLSPFixture("tsc", {
      definition: location(uri, 0, 16, 20),
      references: [location(uri, 0, 16, 20), location(uri, 1, 0, 4)],
      mutateOnMethod: "textDocument/definition",
      mutatePath: target,
      mutatedSource: changedSource,
    });

    const result = await createMSymbolTool("start: TEST").execute(
      ["def", file, "1", "Pick", "refs", file, "2", "Pick"],
      executionContext,
    );
    expect(result).toEqual({
      stderr: "msymbol: input changed during query\n",
      exitCode: 1,
      failureClass: "resolver_error",
      terminationReason: "resolver_cleanup",
    });
  });

  test("names the resolver time limit and a narrower-workspace recovery action", async () => {
    const directory = await temporaryDirectory("msymbol-timeout-");
    process.chdir(directory);
    await writeFile("sample.ts", "export function Pick() {}\n", "utf8");
    const uri = pathToFileURL(path.join(directory, "sample.ts")).href;
    await installLSPFixture("tsc", {
      definition: location(uri, 0, 16, 20),
      references: [],
      hangOnInitialize: true,
    });
    const timers = spyOn(globalThis, "setTimeout").mockImplementation((handler, delay, ...arguments_) =>
      originalSetTimeout(handler, delay === 30_000 ? 1 : delay, ...arguments_));
    try {
      const result = await createMSymbolTool("start: TEST").execute(
        ["def", "sample.ts", "1", "Pick"],
        executionContext,
      );
      expect(result).toEqual({
        stderr: "msymbol: resolver exceeded the 30 s limit; retry with a narrower --workspace ROOT\n",
        exitCode: 1,
        failureClass: "resolver_timeout",
        terminationReason: "resolver_cleanup",
      });
    } finally {
      timers.mockRestore();
    }
  });
});
