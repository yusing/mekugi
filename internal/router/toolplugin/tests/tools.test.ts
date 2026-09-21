import {afterEach, beforeEach, describe, expect, spyOn, test} from "bun:test";
import {spawnSync} from "node:child_process";
import {chmod, mkdtemp, mkdir, readFile, rm, symlink, writeFile} from "node:fs/promises";
import {tmpdir} from "node:os";
import path from "node:path";
import {pathToFileURL} from "node:url";

import {formatVerifiedRow, hashLine} from "mekugi:core/v1";
import {countGPT5Tokens, VerifiedRowOutput} from "../../../../plugins/common.ts";
import {createHGrepTool, splitArguments} from "../../../../plugins/hgrep.ts";
import {createHCatTool} from "../../../../plugins/hcat.ts";
import {createHSymbolTool} from "../../../../plugins/hsymbol.ts";
import {runLSPQuery} from "../../../../plugins/lsp.ts";
import {
  createInspectFileTool,
  goDeclarationRange,
  inspectFileDescription,
  LineMap,
} from "../../../../plugins/inspect_file.ts";
import plugin from "../../../../plugins/tools.ts";

const originalCWD = process.cwd();
const originalTmpdir = process.env.TMPDIR;
const originalPath = process.env.PATH;
const pluginBin = path.resolve(import.meta.dir, "../../../../plugins/node_modules/.bin");
const temporaryDirectories: string[] = [];
const executionContext = {stdinFD: null, scriptReadFD: null, scriptWriteFD: null, outputBudgetBytes: 16 * 1024 * 1024};

async function temporaryDirectory(prefix: string): Promise<string> {
  const directory = await mkdtemp(path.join(tmpdir(), prefix));
  temporaryDirectories.push(directory);
  return directory;
}

type FakeGopls = {
  callsPath: string;
  respond(stdout: string, stderr?: string, exitCode?: number): Promise<void>;
  mutateBeforeResponse(target: string, source: string): Promise<void>;
};

async function installFakeGopls(): Promise<FakeGopls> {
  const directory = await temporaryDirectory("hsymbol-gopls-");
  const executable = path.join(directory, "gopls");
  const callsPath = path.join(directory, "calls");
  await Promise.all([
    writeFile(callsPath, "", "utf8"),
    writeFile(path.join(directory, "stdout"), "", "utf8"),
    writeFile(path.join(directory, "stderr"), "", "utf8"),
    writeFile(path.join(directory, "exit-code"), "0\n", "utf8"),
    writeFile(path.join(directory, "mutate-path"), "", "utf8"),
    writeFile(path.join(directory, "mutate-source"), "", "utf8"),
    writeFile(executable, `#!/bin/sh
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s\\n' "$PWD" > "$root/cwd"
printf '%s\\n' "$*" >> "$root/calls"
if [ -s "$root/mutate-path" ]; then
  target=$(cat "$root/mutate-path")
  cat "$root/mutate-source" > "$target"
fi
cat "$root/stdout"
cat "$root/stderr" >&2
exit "$(cat "$root/exit-code")"
`, "utf8"),
  ]);
  await chmod(executable, 0o700);
  process.env.PATH = `${directory}${path.delimiter}${originalPath ?? ""}`;
  return {
    callsPath,
    async respond(stdout, stderr = "", exitCode = 0) {
      await Promise.all([
        writeFile(path.join(directory, "stdout"), stdout, "utf8"),
        writeFile(path.join(directory, "stderr"), stderr, "utf8"),
        writeFile(path.join(directory, "exit-code"), `${exitCode}\n`, "utf8"),
      ]);
    },
    async mutateBeforeResponse(target, source) {
      await Promise.all([
        writeFile(path.join(directory, "mutate-path"), target, "utf8"),
        writeFile(path.join(directory, "mutate-source"), source, "utf8"),
      ]);
    },
  };
}

function definitionJSON(filePath: string, source: string, nameOffset: number, name: string): string {
  const lines = new LineMap(source);
  return `${JSON.stringify({
    span: {
      uri: pathToFileURL(filePath).href,
      start: {
        line: lines.lineAt(nameOffset),
        column: 1,
        offset: Buffer.byteLength(source.slice(0, nameOffset), "utf8"),
      },
      end: {
        line: lines.lineAt(nameOffset),
        column: 1 + name.length,
        offset: Buffer.byteLength(source.slice(0, nameOffset + name.length), "utf8"),
      },
    },
    description: name,
  }, null, 2)}\n`;
}

async function inspect(...argv: string[]) {
  const tool = createInspectFileTool("description", String.raw`\A.+\z`);
  const execution = await tool.execute(argv, executionContext);
  return {...execution, raw: execution.stdout ?? "", result: JSON.parse(execution.stdout ?? "")};
}

async function inspectOutline(name: string): Promise<Record<string, unknown>[]> {
  return (await inspect(name)).result.data.outline;
}

const hashedRowPattern = /^(\d+):[0-9a-f]{4}$/u;

function outlineLine(entry: Record<string, unknown>, field = "line"): number {
  const value = String(entry[field]);
  const match = hashedRowPattern.exec(value);
  if (match === null) {
    throw new Error(`invalid ${field} identity ${value}`);
  }
  return Number(match[1]);
}

function rustRegexMatches(pattern: string, input: string): boolean {
  const result = spawnSync(
    "rg",
    ["--no-config", "--multiline", "--quiet", "--", pattern, "-"],
    {input, encoding: "utf8"},
  );
  if (result.error !== undefined) {
    throw result.error;
  }
  if (result.status !== 0 && result.status !== 1) {
    throw new Error(`rg regex evaluation failed: ${result.stderr}`);
  }
  return result.status === 0;
}

function contentWithFormattedTokenCount(
  tokens: number,
  format: (content: string) => string,
): string {
  for (let variant = 0; variant < 16; variant += 1) {
    let low = 0;
    let high = tokens + 1;
    const candidate = (repetitions: number) => `row${variant}${" x".repeat(repetitions)}`;
    while (low <= high) {
      const repetitions = Math.floor((low + high) / 2);
      const content = candidate(repetitions);
      const count = countGPT5Tokens(format(content));
      if (count === tokens) {
        return content;
      }
      if (count < tokens) {
        low = repetitions + 1;
      } else {
        high = repetitions - 1;
      }
    }
    for (
      let repetitions = Math.max(0, low - 64);
      repetitions <= Math.min(tokens + 1, low + 64);
      repetitions += 1
    ) {
      const content = candidate(repetitions);
      if (countGPT5Tokens(format(content)) === tokens) {
        return content;
      }
    }
  }
  throw new Error(`cannot construct ${tokens}-token formatted row fixture`);
}

beforeEach(async () => {
  process.env.TMPDIR = await temporaryDirectory("reader-retention-test-");
});

afterEach(async () => {
  if (originalTmpdir === undefined) {
    delete process.env.TMPDIR;
  } else {
    process.env.TMPDIR = originalTmpdir;
  }
  process.chdir(originalCWD);
  if (originalPath === undefined) {
    delete process.env.PATH;
  } else {
    process.env.PATH = originalPath;
  }
  await Promise.all(
    temporaryDirectories.splice(0).map((directory) => rm(directory, {recursive: true, force: true})),
  );
});

describe("verified-row output", () => {
  test("matches the shared Go GPT-5 token fixtures", async () => {
    const fixtures = JSON.parse(await readFile(
      new URL("./testdata/gpt5_tokens.json", import.meta.url),
      "utf8",
    )) as {text: string; tokens: number}[];
    for (const fixture of fixtures) {
      expect(countGPT5Tokens(fixture.text)).toBe(fixture.tokens);
    }
  });

  test("byte-bound admission preserves exact token limits", () => {
    const unicode = new VerifiedRowOutput(1);
    expect(unicode.append("🙂")).toBe(true);
    expect(unicode.append("x")).toBe(false);
    expect(unicode.current).toBe("🙂");

    const compressible = new VerifiedRowOutput(15_000);
    const row = " x".repeat(8000);
    expect(Buffer.byteLength(row)).toBeGreaterThan(15_000);
    expect(countGPT5Tokens(row)).toBeLessThan(15_000);
    expect(compressible.append(row)).toBe(true);
    expect(compressible.append(" later\n")).toBe(true);
    expect(compressible.incomplete).toBe(false);
  });

  test("uses strict GPT-5 limits with complete rows", () => {
    const exact = new VerifiedRowOutput(15_500);
    const atSoftLimit = contentWithFormattedTokenCount(15_000, (content) => `${content}\n`);
    expect(exact.append(atSoftLimit)).toBe(true);
    expect(exact.incomplete).toBe(false);

    const overshootContent = contentWithFormattedTokenCount(
      15_500,
      (content) => `${atSoftLimit}${content}\n`,
    );
    const overshootRow = `${overshootContent}\n`;
    expect(countGPT5Tokens(atSoftLimit + overshootRow)).toBe(15_500);
    expect(exact.append(overshootRow)).toBe(true);
    expect(exact.incomplete).toBe(false);
    expect(exact.append("later\n")).toBe(false);
    expect(exact.incomplete).toBe(true);
    expect(exact.current).toBe(atSoftLimit + overshootRow);

    const tooLarge = new VerifiedRowOutput(15_500);
    const aboveMaximum = contentWithFormattedTokenCount(15_501, (content) => `${content}\n`);
    expect(tooLarge.append(`${aboveMaximum}\n`)).toBe(false);
    expect(tooLarge.current).toBe("");
    expect(tooLarge.incomplete).toBe(true);
  });
});

describe("reader budgets and previews", () => {
  test("shares strict budgets and explicit previews without changing row identities", async () => {
    const directory = await temporaryDirectory("reader-preview-");
    process.chdir(directory);
    const content = `needle 🙂${" x".repeat(20_000)}`;
    await writeFile("long.txt", `${content}\nneedle second\n`, "utf8");
    const tools = [
      {tool: createHCatTool("test", ""), args: ["long.txt"]},
      {tool: createHGrepTool("test", ""), args: ["-F", "needle", "long.txt"]},
    ];
    for (const {tool, args} of tools) {
      const result = await tool.execute(["--max-tokens", "200", "--preview-bytes", "9", ...args], executionContext);
      expect(result.exitCode).toBe(0);
      expect(countGPT5Tokens(result.stdout ?? "")).toBeLessThanOrEqual(200);
      const rows = result.stdout!.trimEnd().split("\n").map((line) => JSON.parse(line));
      expect(rows).toHaveLength(2);
      expect(rows[0]).toMatchObject({
        row: `1:${hashLine(content)}`,
        preview: "needle ",
        source_bytes: Buffer.byteLength(content),
        omitted_bytes: Buffer.byteLength(content) - 7,
      });
      expect(rows[1].row).toBe(`2:${hashLine("needle second")}`);
      const limited = await tool.execute(["--max-tokens", "1", ...args], executionContext);
      expect(limited.exitCode).toBe(1);
      expect(limited.stdout).toBe("");
      expect(limited.stderr).toContain("output incomplete: 1-token limit reached");
      for (const invalid of [
        ["--max-tokens", "0"], ["--max-tokens", "15501"], ["--preview-bytes", "65537"],
        ["--preview-bytes", "2", "--preview-bytes", "3"], ["--max-tokens", "1e3"],
      ]) {
        const rejected = await tool.execute([...invalid, ...args], executionContext);
        expect(rejected.exitCode).toBe(1);
        expect(rejected.stderr).toContain("requires one integer");
      }
    }
  });

  test("keeps admitted rows within a strict caller budget", async () => {
    const directory = await temporaryDirectory("reader-budget-");
    process.chdir(directory);
    await writeFile("rows.txt", "needle first\nneedle second\n", "utf8");
    for (const {tool, args} of [
      {tool: createHCatTool("test", ""), args: ["rows.txt"]},
      {tool: createHGrepTool("test", ""), args: ["-F", "needle", "rows.txt"]},
    ]) {
      const full = await tool.execute(args, executionContext);
      const first = full.stdout!.split("\n")[0] + "\n";
      const budget = countGPT5Tokens(first);
      const limited = await tool.execute(["--max-tokens", String(budget), ...args], executionContext);
      expect(limited.stdout).toBe(first);
      expect(limited.exitCode).toBe(1);
      expect(countGPT5Tokens(limited.stdout!)).toBeLessThanOrEqual(budget);
    }
  });

  test("retains bounded hcat storage and reports the preview source bound", async () => {
    const directory = await temporaryDirectory("reader-preview-bound-");
    process.chdir(directory);
    await writeFile("huge.txt", "a".repeat(2_000_000), "utf8");
    const result = await createHCatTool("test", "").execute(["--preview-bytes", "32", "huge.txt"], executionContext);
    expect(result.exitCode).toBe(1);
    expect(result.stdout).toBe("");
    expect(result.stderr).toContain("row 1 exceeds the 1984000-byte inspection bound");
  });

  test("parses hcat reader options before a quoted path", async () => {
    const tool = createHCatTool("test", "");
    expect(await tool.parse('--preview-bytes 12 --max-tokens 100 "path with spaces" 0:2')).toEqual([
      "--preview-bytes", "12", "--max-tokens", "100", "path with spaces", "1:2",
    ]);
  });
});

describe("hcat line limits", () => {
  test("selects complete head/tail lines before optional token limits", async () => {
    const directory = await temporaryDirectory("hcat-lines-");
    const file = path.join(directory, "rows.txt");
    const tool = createHCatTool("", "");
    await writeFile(file, "one\r\ntwo\rthree\nfour");
    for (const [flags, expected] of [
      [["-n", "2"], formatVerifiedRow(1, "one") + formatVerifiedRow(2, "two")],
      [["--tail", "-n", "2"], formatVerifiedRow(3, "three") + formatVerifiedRow(4, "four")],
      [["-n", "2", "--max-tokens", "100"], formatVerifiedRow(1, "one") + formatVerifiedRow(2, "two")],
      [["--tail", "-n", "2", "--max-tokens", "100"], formatVerifiedRow(3, "three") + formatVerifiedRow(4, "four")],
    ] as const) {
      const result = await tool.execute([...flags, file], executionContext);
      expect(result.stdout).toBe(expected);
      expect(result.exitCode).toBe(1);
      expect(result.stderr).toContain("2-line limit");
    }
    const range = await tool.execute(["--tail", "-n", "1", file, "1:2"], executionContext);
    expect(range.stdout).toBe(formatVerifiedRow(2, "two"));
    expect(await tool.parse('-n 2 --tail "rows.txt"')).toEqual(
      ["-n", "2", "--tail", "rows.txt"]);
    for (const flags of [["-n"], ["-n", "0"], ["-n", "01"], ["-n", "1", "-n", "2"], ["-n", "9007199254740992"]]) {
      expect((await tool.execute([...flags, file], executionContext)).failureClass).toBe("invalid_arguments");
    }
  });

  test("line-only mode admits long complete rows beyond the default token ceiling", async () => {
    const directory = await temporaryDirectory("hcat-line-long-");
    const file = path.join(directory, "rows.txt");
    const content = "word ".repeat(20000);
    await writeFile(file, `${content}\nend`);
    const tool = createHCatTool("", "");
    const head = await tool.execute(["-n", "1", file], executionContext);
    expect(head.stdout).toBe(formatVerifiedRow(1, content));
    const tail = await tool.execute(["--tail", "-n", "2", file], executionContext);
    expect(tail.stdout).toBe(formatVerifiedRow(1, content) + formatVerifiedRow(2, "end"));
    expect(tail.exitCode).toBe(0);
    const limited = await tool.execute(["-n", "1", "--max-tokens", "20", file], executionContext);
    expect(limited.stdout).toBe("");
    const tailLimited = await tool.execute(["--tail", "-n", "2", "--max-tokens", "20", file], executionContext);
    expect(tailLimited.stdout).toBe(formatVerifiedRow(2, "end"));
    await writeFile(file, `first\n${"x".repeat(2_000_000)}`);
    const smallHead = await tool.execute(["-n", "1", file], executionContext);
    expect(smallHead.stdout).toBe(formatVerifiedRow(1, "first"));
    expect(smallHead.stderr).toContain("row 2 exceeds");
    expect(smallHead.omittedOutput).toBeUndefined();
    // Skipping unselected rows must not skip whole-source UTF-8 validation.
    await writeFile(file, Buffer.concat([Buffer.from("first\n"), Buffer.from([0xff])]));
    expect((await tool.execute(["-n", "1", file], executionContext)).stderr).toContain("not UTF-8");

    await writeFile(file, "");
    expect((await tool.execute(["--tail", "-n", "2", file], executionContext)).exitCode).toBe(0);
  });
});

describe("hcat omitted rows", () => {
  test("resumes only omitted rows across logical terminators and bounded selections", async () => {
    const directory = await temporaryDirectory("hcat-resume-");
    const file = path.join(directory, "rows.txt");
    const tool = createHCatTool("", "");
    for (const ending of ["\n", "\r", "\r\n"]) {
      await writeFile(file, ["one", "", "three", "four", "five"].join(ending));
      const head = await tool.execute(["-n", "2", file, "2:9"], executionContext);
      expect(head.stdout).toBe(formatVerifiedRow(2, "") + formatVerifiedRow(3, "three"));
      expect(head.omittedOutput?.stdout).toBe(formatVerifiedRow(4, "four") + formatVerifiedRow(5, "five"));
      expect(head.stderr).toContain("[out of range]");
      const rest = await tool.execute([file, "4:5"], executionContext);
      expect(rest).toEqual({
        stdout: formatVerifiedRow(4, "four") + formatVerifiedRow(5, "five"),
        exitCode: 0,
      });
      const limited = await tool.execute(["-n", "1", file, "2:3"], executionContext);
      expect(limited.omittedOutput?.stdout).toBe(formatVerifiedRow(3, "three"));
      const tail = await tool.execute(["--tail", "-n", "1", file], executionContext);
      expect(tail.omittedOutput?.stdout).toBe(["one", "", "three", "four"].map((row, i) => formatVerifiedRow(i + 1, row)).join(""));
    }
  });

  test("reports the first unadmitted row for strict token and preview budgets", async () => {
    const directory = await temporaryDirectory("hcat-resume-token-");
    const file = path.join(directory, "rows.txt");
    const tool = createHCatTool("", "");
    await writeFile(file, "first\nsecond\nthird\n");
    const budget = countGPT5Tokens(formatVerifiedRow(1, "first"));
    const first = await tool.execute(["--max-tokens", String(budget), file], executionContext);
    expect(first.stdout).toBe(formatVerifiedRow(1, "first"));
    expect(first.omittedOutput?.stdout).toBe(formatVerifiedRow(2, "second") + formatVerifiedRow(3, "third"));
    for (const extra of [[], ["--preview-bytes", "1"]]) {
      const none = await tool.execute(["--max-tokens", "1", ...extra, file], executionContext);
      expect(none.stdout).toBe("");
      expect(none.omittedOutput?.stdout.split("\n").filter(Boolean)).toHaveLength(3);
      expect(none.omittedOutput?.stdoutKind).toBe("rows");
    }
    await writeFile(file, "");
    expect((await tool.execute(["-n", "1", file], executionContext)).stderr).toBeUndefined();
    await writeFile(file, Buffer.from([0xff]));
    expect((await tool.execute(["-n", "1", file], executionContext)).stderr).not.toContain("unread rows");
  });
});

describe("shared reader controls", () => {
  test("accepts budgets around operands and preserves option-valued search patterns", async () => {
    const directory = await temporaryDirectory("reader-options-");
    const file = path.join(directory, "sample.go");
    await writeFile(file, "package p\nfunc First() {}\nfunc Second() {}\n");
    for (const tool of [createHCatTool("", ""), createInspectFileTool("", "")]) {
      const before = await tool.execute(["--max-tokens", "200", file], executionContext);
      const after = await tool.execute([file, "--max-tokens", "200"], executionContext);
      expect(await tool.execute([file, "--max-tokens", "200", "--"], executionContext)).toEqual(before);
      expect(after).toEqual(before);
      for (const input of [`--max-tokens 200 ${JSON.stringify(file)}`, `${JSON.stringify(file)} --max-tokens 200`]) {
        const argv = await tool.parse(input);
        expect(await tool.execute(argv, executionContext)).toEqual(before);
      }
    }
    await writeFile(file, "--max-tokens\nordinary\n");
    const grep = createHGrepTool("", "");
    for (const args of [
      ["-F", "-e", "--max-tokens", file, "--max-tokens", "200"],
      ["--max-tokens", "200", "-F", "--regexp", "--max-tokens", file],
      ["--max-tokens", "200", "-F", "--", "--max-tokens", file],
    ]) {
      const result = await grep.execute(args, executionContext);
      expect(result.exitCode).toBe(0);
      expect(result.stdout).toContain(formatVerifiedRow(1, "--max-tokens"));
    }
  });

  test("bounds omitted rows even when selected line-only rows bypass tokenization", async () => {
    const directory = await temporaryDirectory("reader-omitted-bound-");
    const file = path.join(directory, "rows.txt");
    await writeFile(file, `first\n${"x".repeat(4 * 1024 * 1024)}\nlast\n`);
    const result = await createHCatTool("", "").execute([file, "-n", "1"], executionContext);
    expect(result.stdout).toBe(formatVerifiedRow(1, "first"));
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain("row 2 exceeds");
    expect(result.omittedOutput).toBeUndefined();
  });

  test("tail output remains usable when retained rows exceed capacity", async () => {
    const directory = await temporaryDirectory("reader-capacity-");
    const file = path.join(directory, "rows.txt");
    await writeFile(file, `${("x".repeat(20_000) + "\n").repeat(850)}last\n`);
    const result = await createHCatTool("", "").execute([file, "-n", "1", "--tail"], executionContext);
    expect(result.stdout).toBe(formatVerifiedRow(851, "last"));
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain("recovery bound");
    expect(result.omittedOutput).toBeUndefined();
  });
});

describe("hcat tail", () => {
  test("returns a whole-row suffix within the budget, including ranges and previews", async () => {
    const directory = await temporaryDirectory("hcat-tail-");
    const file = path.join(directory, "rows with spaces.txt");
    await writeFile(file, "first\r\nsecond\rthird\nlast", "utf8");
    const tool = createHCatTool("test", "");
    const last = formatVerifiedRow(4, "last");
    const budget = countGPT5Tokens(last);
    const result = await tool.execute(["--tail", "--max-tokens", String(budget), file], executionContext);
    expect(result.stdout).toBe(last);
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain("output incomplete");
    expect(countGPT5Tokens(result.stdout!)).toBeLessThanOrEqual(budget);

    const complete = await tool.execute(["--max-tokens", "200", "--tail", file, "2:3"], executionContext);
    expect(complete.stdout).toBe(formatVerifiedRow(2, "second") + formatVerifiedRow(3, "third"));
    expect(complete.exitCode).toBe(0);
    const preview = await tool.execute(["--preview-bytes", "2", "--tail", "--max-tokens", "200", file, "4:4"], executionContext);
    expect(JSON.parse(preview.stdout!)).toMatchObject({row: `4:${hashLine("last")}`, preview: "la", omitted_bytes: 2});
    expect(preview.exitCode).toBe(0);
    expect(await tool.parse('--tail --max-tokens 100 "rows with spaces.txt" 0:2')).toEqual([
      "--tail", "--max-tokens", "100", "rows with spaces.txt", "1:2",
    ]);
  });

  test("continues after oversized rows but never skips an oversized final row", async () => {
    const directory = await temporaryDirectory("hcat-tail-long-");
    const file = path.join(directory, "rows");
    const tool = createHCatTool("test", "");
    for (const large of [" x".repeat(1000), "a".repeat(2_000_000)]) {
      await writeFile(file, `first\n${large}\nlast\n`, "utf8");
      const result = await tool.execute(["--tail", "--max-tokens", "20", file], executionContext);
      expect(result.stdout).toBe(formatVerifiedRow(3, "last"));
      expect(result.exitCode).toBe(1);
      await writeFile(file, `first\n${large}`, "utf8");
      const final = await tool.execute(["--tail", "--max-tokens", "20", file], executionContext);
      expect(final.stdout).toBe("");
      expect(final.exitCode).toBe(1);
    }
  });

  test("bounds a long scan while retaining the final rows", async () => {
    const directory = await temporaryDirectory("hcat-tail-scan-");
    const file = path.join(directory, "rows");
    await writeFile(file, "same\n".repeat(10_000), "utf8");
    const result = await createHCatTool("test", "").execute(
      ["--tail", "--max-tokens", "100", file], executionContext);
    expect(result.stdout).toEndWith(formatVerifiedRow(10_000, "same"));
    expect(countGPT5Tokens(result.stdout!)).toBeLessThanOrEqual(100);
    expect(result.exitCode).toBe(1);
  });

  test("handles a long single-piece final row without quadratic tokenization", async () => {
    const directory = await temporaryDirectory("hcat-tail-long-piece-");
    const file = path.join(directory, "rows");
    const content = " ".repeat(1_500_000);
    await writeFile(file, "before\n".repeat(50) + content, "utf8");
    const result = await createHCatTool("test", "").execute(
      ["--tail", "--max-tokens", "15500", file], executionContext);
    expect(result.stdout).toEndWith(formatVerifiedRow(51, content));
    expect(result.exitCode).toBe(0);
  }, 15_000);

  test("validates flags and all source bytes, even outside the retained suffix", async () => {
    const directory = await temporaryDirectory("hcat-tail-validation-");
    const file = path.join(directory, "rows");
    const tool = createHCatTool("test", "");
    for (const options of [["--tail"], ["--tail", "--max-tokens", "20", "--tail"]]) {
      const result = await tool.execute([...options, file], executionContext);
      expect(result.failureClass).toBe("invalid_arguments");
    }
    await writeFile(file, Buffer.concat([Buffer.from([0xff]), Buffer.from("\nlast\n")]));
    const invalid = await tool.execute(["--tail", "--max-tokens", "20", file], executionContext);
    expect(invalid.stdout).toBeUndefined();
    expect(invalid.stderr).toContain("not UTF-8");
    await writeFile(file, "", "utf8");
    const empty = await tool.execute(["--tail", "--max-tokens", "20", file], executionContext);
    expect(empty).toMatchObject({stdout: "", exitCode: 0});
  });
});

describe("hcat built-in plugin", () => {
  test("keeps the private description call-local", () => {
    const description = plugin.tools[0].specification.description.replace(/\s+/g, " ");
    expect(description).toContain("Read one UTF-8 file or inclusive logical-line range");
    expect(description).toContain("`LINE:HASH TEXT`");
    expect(description).toContain("`hcat [-n N] [--max-tokens N] [--preview-bytes N] [--tail] PATH [START:END]`");
    for (const persistent of ["authorized edit", "ordinary read", "HPATCH targets", "through `shell`"]) {
      expect(description).not.toContain(persistent);
    }
  });

  test("declares a single-file regex grammar", () => {
    const format = plugin.tools[0].specification.format;
    expect(format?.syntax).toBe("regex");
    if (format === undefined) {
      throw new Error("hcat grammar format is missing");
    }
    for (const input of [
      "plain.txt",
      "plain.txt 2:9",
      "plain.txt 0:9",
      "\"second file.txt\" 2:3",
      `"quoted\\"file.txt"`,
    ]) {
      expect(rustRegexMatches(format.definition, input)).toBe(true);
    }
    for (const input of [
      "",
      "\nplain.txt",
      "plain.txt\n",
      "plain.txt\nsecond.txt",
      "plain file.txt",
      "plain.txt 2:0",
      "plain.txt 2:3 extra",
      "\"unterminated",
    ]) {
      expect(rustRegexMatches(format.definition, input)).toBe(false);
    }
  });

  test("preserves quoted option-like paths through execution", async () => {
    const directory = await temporaryDirectory("hcat-option-path-");
    process.chdir(directory);
    const tool = createHCatTool("", "");
    for (const name of ["--tail", "--max-tokens", "--preview-bytes", "-n"]) {
      const file = path.join(directory, name);
      await writeFile(file, "first\nsecond\n");
      for (const [prefix, range, expected] of [
        ["", "", formatVerifiedRow(1, "first") + formatVerifiedRow(2, "second")],
        ["-n 1 --tail ", " 2:2", formatVerifiedRow(2, "second")],
      ]) {
        const argv = await tool.parse(`${prefix}${JSON.stringify(name)}${range}`);
        expect(argv).toContain(`./${name}`);
        expect(await tool.execute(argv, executionContext)).toMatchObject({stdout: expected, exitCode: 0});
      }
    }
  });

  test("parses one path and optional range into shell arguments", async () => {
    const tool = createHCatTool("description", "start: TEST");
    const parse = (input: string) => tool.parse(input);


    expect(await parse("plugins/shell.mjs 164:300")).toEqual([
      "plugins/shell.mjs",
      "164:300",
    ]);
    expect(await parse("plugins/shell.mjs 0:300")).toEqual([
      "plugins/shell.mjs",
      "1:300",
    ]);
    expect(await parse("\"path with spaces.txt\" 2:9")).toEqual([
      "path with spaces.txt",
      "2:9",
    ]);
    expect(() => parse("first.txt\nsecond.txt 2:9")).toThrow(
      "invalid bare reader argument",
    );
  });


  test("reads one whole file or range", async () => {
    const directory = await temporaryDirectory("hcat-plugin-");
    process.chdir(directory);
    await writeFile("plain.txt", "alpha\r\nbeta\rgamma\n", "utf8");
    await writeFile("second file.txt", "one\ntwo\nthree", "utf8");
    await writeFile("token-spellings.txt", "<|endoftext|> <|im_start|> <|fim_prefix|>\n", "utf8");

    const tool = createHCatTool("description", "start: TEST");
    const whole = await tool.execute(["plain.txt"], executionContext);
    expect(whole).toEqual({
      stdout: [
        formatVerifiedRow(1, "alpha"),
        formatVerifiedRow(2, "beta"),
        formatVerifiedRow(3, "gamma"),
      ].join(""),
      exitCode: 0,
    });

    const range = await tool.execute(["plain.txt", "2:3"], executionContext);
    expect(range).toEqual({
      stdout: [
        formatVerifiedRow(2, "beta"),
        formatVerifiedRow(3, "gamma"),
      ].join(""),
      exitCode: 0,
    });

    const zeroRange = await tool.execute(["plain.txt", "0:3"], executionContext);
    expect(zeroRange).toEqual(whole);

    const overrun = await tool.execute(["plain.txt", "2:5"], executionContext);
    expect(overrun).toEqual({
      stdout: [
        formatVerifiedRow(2, "beta"),
        formatVerifiedRow(3, "gamma"),
      ].join(""),
      stderr: "hcat: 4-5: [out of range]\n",
      exitCode: 0,
    });

    const tokenSpellings = await tool.execute(["token-spellings.txt"], executionContext);
    expect(tokenSpellings).toEqual({
      stdout: formatVerifiedRow(1, "<|endoftext|> <|im_start|> <|fim_prefix|>"),
      exitCode: 0,
    });

    const outside = await tool.execute(["plain.txt", "4:5"], executionContext);
    expect(outside).toEqual({
      stderr: "hcat: start line 4 is past EOF (3 lines)\n",
      exitCode: 1,
      failureClass: "reader_error",
    });

    const missing = await tool.execute(["missing.txt"], executionContext);
    expect(missing).toEqual({
      stderr: "hcat: ENOENT: no such file or directory\n",
      exitCode: 1,
      failureClass: "not_found",
    });

    expect(await tool.execute(["@shell/call-id"], executionContext)).toEqual(missing);
  });

  test("rejects malformed ranges, non-regular files, and invalid UTF-8", async () => {
    const directory = await temporaryDirectory("hcat-plugin-");
    process.chdir(directory);
    await writeFile("short.txt", "one\n", "utf8");
    await writeFile("binary.txt", Uint8Array.from([0xff]));
    await mkdir("folder");
    if (process.platform !== "win32") {
      const created = spawnSync("mkfifo", ["pipe"]);
      expect(created.status).toBe(0);
    }

    const tool = createHCatTool("description", "start: TEST");
    for (const [argv, diagnostic] of [
      [["short.txt", "3:2"], "range start exceeds end"],
      [["binary.txt"], "not UTF-8"],
      [["folder"], "not a regular file"],
      ...(process.platform === "win32" ? [] : [[["pipe"], "not a regular file"]]),
    ] as const) {
      const result = await tool.execute([...argv], executionContext);
      expect(result.exitCode).toBe(1);
      expect(result.stderr).toContain(diagnostic);
    }
  });

  test("retains whole admitted rows and fails when later rows exceed the token limit", async () => {
    const directory = await temporaryDirectory("hcat-limit-");
    process.chdir(directory);
    const first = contentWithFormattedTokenCount(4_000, (content) => formatVerifiedRow(1, content));
    await writeFile("large.txt", `${first}\nsecond\nthird\n`, "utf8");

    const tool = createHCatTool("description", "start: TEST");
    const result = await tool.execute(["large.txt"], executionContext);
    expect(result).toEqual({
      stdout: formatVerifiedRow(1, first),
      stderr: "hcat: output incomplete: 4000-token limit reached\n",
      omittedOutput: {stdout: formatVerifiedRow(2, "second") + formatVerifiedRow(3, "third"), stderr: "", stdoutKind: "rows"},
      exitCode: 1,
      failureClass: "output_limit",
    });
  });

  test("discards an unavoidably over-limit row while streaming", async () => {
    const directory = await temporaryDirectory("hcat-oversized-row-");
    process.chdir(directory);
    await writeFile("large.txt", " ".repeat(15_500 * 128 + 1), "utf8");

    const tool = createHCatTool("description", "start: TEST");
    const result = await tool.execute(["large.txt"], executionContext);
    expect(result).toEqual({
      stdout: "",
      stderr: "hcat: row 1 exceeds the 1984000-byte inspection bound; use a byte-window reader\n",
      exitCode: 1,
      failureClass: "output_limit",
    });
  });

});

describe("hgrep built-in plugin", () => {
  test("keeps the private description call-local", () => {
    const description = plugin.tools[1].specification.description.replace(/\s+/g, " ");
    expect(description).toContain("Search files with supported ripgrep arguments");
    expect(description).toContain("`\"PATH\":LINE:HASH TEXT`");
    for (const persistent of ["authorized edit", "ordinary search", "HPATCH targets", "through `shell`"]) {
      expect(description).not.toContain(persistent);
    }
  });

  test("splits literal arguments without shell evaluation", () => {
    expect(splitArguments(
      `-F 'two words' "path with spaces.txt" semi;colon dollar$(value) back\\\\slash`,
    )).toEqual([
      "-F",
      "two words",
      "path with spaces.txt",
      "semi;colon",
      "dollar$(value)",
      "back\\slash",
    ]);
    for (const input of ["", "'unterminated", "\"escape\\", "one\nsecond"]) {
      expect(() => splitArguments(input)).toThrow();
    }
  });

  test("declares a strict Rust regex and accepts one transport newline", async () => {
    const format = plugin.tools[1].specification.format;
    expect(format?.syntax).toBe("regex");
    if (format === undefined) {
      throw new Error("hgrep grammar format is missing");
    }
    const tool = createHGrepTool("description", format.definition);
    const accepted = [
      "needle",
      "  -F 'two words' \"path with spaces.txt\"\t",
      "empty''argument",
      "escaped\\ space",
      "\"\"",
    ];
    for (const input of accepted) {
      expect(rustRegexMatches(format.definition, input)).toBe(true);
      const parsed = await tool.parse(input);
      expect(await tool.argv(parsed)).toEqual(splitArguments(input));
    }
    for (const input of ["needle\n", "needle\r\n"]) {
      expect(rustRegexMatches(format.definition, input)).toBe(false);
      const parsed = await tool.parse(input);
      expect(await tool.argv(parsed)).toEqual(["needle"]);
    }
    for (const input of [
      "",
      " \t",
      "\nneedle",
      "needle\n\n",
      "needle\r",
      "one\r\ntwo",
      "'unterminated",
      "\"escape\\",
      "'one\nsecond'",
    ]) {
      expect(rustRegexMatches(format.definition, input)).toBe(false);
      expect(() => tool.parse(input)).toThrow();
    }
  });


  test("maps repeated matches after bare CR to exact logical rows", async () => {
    const directory = await temporaryDirectory("hgrep-logical-");
    process.chdir(directory);
    await writeFile("mixed.txt", "prefix\rskip\nsame\nsame\n");
    const result = await createHGrepTool("test", "").execute(["-F", "same", "mixed.txt"], executionContext);
    expect(result).toEqual({
      stdout: `${JSON.stringify("mixed.txt")}:${formatVerifiedRow(3, "same")}`
        + `${JSON.stringify("mixed.txt")}:${formatVerifiedRow(4, "same")}`,
      stderr: "",
      exitCode: 0,
    });
    const contextual = await createHGrepTool("test", "").execute(["-B1", "-F", "same", "mixed.txt"], executionContext);
    expect(contextual.exitCode).toBe(0);
    expect(contextual.stdout).toContain(`${JSON.stringify("mixed.txt")}:${formatVerifiedRow(2, "skip")}`);
  });

  test("preserves BOM source rows and rejects UTF-16 search identities", async () => {
    const directory = await temporaryDirectory("reader-bom-");
    process.chdir(directory);
    const cat = createHCatTool("test", "");
    const grep = createHGrepTool("test", "");
    for (const source of ["\uFEFF", "\uFEFFneedle\nnext\n"]) {
      await writeFile("bom.txt", source);
      const result = await cat.execute(["bom.txt"], executionContext);
      expect(result.exitCode).toBe(0);
      expect(result.stdout).toStartWith(formatVerifiedRow(1, source.split("\n")[0]));
      const searched = await grep.execute(["-F", source === "\uFEFF" ? "\uFEFF" : "needle", "bom.txt"], executionContext);
      expect(searched.exitCode).toBe(0);
      expect(searched.stdout).toBe(`${JSON.stringify("bom.txt")}:${formatVerifiedRow(1, source.split("\n")[0])}`);
    }
    await writeFile("utf16.txt", Buffer.from("\uFEFFneedle\n", "utf16le"));
    const rejected = await grep.execute(["-F", "n", "utf16.txt"], executionContext);
    expect(rejected.exitCode).toBe(1);
    expect(rejected.stdout).toBeUndefined();
    expect(rejected.stderr).toContain("not UTF-8");
  });

  test("runs ripgrep and emits verified complete rows", async () => {
    const directory = await temporaryDirectory("hgrep-plugin-");
    process.chdir(directory);
    await writeFile("path with spaces.txt", "before\nneedle\nafter\n", "utf8");
    await writeFile("token-spellings.txt", "<|endoftext|> <|im_start|> <|fim_prefix|>\n", "utf8");

    const tool = createHGrepTool("description", "start: TEST");
    const result = await tool.execute(["-A1", "-F", "needle", "path with spaces.txt"], executionContext);
    expect(result).toEqual({
      stdout: `${JSON.stringify("path with spaces.txt")}:${formatVerifiedRow(2, "needle")}`
        + `${JSON.stringify("path with spaces.txt")}:${formatVerifiedRow(3, "after")}`,
      stderr: "",
      exitCode: 0,
    });

    await writeFile("carriage.txt", "needle\r", "utf8");
    const carriage = await tool.execute(["-F", "needle", "carriage.txt"], executionContext);
    expect(carriage).toEqual({
      stdout: `${JSON.stringify("carriage.txt")}:${formatVerifiedRow(1, "needle")}`,
      stderr: "",
      exitCode: 0,
    });

    const tokenSpellings = await tool.execute(["-F", "<|im_start|>", "token-spellings.txt"], executionContext);
    expect(tokenSpellings).toEqual({
      stdout: `${JSON.stringify("token-spellings.txt")}:${formatVerifiedRow(
        1,
        "<|endoftext|> <|im_start|> <|fim_prefix|>",
      )}`,
      stderr: "",
      exitCode: 0,
    });

    const missing = await tool.execute(["-F", "needle", "missing.txt"], executionContext);
    expect(missing.exitCode).toBe(1);
    expect(missing.stderr).toMatch(
      /^hgrep: (?!rg:)(?!.*missing\.txt)(?!.*IO error for operation on ).+\n$/u,
    );
  });

  test("retains whole admitted matches and fails when later matches exceed the token limit", async () => {
    const directory = await temporaryDirectory("hgrep-limit-");
    process.chdir(directory);
    const prefix = `${JSON.stringify("large.txt")}:`;
    const first = contentWithFormattedTokenCount(
      15_000,
      (content) => `${prefix}${formatVerifiedRow(1, content)}`,
    );
    await writeFile("large.txt", `${first}\nneedle second\nneedle third\n`, "utf8");

    const tool = createHGrepTool("description", "start: TEST");
    const result = await tool.execute(["-F", "needle", "large.txt"], executionContext);
    expect(result).toEqual({
      stdout: `${prefix}${formatVerifiedRow(2, "needle second")}`
        + `${prefix}${formatVerifiedRow(3, "needle third")}`,
      stderr: "",
      exitCode: 0,
    });

    await writeFile("large.txt", `needle ${first}\nneedle second\nneedle third\n`, "utf8");
    const limited = await tool.execute(["-F", "needle", "large.txt"], executionContext);
    expect(limited.exitCode).toBe(1);
    expect(limited.stderr).toStartWith("hgrep: output incomplete: 4000-token limit reached\n");
    await rm("large.txt");
    expect(limited.omittedOutput).toEqual({stdout:
      `${prefix}${formatVerifiedRow(1, `needle ${first}`)}`
      + `${prefix}${formatVerifiedRow(2, "needle second")}`
      + `${prefix}${formatVerifiedRow(3, "needle third")}`, stderr: "", stdoutKind: "rows"});
    expect(limited.stdout).toBe("");
    expect(limited.stdout).not.toContain("needle second");
    expect(limited.stdout).not.toContain("needle third");
  });

  test("bounds source events separately from JSON wire bytes", async () => {
    const directory = await temporaryDirectory("hgrep-source-bound-");
    process.chdir(directory);
    await writeFile("large.txt", `needle\nneedle ${"x".repeat(2_000_000)}\n`);
    const result = await createHGrepTool("test", "").execute(["--preview-bytes", "32", "-F", "needle", "large.txt"], executionContext);
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toBe("hgrep: output incomplete: rg source event exceeds the 1984000-byte inspection bound\n");
    expect(JSON.parse(result.stdout!)).toMatchObject({row: `1:${hashLine("needle")}`, preview: "needle"});
  });

  test("accepts GNU grep and ripgrep search options while rejecting incompatible modes", async () => {
    const directory = await temporaryDirectory("hgrep-plugin-");
    process.chdir(directory);
    await writeFile("file.txt", "needle\n", "utf8");

    const tool = createHGrepTool("description", "start: TEST");
    const gnuGrep = await tool.execute(["-R", "-n", "-F", "needle", "file.txt"], executionContext);
    const ripgrep = await tool.execute(["--glob", "*.txt", "-F", "needle", "file.txt"], executionContext);
    expect(gnuGrep).toEqual(ripgrep);
    expect(gnuGrep.stderr).toBe("");

    const ignored = await tool.execute(["--color", "always", "-F", "needle", "file.txt"], executionContext);
    expect(ignored.exitCode).toBe(0);
    expect(ignored.stderr).toContain("ignoring ripgrep options --color");

    const rejected = await tool.execute(["--multiline", "needle", "file.txt"], executionContext);
    expect(rejected.exitCode).toBe(1);
    expect(rejected.stderr).toContain("--multiline is incompatible");
  });
});

describe("hsymbol built-in plugin", () => {
  test("keeps its private contract behavioral", () => {
    const description = plugin.tools[2].specification.description.replace(/\s+/g, " ");
    expect(description).toContain("hsymbol [--max-tokens N] [--workspace ROOT] (def|refs) PATH (LINE|LINE:HASH) SYMBOL [N]");
    expect(description).toContain('"PATH":LINE:HASH TEXT');
    expect(description).toContain("ambiguous selectors");
    for (const persistent of ["rename", "audit", "before editing", "functions.hpatch"]) {
      expect(description).not.toContain(persistent);
    }
  });

  test("keeps a BOM in Go resolver byte offsets and verified first rows", async () => {
    const directory = await temporaryDirectory("hsymbol-bom-");
    process.chdir(directory);
    const source = "\uFEFFpackage p\nfunc Pick() {\n  println(1)\n}\n";
    const target = path.join(directory, "sample.go");
    await writeFile(target, source);
    const fake = await installFakeGopls();
    await fake.respond(`${target}:2:6-10\n`);
    const result = await createHSymbolTool("test", "").execute(["refs", "sample.go", "2", "Pick"], executionContext);
    expect(result).toMatchObject({exitCode: 0});
    expect(await readFile(fake.callsPath, "utf8")).toContain(`:#${Buffer.byteLength(source.slice(0, source.indexOf("Pick")))}`);
    await fake.respond(definitionJSON(target, source, source.indexOf("Pick"), "Pick"));
    const definition = await createHSymbolTool("test", "").execute(["def", "sample.go", "2", "Pick"], executionContext);
    expect(definition.exitCode).toBe(0);
    expect(definition.stdout).toBe([2, 3, 4].map((line) =>
      `${JSON.stringify("sample.go")}:${formatVerifiedRow(line, source.split("\n")[line - 1])}`).join(""));
    const goInspection = await createInspectFileTool("test", "").execute(["sample.go"], executionContext);
    expect(JSON.parse(goInspection.stdout!).data).toMatchObject({
      parse_complete: true,
      outline: [{kind: "function", name: "Pick", line: `2:${hashLine("func Pick() {")}`, line_end: `4:${hashLine("}")}`}],
    });
    await writeFile("sample.ts", "\uFEFFfunction pick() {}\n");
    const inspected = await createInspectFileTool("test", "").execute(["sample.ts"], executionContext);
    expect(inspected.exitCode).toBe(0);
    expect(inspected.stdout).toContain(`1:${hashLine("\uFEFFfunction pick() {}")}`);
  });

  test("parses BOM Markdown frontmatter and JSON with original row identities", async () => {
    const directory = await temporaryDirectory("inspect-bom-");
    process.chdir(directory);
    const tool = createInspectFileTool("test", "");
    await writeFile("frontmatter.md", "\uFEFF---\ntitle: Example\n---\n# Heading\n");
    const markdown = await tool.execute(["frontmatter.md"], executionContext);
    expect(JSON.parse(markdown.stdout!).data).toMatchObject({
      parse_complete: true,
      outline: [
        {kind: "frontmatter", name: "title", line: `2:${hashLine("title: Example")}`},
        {kind: "heading", line: `4:${hashLine("# Heading")}`},
      ],
    });
    for (const [file, source] of [
      ["source.json", "\uFEFF{\"name\": \"value\"}\n"],
      ["source.py", "\uFEFFdef pick():\n    return 1\n"],
      ["source.md", "\uFEFF# Heading\n"],
    ]) {
      await writeFile(file, source);
      const result = await tool.execute([file], executionContext);
      const data = JSON.parse(result.stdout!).data;
      expect(data.parse_complete).toBe(true);
      expect(data.outline[0].line).toBe(`1:${hashLine(source.split("\n")[0])}`);
    }
  });

  test("queries a current line in an explicit workspace without a verified read", async () => {
    const directory = await temporaryDirectory("hsymbol-current-");
    const caller = await temporaryDirectory("hsymbol-caller-");
    const source = "package p\nfunc Pick() {}\nfunc Use() { Pick() }\n";
    await writeFile(path.join(directory, "sample.go"), source);
    process.chdir(caller);
    const fake = await installFakeGopls();
    const tool = createHSymbolTool("test", "");
    await fake.respond(`${path.join(directory, "sample.go")}:3:14-18\n`);
    const result = await tool.execute(["--workspace", directory, "refs", "sample.go", "3", "Pick"], executionContext);
    expect(result.exitCode).toBe(0);
    expect(result.stdout).toBe(`${JSON.stringify(path.join(directory, "sample.go"))}:${formatVerifiedRow(3, "func Use() { Pick() }")}`);
    expect(result.stderr).toBe(`hsymbol: input ${JSON.stringify(path.join(directory, "sample.go"))}:3:${hashLine("func Use() { Pick() }")} (current snapshot)\n`);
    expect(await readFile(path.join(path.dirname(fake.callsPath), "cwd"), "utf8")).toBe(`${directory}\n`);
    expect(process.cwd()).toBe(caller);
    expect(await tool.parse(`--workspace "${directory}" def sample.go 3 Pick`, {})).toEqual([
      "--workspace", directory, "def", "sample.go", "3", "Pick",
    ]);
    const outside = await tool.execute(["--workspace", directory, "refs", path.join(caller, "sample.go"), "3", "Pick"], executionContext);
    expect(outside.exitCode).toBe(1);
    expect(outside.stderr).toContain("outside the workspace");
    const notDirectory = await tool.execute(["--workspace", path.join(directory, "sample.go"), "refs", ".", "3", "Pick"], executionContext);
    expect(notDirectory.stderr).toContain("workspace is not a directory");
    await fake.mutateBeforeResponse(path.join(directory, "sample.go"), source.replace("Pick() }", "Pick(); Pick() }"));
    const changed = await tool.execute(["--workspace", directory, "refs", "sample.go", "3", "Pick"], executionContext);
    expect(changed.exitCode).toBe(1);
    expect(changed.stdout).toBeUndefined();
    expect(changed.stderr).toContain("input changed during query");
  });

  test("classifies selected source failures without starting a resolver", async () => {
    const directory = await temporaryDirectory("hsymbol-source-failures-");
    const outside = await temporaryDirectory("hsymbol-outside-");
    process.chdir(directory);
    await mkdir("directory.go");
    await writeFile("invalid.go", Uint8Array.from([0xff]));
    await writeFile("unsupported.bin", "content");
    await writeFile(path.join(outside, "source.go"), "package p\n");
    await symlink(path.join(outside, "source.go"), "outside.go");
    const fake = await installFakeGopls();
    const tool = createHSymbolTool("test", "");
    for (const [source, diagnostic] of [
      ["missing.go", "path does not exist"],
      ["directory.go", "path is not a regular file"],
      ["invalid.go", "path is not UTF-8"],
      ["unsupported.bin", "path has an unsupported hsymbol source format"],
      [path.join(outside, "source.go"), "path is outside the workspace"],
      ["outside.go", "path resolves outside the workspace"],
    ]) {
      const result = await tool.execute(["refs", source, "1", "target"], executionContext);
      expect(result).toEqual({stderr: `hsymbol: ${diagnostic}\n`, exitCode: 1, failureClass: "invalid_source"});
    }
    expect(await readFile(fake.callsPath, "utf8")).toBe("");
  });

  test("plain-line selection retains exact tokens and ambiguity checks before resolver startup", async () => {
    const directory = await temporaryDirectory("hsymbol-plain-validation-");
    process.chdir(directory);
    await writeFile("sample.go", 'package p\nfunc Use() { target := 1; _ = target; _ = "target" }\n');
    const fake = await installFakeGopls();
    const tool = createHSymbolTool("test", "");
    for (const args of [
      ["refs", "sample.go", "2", "target"],
      ["refs", "sample.go", "2", "targ"],
      ["refs", "sample.go", "20", "target"],
      ["refs", "sample.go", "02", "target"],
      ["refs", "sample.go", "2", "target", "3"],
    ]) {
      const result = await tool.execute(args, executionContext);
      expect(result.exitCode).toBe(1);
      expect(result.stdout).toBeUndefined();
    }
    expect(await readFile(fake.callsPath, "utf8")).toBe("");
    const selected = await tool.execute(["refs", "sample.go", "2", "target", "2"], executionContext);
    expect(selected.exitCode).toBe(0);
    expect(selected.stderr).toContain("(current snapshot)");
  });

  test("plain-line TypeScript lookup uses the explicit resolver workspace", async () => {
    const directory = await temporaryDirectory("hsymbol-current-ts-");
    const caller = await temporaryDirectory("hsymbol-current-ts-caller-");
    await writeFile(path.join(directory, "tsconfig.json"), '{"include":["*.ts"]}');
    await writeFile(path.join(directory, "sample.ts"), "export const target = 42;\nconsole.log(target);\n");
    process.chdir(caller);
    process.env.PATH = `${pluginBin}${path.delimiter}${originalPath ?? ""}`;
    const result = await createHSymbolTool("test", "").execute([
      "--workspace", directory, "def", "sample.ts", "2", "target",
    ], executionContext);
    expect(result.exitCode).toBe(0);
    expect(result.stdout).toContain(`${JSON.stringify(path.join(directory, "sample.ts"))}:${formatVerifiedRow(1, "export const target = 42;")}`);
    expect(result.stderr).toContain(`${JSON.stringify(path.join(directory, "sample.ts"))}:2:${hashLine("console.log(target);")} (current snapshot)`);
  }, 30_000);

  test.skipIf(Bun.which("gopls") === null)("Go field references include differently named internal and external tests", async () => {
    const directory = await temporaryDirectory("hsymbol-go-callers-");
    process.chdir(directory);
    await writeFile("go.mod", "module example.com/callers\n\ngo 1.26\n");
    await writeFile("state.go", "package callers\ntype State struct { Removed int }\n");
    await writeFile("capture_order_test.go", "package callers\nfunc capture(s State) int { return s.Removed }\n");
    await writeFile("consumer_test.go", 'package callers_test\nimport "example.com/callers"\nfunc use(s callers.State) int { return s.Removed }\n');
    const result = await createHSymbolTool("description", "start: TEST").execute(
      ["refs", "state.go", "2", "Removed"], executionContext,
    );
    expect(result.exitCode).toBe(0);
    expect(result.stderr ?? "").not.toContain("skipped");
    expect(result.stdout).toContain('"state.go":2:');
    expect(result.stdout).toContain('"capture_order_test.go":2:');
    expect(result.stdout).toContain('"consumer_test.go":3:');
  }, 30_000);

  test("validates the verified Go token selector before starting gopls", async () => {
    const directory = await temporaryDirectory("hsymbol-plugin-");
    process.chdir(directory);
    const source = [
      "package sample",
      "func Use() {",
      '  名稱 := 1; _ = 名稱; _ = "名稱" // 名稱',
      "}",
      "",
    ].join("\n");
    await writeFile("path with spaces.go", source, "utf8");
    const fake = await installFakeGopls();
    const tool = createHSymbolTool("description", "start: TEST");
    const line = new LineMap(source).logicalLine(3)?.text;
    if (line === undefined) {
      throw new Error("selector fixture line is missing");
    }
    const reference = `3:${hashLine(line)}`;

    for (const argv of [
      ["refs", "path with spaces.go", "3:ffff", "名稱"],
      ["refs", "path with spaces.go", reference, "名稱"],
      ["refs", "path with spaces.go", reference, "名稱", "3"],
      ["refs", "path with spaces.go", reference, "名稱", "01"],
      ["refs", "path with spaces.go", reference, "func"],
      ["refs", "path with spaces.go", reference, "Name"],
    ]) {
      const result = await tool.execute(argv, executionContext);
      expect(result.exitCode).toBe(1);
      expect(result.stdout).toBeUndefined();
    }
    expect(await readFile(fake.callsPath, "utf8")).toBe("");

    const selected = await tool.execute(
      ["refs", "path with spaces.go", reference, "名稱", "2"],
      executionContext,
    );
    expect(selected).toEqual({
      stdout: "",
      exitCode: 0,
      terminationReason: "resolver_cleanup",
    });
    const selectedOffset = source.indexOf("名稱", source.indexOf("名稱") + 1);
    const calls = await readFile(fake.callsPath, "utf8");
    expect(calls).toContain("references -d");
    expect(calls).toContain(`:#${Buffer.byteLength(source.slice(0, selectedOffset), "utf8")}`);
    expect(calls.trim().split("\n")).toHaveLength(1);
  });

  test("rejects JavaScript labels before starting TypeScript", async () => {
    const directory = await temporaryDirectory("hsymbol-label-");
    process.chdir(directory);
    const source = "target: while (false) break target;\n";
    await writeFile("input.js", source);
    process.env.PATH = await temporaryDirectory("hsymbol-label-empty-path-");
    const result = await createHSymbolTool("description", "start: TEST").execute(
      ["refs", "input.js", `1:${hashLine(source.trimEnd())}`, "target"],
      executionContext,
    );
    expect(result).toEqual({
      stderr: "hsymbol: target is not a symbol token on the selected line\n",
      exitCode: 1,
      failureClass: "resolver_error",
    });
  });

  test("accepts canonical in-workspace paths and rejects workspace escapes before gopls", async () => {
    const directory = await temporaryDirectory("hsymbol-path-");
    process.chdir(directory);
    const source = "package sample\nfunc Use() { Target() }\n";
    const inputPath = path.join(directory, "input.go");
    await writeFile(inputPath, source, "utf8");
    const inputLine = new LineMap(source).logicalLine(2)?.text;
    if (inputLine === undefined) {
      throw new Error("path input line is missing");
    }
    const reference = `2:${hashLine(inputLine)}`;
    const fake = await installFakeGopls();
    const tool = createHSymbolTool("description", "start: TEST");
    expect(await tool.execute(["refs", inputPath, reference, "Target"], executionContext)).toEqual({
      stdout: "",
      exitCode: 0,
      terminationReason: "resolver_cleanup",
    });

    const externalDirectory = await temporaryDirectory("hsymbol-external-");
    const externalPath = path.join(externalDirectory, "external.go");
    await writeFile(externalPath, source, "utf8");
    await symlink(externalPath, "escaped.go");
    for (const escapedPath of [externalPath, "escaped.go"]) {
      const result = await tool.execute(["refs", escapedPath, reference, "Target"], executionContext);
      expect(result.exitCode).toBe(1);
      expect(result.stderr).toContain("outside the workspace");
      expect(result.stdout).toBeUndefined();
    }
    expect((await readFile(fake.callsPath, "utf8")).trim().split("\n")).toHaveLength(1);
  });

  test("expands only exact top-level and direct-method outline declarations", async () => {
    const directory = await temporaryDirectory("hsymbol-def-");
    process.chdir(directory);
    const source = [
      "package sample",
      "const (",
      "  A = 1",
      ")",
      "var B = struct {",
      "  X int",
      "}{}",
      "type C struct {",
      "  Y int",
      "}",
      "func D() {",
      "  _ = A",
      "}",
      "type R struct{}",
      "func (R) M() {",
      "  D()",
      "}",
      "func Use(r R) { _ = A; _ = B; _ = C{}; D(); r.M() }",
      "",
    ].join("\n");
    const filePath = path.join(directory, "declarations.go");
    await writeFile(filePath, source, "utf8");
    const fake = await installFakeGopls();
    const tool = createHSymbolTool("description", "start: TEST");
    const useLine = new LineMap(source).logicalLine(18)?.text;
    if (useLine === undefined) {
      throw new Error("definition fixture line is missing");
    }
    const reference = `18:${hashLine(useLine)}`;
    const cases = [
      {name: "A", from: 3, to: 3},
      {name: "B", from: 5, to: 7},
      {name: "C", from: 8, to: 10},
      {name: "D", from: 11, to: 13},
      {name: "M", from: 15, to: 17},
    ];
    const lines = new LineMap(source);

    for (const testCase of cases) {
      const nameOffset = source.indexOf(testCase.name, lines.logicalLine(testCase.from)?.from);
      const response = definitionJSON(filePath, source, nameOffset, testCase.name);
      await fake.respond(response);
      const result = await tool.execute(
        ["def", "declarations.go", reference, testCase.name],
        executionContext,
      );
      let expected = "";
      for (let lineNumber = testCase.from; lineNumber <= testCase.to; lineNumber += 1) {
        const text = lines.logicalLine(lineNumber)?.text;
        if (text === undefined) {
          throw new Error(`missing fixture line ${lineNumber}`);
        }
        expected += `${JSON.stringify("declarations.go")}:${formatVerifiedRow(lineNumber, text)}`;
      }
      expect(result).toEqual({
        stdout: expected,
        exitCode: 0,
        terminationReason: "resolver_cleanup",
      });
    }
  });

  test("falls back to the definition line for unowned or uncertain declarations", async () => {
    const directory = await temporaryDirectory("hsymbol-def-");
    process.chdir(directory);
    const source = [
      "package sample",
      "type R struct {",
      "  Field int",
      "}",
      "func Use(r R) { _ = r.Field }",
      "",
    ].join("\n");
    const filePath = path.join(directory, "field.go");
    await writeFile(filePath, source, "utf8");
    const fieldOffset = source.indexOf("Field");
    expect(goDeclarationRange(
      source,
      Buffer.byteLength(source.slice(0, fieldOffset), "utf8"),
      Buffer.byteLength(source.slice(0, fieldOffset + "Field".length), "utf8"),
    )).toBeNull();
    const broken = source + "func Broken( {\n";
    const useOffset = broken.indexOf("Use");
    expect(goDeclarationRange(
      broken,
      Buffer.byteLength(broken.slice(0, useOffset), "utf8"),
      Buffer.byteLength(broken.slice(0, useOffset + "Use".length), "utf8"),
    )).toBeNull();

    const fake = await installFakeGopls();
    const response = definitionJSON(filePath, source, fieldOffset, "Field");
    await fake.respond(response);
    const useLine = new LineMap(source).logicalLine(5)?.text;
    if (useLine === undefined) {
      throw new Error("field use line is missing");
    }
    const result = await createHSymbolTool("description", "start: TEST").execute(
      ["def", "field.go", `5:${hashLine(useLine)}`, "Field"],
      executionContext,
    );
    expect(result).toEqual({
      stdout: `${JSON.stringify("field.go")}:${formatVerifiedRow(3, "  Field int")}`,
      exitCode: 0,
      terminationReason: "resolver_cleanup",
    });
  });

  test("deduplicates canonical reference rows and reports skipped locations", async () => {
    const directory = await temporaryDirectory("hsymbol-refs-");
    process.chdir(directory);
    const inputSource = "package sample\nfunc Use() { Target() }\n";
    const resultSource = "package sample\nfunc Target() {}\n";
    await Promise.all([
      writeFile("input.go", inputSource, "utf8"),
      writeFile("result.go", resultSource, "utf8"),
      writeFile("not-go.txt", "Target\n", "utf8"),
      writeFile("invalid.go", Uint8Array.from([0xff])),
      mkdir("folder.go"),
    ]);
    await symlink("result.go", "alias.go");
    const externalDirectory = await temporaryDirectory("hsymbol-external-");
    const externalPath = path.join(externalDirectory, "external.go");
    await writeFile(externalPath, "package external\nfunc Target() {}\n", "utf8");
    const resultPath = path.join(directory, "result.go");
    const inputPath = path.join(directory, "input.go");
    const rows = [
      `${resultPath}:2:6-12`,
      `${path.join(directory, "alias.go")}:2:6-12`,
      `${inputPath}:2:14-20`,
      `${externalPath}:2:6-12`,
      `${path.join(directory, "not-go.txt")}:1:1-7`,
      `${path.join(directory, "folder.go")}:1:1-7`,
      `${path.join(directory, "invalid.go")}:1:1-7`,
      `${path.join(directory, "missing.go")}:1:1-7`,
    ];
    const goplsStdout = `${rows.join("\n")}\n`;
    const fake = await installFakeGopls();
    await fake.respond(goplsStdout, "gopls note\n");
    const inputLine = new LineMap(inputSource).logicalLine(2)?.text;
    if (inputLine === undefined) {
      throw new Error("reference input line is missing");
    }
    const result = await createHSymbolTool("description", "start: TEST").execute(
      ["refs", "input.go", `2:${hashLine(inputLine)}`, "Target"],
      executionContext,
    );
    expect(result).toEqual({
      stdout: `${JSON.stringify("result.go")}:${formatVerifiedRow(2, "func Target() {}")}`
        + `${JSON.stringify("input.go")}:${formatVerifiedRow(2, "func Use() { Target() }")}`,
      stderr: "gopls note\nhsymbol: skipped 1 location outside workspace, 1 location not Go, 1 location not regular, 1 location not UTF-8, 1 location unavailable\n",
      exitCode: 0,
      terminationReason: "resolver_cleanup",
    });
    expect(await readFile(fake.callsPath, "utf8")).toContain("references -d");
  });

  test("fails an uneditable definition and a missing gopls without useful stdout", async () => {
    const directory = await temporaryDirectory("hsymbol-failure-");
    process.chdir(directory);
    const source = "package sample\nfunc Use() { Target() }\n";
    await writeFile("input.go", source, "utf8");
    const inputLine = new LineMap(source).logicalLine(2)?.text;
    if (inputLine === undefined) {
      throw new Error("failure input line is missing");
    }
    const reference = `2:${hashLine(inputLine)}`;
    const externalDirectory = await temporaryDirectory("hsymbol-external-");
    const externalSource = "package external\nfunc Target() {}\n";
    const externalPath = path.join(externalDirectory, "external.go");
    await writeFile(externalPath, externalSource, "utf8");
    const response = definitionJSON(externalPath, externalSource, externalSource.indexOf("Target"), "Target");
    const fake = await installFakeGopls();
    await fake.respond(response);
    const tool = createHSymbolTool("description", "start: TEST");
    const external = await tool.execute(["def", "input.go", reference, "Target"], executionContext);
    expect(external).toEqual({
      stderr: "hsymbol: skipped 1 location outside workspace\nhsymbol: definition has no editable workspace location\n",
      exitCode: 1,
      failureClass: "no_editable_location",
      terminationReason: "resolver_cleanup",
    });

    await fake.respond("", "query failed\n", 2);
    const failed = await tool.execute(["refs", "input.go", reference, "Target"], executionContext);
    expect(failed).toEqual({stderr: "hsymbol: query failed\n", exitCode: 1, failureClass: "resolver_error", terminationReason: "resolver_cleanup"});

    const emptyPath = await temporaryDirectory("hsymbol-empty-path-");
    process.env.PATH = emptyPath;
    const unavailable = await tool.execute(["refs", "input.go", reference, "Target"], executionContext);
    expect(unavailable).toEqual({stderr: "hsymbol: gopls is unavailable; expose gopls on the executor PATH\n", exitCode: 1, failureClass: "dependency_unavailable", terminationReason: "resolver_cleanup"});
  });

  test("fails without query output when the selected input changes during gopls", async () => {
    const directory = await temporaryDirectory("hsymbol-changing-input-");
    process.chdir(directory);
    const source = "package sample\nfunc Use() { Target() }\n";
    const inputPath = path.join(directory, "input.go");
    await writeFile(inputPath, source, "utf8");
    const inputLine = new LineMap(source).logicalLine(2)?.text;
    if (inputLine === undefined) {
      throw new Error("changing input line is missing");
    }
    const fake = await installFakeGopls();
    await fake.respond(`${inputPath}:2:14-20\n`);
    await fake.mutateBeforeResponse(inputPath, `package sample\n\n${inputLine}\n`);

    const result = await createHSymbolTool("description", "start: TEST").execute(
      ["refs", "input.go", `2:${hashLine(inputLine)}`, "Target"],
      executionContext,
    );
    expect(result).toEqual({stderr: "hsymbol: input changed during query\n", exitCode: 1, failureClass: "resolver_error", terminationReason: "resolver_cleanup"});
  });

  test("applies the shared whole-row token admission to references", async () => {
    const directory = await temporaryDirectory("hsymbol-limit-");
    process.chdir(directory);
    const inputSource = "package sample\nfunc Use() { Target() }\n";
    await writeFile("input.go", inputSource, "utf8");
    const resultPath = path.join(directory, "large.go");
    const prefix = `${JSON.stringify("large.go")}:`;
    const first = contentWithFormattedTokenCount(
      15_000,
      (content) => `${prefix}${formatVerifiedRow(1, content)}`,
    );
    await writeFile(resultPath, `${first}\nsecond\nthird\n`, "utf8");
    const externalDirectory = await temporaryDirectory("hsymbol-limit-external-");
    const externalPath = path.join(externalDirectory, "external.go");
    await writeFile(externalPath, "package external\n", "utf8");
    const goplsStdout = [
      ...[1, 2, 3].map((line) => `${resultPath}:${line}:1-2`),
      `${externalPath}:1:1-2`,
    ].join("\n") + "\n";
    const fake = await installFakeGopls();
    await fake.respond(goplsStdout);
    const inputLine = new LineMap(inputSource).logicalLine(2)?.text;
    if (inputLine === undefined) {
      throw new Error("limit input line is missing");
    }
    const result = await createHSymbolTool("description", "start: TEST").execute(
      ["refs", "input.go", `2:${hashLine(inputLine)}`, "Target"],
      executionContext,
    );
    expect(result).toEqual({
      omittedOutput: {stdout: `${prefix}${formatVerifiedRow(1, first)}${prefix}${formatVerifiedRow(2, "second")}${prefix}${formatVerifiedRow(3, "third")}`, stderr: "", stdoutKind: "rows"},
      stdout: "",
      stderr: expect.stringContaining("hsymbol: skipped 1 location outside workspace\n"
        + "hsymbol: output incomplete: 4000-token limit reached\n"),
      exitCode: 1,
      failureClass: "output_limit",
      terminationReason: "resolver_cleanup",
    });
  });

  test("retained references survive source removal without another resolver call", async () => {
    const directory = await temporaryDirectory("hsymbol-retained-");
    process.chdir(directory);
    const source = "package sample\nfunc Use() { Target() }\n";
    await writeFile("input.go", source);
    const huge = "var Target = 0 // " + "word ".repeat(16000);
    await writeFile("uses.go", huge + "\nfunc Other() { Target() }\n");
    const fake = await installFakeGopls();
    await fake.respond(`${path.join(directory, "uses.go")}:1:5-11\n${path.join(directory, "uses.go")}:2:16-22\n`);
    const result = await createHSymbolTool("description", "start: TEST").execute(
      ["refs", "input.go", "2", "Target"], executionContext,
    );
    expect(result.exitCode).toBe(1);
    expect(result.stdout).toBe("");
    expect(result.stderr).toContain("output incomplete");
    await rm("uses.go");
    expect(result.omittedOutput?.stdout).toBe(
      `"uses.go":${formatVerifiedRow(1, huge)}"uses.go":${formatVerifiedRow(2, "func Other() { Target() }")}`,
    );
    expect((await readFile(fake.callsPath, "utf8")).trim().split("\n")).toHaveLength(1);
  });

  test("resolves TypeScript 7 and Python definitions through their LSP servers", async () => {
    const directory = await temporaryDirectory("hsymbol-lsp-");
    process.chdir(directory);
    process.env.PATH = `${pluginBin}${path.delimiter}${originalPath ?? ""}`;
    // TypeScript supports a version probe; Pyright is validated by the LSP query below.
    const tscCheck = spawnSync("tsc", ["--version"], {encoding: "utf8"});
    if (tscCheck.status !== 0) {
      throw new Error(`tsc is not available: ${tscCheck.error?.message ?? tscCheck.stderr}`);
    }
    const typescriptTarget = [
      "export function target(value: number) {",
      "  return value + 1;",
      "}",
      "",
    ].join("\n");
    const typescriptInput = [
      'import {target} from "./target";',
      'export const emoji = "😀"; export const answer = target(1);',
      "",
    ].join("\n");
    const pythonTarget = [
      "def target(value: int) -> int:",
      "    return value + 1",
      "",
    ].join("\n");
    const pythonInput = [
      "from target import target",
      "answer = target(1)",
      "",
    ].join("\n");
    const pythonStub = "def stub_target(value: int) -> int: ...\n";
    const ambientTarget = [
      "export declare function ambientTarget(",
      "  value: number,",
      "): number;",
      "",
    ].join("\n");
    const ambientInput = [
      'import {ambientTarget} from "./ambient";',
      "export const ambientAnswer = ambientTarget(1);",
      "",
    ].join("\n");
    await Promise.all([
      writeFile("tsconfig.json", JSON.stringify({compilerOptions: {allowJs: true, jsx: "react-jsx"}})),
      writeFile("target.ts", typescriptTarget),
      writeFile("input.ts", typescriptInput),
      writeFile("target.py", pythonTarget),
      writeFile("input.py", pythonInput),
      writeFile("sample.pyi", pythonStub),
      writeFile("ambient.d.ts", ambientTarget),
      writeFile("ambient_input.ts", ambientInput),
    ]);
    const tool = createHSymbolTool("description", "start: TEST");
    const typescriptLine = new LineMap(typescriptInput).logicalLine(2)?.text;
    const pythonLine = new LineMap(pythonInput).logicalLine(2)?.text;
    if (typescriptLine === undefined || pythonLine === undefined) {
      throw new Error("LSP fixture line is missing");
    }

    const typescript = await tool.execute(
      ["def", "input.ts", `2:${hashLine(typescriptLine)}`, "target"],
      executionContext,
    );
    expect(typescript).toMatchObject({exitCode: 0});
    expect(typescript.stdout).toContain(`${JSON.stringify("target.ts")}:${formatVerifiedRow(1, "export function target(value: number) {")}`);
    expect(typescript.stdout).toContain(`${JSON.stringify("target.ts")}:${formatVerifiedRow(3, "}")}`);

    const ambient = await tool.execute(
      ["def", "ambient_input.ts", `2:${hashLine(ambientInput.split("\n")[1])}`, "ambientTarget"],
      executionContext,
    );
    expect(ambient).toMatchObject({exitCode: 0});
    for (const [line, text] of ambientTarget.trimEnd().split("\n").entries()) {
      expect(ambient.stdout).toContain(`${JSON.stringify("ambient.d.ts")}:${formatVerifiedRow(line + 1, text)}`);
    }

    const python = await tool.execute(
      ["def", "input.py", `2:${hashLine(pythonLine)}`, "target"],
      executionContext,
    );
    expect(python).toMatchObject({exitCode: 0});
    expect(python.stdout).toContain(`${JSON.stringify("target.py")}:${formatVerifiedRow(1, "def target(value: int) -> int:")}`);
    expect(python.stdout).toContain(`${JSON.stringify("target.py")}:${formatVerifiedRow(2, "    return value + 1")}`);
    const stub = await tool.execute(
      ["refs", "sample.pyi", `1:${hashLine(pythonStub.trimEnd())}`, "stub_target"],
      executionContext,
    );
    expect(stub).toMatchObject({exitCode: 0});

    process.env.PATH = await temporaryDirectory("hsymbol-empty-lsp-path-");
    expect(await tool.execute(
      ["refs", "input.ts", `2:${hashLine(typescriptLine)}`, "target"],
      executionContext,
    )).toEqual({stderr: "hsymbol: tsc is unavailable; expose TypeScript 7 tsc with --lsp support on the executor PATH\n", exitCode: 1, failureClass: "dependency_unavailable", terminationReason: "resolver_cleanup"});
    expect(await tool.execute(
      ["refs", "input.py", `2:${hashLine(pythonLine)}`, "target"],
      executionContext,
    )).toEqual({stderr: "hsymbol: pyright-langserver is unavailable; expose pyright-langserver on the executor PATH\n", exitCode: 1, failureClass: "dependency_unavailable", terminationReason: "resolver_cleanup"});
  });

  test("reaps a language server that ignores shutdown", async () => {
    const directory = await temporaryDirectory("hsymbol-lsp-shutdown-");
    const server = path.join(directory, "server.mjs");
    await writeFile(server, String.raw`
let input = Buffer.alloc(0);
function respond(id, result) {
  const body = JSON.stringify({jsonrpc: "2.0", id, result});
  process.stdout.write("Content-Length: " + Buffer.byteLength(body) + "\r\n\r\n" + body);
}
function receive(message) {
  if (message.method === "initialize") {
    respond(message.id, {capabilities: {positionEncoding: "utf-16"}});
  } else if (message.method === "textDocument/definition") {
    respond(message.id, []);
  }
}
process.stdin.on("data", (chunk) => {
  input = Buffer.concat([input, chunk]);
  while (true) {
    const headerEnd = input.indexOf("\r\n\r\n");
    if (headerEnd < 0) break;
    const header = input.subarray(0, headerEnd).toString();
    const length = Number(/Content-Length: ([0-9]+)/iu.exec(header)?.[1]);
    if (input.length < headerEnd + 4 + length) break;
    const bodyStart = headerEnd + 4;
    receive(JSON.parse(input.subarray(bodyStart, bodyStart + length).toString()));
    input = input.subarray(bodyStart + length);
  }
});
`);
    const started = performance.now();
    const result = await runLSPQuery({
      command: process.execPath,
      args: [server],
      workspace: directory,
      path: path.join(directory, "input.ts"),
      languageID: "typescript",
      source: "const value = 1;\n",
      position: {line: 0, character: 6},
      mode: "def",
    });
    expect(result.locations).toEqual([]);
    expect(performance.now() - started).toBeLessThan(3_000);
  });

  test("accepts every stable TypeScript 7 source format", async () => {
    const directory = await temporaryDirectory("hsymbol-typescript-formats-");
    process.chdir(directory);
    process.env.PATH = `${pluginBin}${path.delimiter}${originalPath ?? ""}`;
    await writeFile("tsconfig.json", JSON.stringify({
      compilerOptions: {allowJs: true, checkJs: true, jsx: "react-jsx"},
      include: ["*"],
    }));
    const fixtures = [
      ["sample.ts", "export const target = 1; console.log(target);"],
      ["sample.tsx", "export const target = 1; const view = <div>{target}</div>;"],
      ["sample.d.ts", "export declare const target: number;"],
      ["sample.mts", "export const target = 1; console.log(target);"],
      ["sample.d.mts", "export declare const target: number;"],
      ["sample.cts", "export const target = 1; console.log(target);"],
      ["sample.d.cts", "export declare const target: number;"],
      ["sample.js", "export const target = 1; console.log(target);"],
      ["sample.jsx", "export const target = 1; const view = <div>{target}</div>;"],
      ["sample.mjs", "export const target = 1; console.log(target);"],
      ["sample.cjs", "const target = 1; module.exports = target;"],
      ["sample.json", '{"target": 1}'],
    ] as const;
    await Promise.all(fixtures.map(([name, source]) => writeFile(name, `${source}\n`)));
    const tool = createHSymbolTool("description", "start: TEST");

    for (const [name, source] of fixtures) {
      const result = await tool.execute(
        ["refs", name, `1:${hashLine(source)}`, "target", "1"],
        executionContext,
      );
      expect(result).toMatchObject({exitCode: 0});
      // The real server may log shutdown timing; backend stderr is preserved.
      expect(result.stderr ?? "").not.toContain("hsymbol:");
    }
  }, 30_000);
});

describe("inspect_file built-in plugin", () => {

  test("embeds its outline-only shape schema", async () => {
    const marker = "Result shape schema:\n";
    const schema = JSON.parse(inspectFileDescription.slice(
      inspectFileDescription.indexOf(marker) + marker.length,
    ));
    expect(schema.success.data.outline).toBe("outline_entry[]");
    expect(schema).not.toHaveProperty("selected_entry_source");
    expect(JSON.stringify(schema.outline_entry)).not.toContain("source");
    for (const persistent of ["hcat", "before editing", "Reason carefully"]) {
      expect(inspectFileDescription).not.toContain(persistent);
    }

    const directory = await temporaryDirectory("inspect-file-");
    process.chdir(directory);
    await writeFile("sample.go", [
      "package p", "import alias \"example.com/a\"", "import `raw/path`",
      "import rawalias `aliased/raw`", "import (_ `grouped/raw`)",
      "const (A, B = 1, 2)", "var C int", "type T[P any] struct { Secret string }",
      "func F() { local := \"body-secret\" }", "func (receiver *T[P]) M() {}", "",
    ].join("\n"));
    await writeFile("sample.md", [
      "---", "title: do-not-return", "\"quoted key\": hidden-value",
      "summary: |", "  # secret scalar", "meta:", "  nested: excluded", "---",
      "# Main *source* #", "```", "## hidden", "```", "### Visible", "",
    ].join("\r\n"));
    await writeFile("sample.json", "{\"a/b\":{\"~key\":[true,null,123,\"never-return\"]}}");
    await writeFile("recovered.json", "{\"a\" \"x\"}");
    await writeFile("duplicate.md", "---\na: 1\na: 2\n---\n");

    const go = await inspect("sample.go");
    expect(go.result.data.outline.map((entry: Record<string, unknown>) =>
      [entry.kind, entry.name, entry.receiver],
    )).toEqual([
      ["import", "example.com/a", undefined], ["import", "raw/path", undefined],
      ["import", "aliased/raw", undefined], ["import", "grouped/raw", undefined],
      ["constant", "A", undefined], ["constant", "B", undefined],
      ["variable", "C", undefined], ["type", "T", undefined],
      ["function", "F", undefined], ["method", "M", "*T[P]"],
    ]);
    expect(JSON.stringify(go.result)).not.toMatch(/Secret|body-secret|local/u);

    const markdown = await inspect("sample.md");
    expect(markdown.result.data.outline.map((entry: Record<string, unknown>) =>
      [entry.kind, entry.name, entry.level],
    )).toEqual([
      ["frontmatter", "title", undefined], ["frontmatter", "quoted key", undefined],
      ["frontmatter", "summary", undefined], ["frontmatter", "meta", undefined],
      ["heading", "Main *source*", 1], ["heading", "Visible", 3],
    ]);
    expect(JSON.stringify(markdown.result)).not.toMatch(
      /do-not-return|hidden-value|secret scalar|nested|excluded|hidden/u,
    );
    const duplicate = await inspect("duplicate.md");
    expect(duplicate.result.data.parse_complete).toBe(false);
    expect(duplicate.result.data.outline.map((entry: Record<string, unknown>) => entry.name)).toEqual(["a", "a"]);

    const json = await inspect("sample.json");
    expect(json.result.data.outline.map((entry: Record<string, unknown>) =>
      [entry.pointer, entry.value_type],
    )).toEqual([
      ["", "object"], ["/a~1b", "object"], ["/a~1b/~0key", "array"],
      ["/a~1b/~0key/0", "boolean"], ["/a~1b/~0key/1", "null"],
      ["/a~1b/~0key/2", "number"], ["/a~1b/~0key/3", "string"],
    ]);
    expect(JSON.stringify(json.result)).not.toContain("never-return");
    const recovered = await inspect("recovered.json");
    expect(recovered.result.data.parse_complete).toBe(false);
    expect(recovered.result.data.outline.map((entry: Record<string, unknown>) => entry.pointer)).toEqual([""]);
  });
});

describe("inspect_file language projections", () => {
  test("decodes JavaScript side-effect imports using module string semantics", async () => {
    const directory = await temporaryDirectory("inspect-js-imports-");
    process.chdir(directory);
    const literals = [
      {raw: "'single'", name: "single"},
      {raw: '\"double\"', name: "double"},
      {raw: String.raw`'hex\x2d\u0061\u{1f600}'`, name: "hex-a😀"},
      {raw: String.raw`'it\'s'`, name: "it's"},
      {raw: "'continued\\\nmodule'", name: "continuedmodule"},
      {raw: "''", name: ""},
    ];
    for (const extension of ["js", "ts"]) {
      await writeFile(`imports.${extension}`, literals.map(({raw}) => `import ${raw};`).join("\n"));
      const result = await inspect(`imports.${extension}`);
      expect(result.result.ok).toBe(true);
      expect(result.result.data.parse_complete).toBe(true);
      expect(result.result.data.outline.map((entry: Record<string, unknown>) => [entry.kind, entry.name]))
        .toEqual(literals.map(({name}) => ["import", name]));
    }
  });

  test("hashes each outline boundary once per inspected snapshot", async () => {
    const directory = await temporaryDirectory("inspect-hash-cache-");
    process.chdir(directory);
    const source = JSON.stringify(Array.from({length: 10000}, (_, index) => index));
    await writeFile("dense.json", source);
    const logicalLine = spyOn(LineMap.prototype, "logicalLine");
    try {
      const first = await inspect("dense.json");
      expect(first.result.ok).toBe(true);
      expect(first.result.truncated).toBe(true);
      expect(logicalLine).toHaveBeenCalledTimes(1);
      const identity = `1:${hashLine(source)}`;
      for (const entry of first.result.data.outline) {
        expect(entry.line).toBe(identity);
        expect(entry.line_end).toBe(entry.line);
      }
      await writeFile("dense.json", "[true, false]\r\n");
      const second = await inspect("dense.json");
      expect(logicalLine).toHaveBeenCalledTimes(2);
      expect(second.result.data.outline).toHaveLength(3);
      for (const entry of second.result.data.outline) {
        expect(entry.line).toBe(`1:${hashLine("[true, false]")}`);
        expect(entry.line_end).toBe(entry.line);
      }
    } finally {
      logicalLine.mockRestore();
    }
  });

  test("keeps Go initializer declarations and generic parameters out of top-level outlines", async () => {
    const directory = await temporaryDirectory("inspect-go-scopes-");
    process.chdir(directory);
    const source = [
      "package sample",
      "var Exported = func() {",
      "  var localVar = 1",
      "  const localConst = 2",
      "  type localType int",
      "  _ = localVar; _ = localConst; _ = localType(0)",
      "}",
      "var (First, Second = 1, 2; Third = func() { var hidden = 3; _ = hidden })",
      "const (Alpha, Beta = 1, 2)",
      "type (Generic[P any] struct { Field P }; Alias = int)",
      "",
    ].join("\n");
    await writeFile("scope.go", source, "utf8");
    expect((await inspectOutline("scope.go")).map((entry) => [entry.kind, entry.name])).toEqual([
      ["variable", "Exported"], ["variable", "First"], ["variable", "Second"], ["variable", "Third"],
      ["constant", "Alpha"], ["constant", "Beta"], ["type", "Generic"], ["type", "Alias"],
    ]);
    for (const name of ["localVar", "localConst", "localType", "hidden", "P", "Field"]) {
      const offset = source.indexOf(name);
      expect(goDeclarationRange(source, Buffer.byteLength(source.slice(0, offset)),
        Buffer.byteLength(source.slice(0, offset + name.length)))).toBeNull();
    }
    const fake = await installFakeGopls();
    await fake.respond(definitionJSON(path.join(directory, "scope.go"), source, source.indexOf("localVar"), "localVar"));
    const useLine = source.split("\n")[5];
    const tool = createHSymbolTool("description", "start: TEST");
    const result = await tool.execute(["def", "scope.go", `6:${hashLine(useLine)}`, "localVar"], executionContext);
    expect(result).toEqual({ stdout: `${JSON.stringify("scope.go")}:${formatVerifiedRow(3, source.split("\n")[2])}`, exitCode: 0, terminationReason: "resolver_cleanup" });
  });


  test("normalizes JavaScript, TypeScript, and Python declarations", async () => {
    const directory = await temporaryDirectory("inspect-file-");
    process.chdir(directory);
    await writeFile("sample.js", [
      "import primary, {remote as local} from \"pkg\"; import \"side\";",
      "export const callable = () => 1, value = 2; let mutable = 3;",
      "function run() { const hidden = 1; }",
      "class Box { field = \"secret\"; method() {} #private() {} 1() {} \"quoted\"() {} [\"literal\"]() {} [name]() {} static async *gen() {} }",
    ].join("\n"));
    await writeFile("sample.ts", [
      "interface Shape { field: string }", "type Name = string;",
      "enum Choice { One }",
      "class Typed { method(): void {} #private() {} 1() {} \"quoted\"() {} [\"literal\"]() {} [name]() {} static async *gen(): void {} }",
    ].join("\n"));
    await writeFile("sample.py", [
      "import package.module, second as alias",
      "from source.module import member as local",
      "from pkg import (",
      "    first, # inline",
      "    second as second_alias,",
      ")",
      "value = 1", "@decorate", "def run():", "    nested = \"secret\"",
      "@decorate", "class Box:", "    field = 1",
      "    @decorate", "    def method(self):", "        pass", "",
    ].join("\n"));
    await writeFile("assignments.py", [
      "a = b = 1",
      "obj.attr = 2",
      "items[0] = 3",
      "annotated: Type = 4",
      "left, (middle, right) = source_value",
      "[first_item, second_item] = source_value",
      "",
    ].join("\n"));

    expect((await inspectOutline("sample.js")).map((entry) =>
      [entry.kind, entry.name, entry.receiver],
    )).toEqual([
      ["import", "primary", undefined], ["import", "local", undefined],
      ["import", "side", undefined], ["constant", "callable", undefined],
      ["constant", "value", undefined], ["variable", "mutable", undefined],
      ["function", "run", undefined], ["class", "Box", undefined],
      ["method", "method", "Box"], ["method", "#private", "Box"],
      ["method", "1", "Box"], ["method", "\"quoted\"", "Box"],
      ["method", "[\"literal\"]", "Box"], ["method", "[name]", "Box"],
      ["method", "gen", "Box"],
    ]);
    expect((await inspectOutline("sample.ts")).map((entry) =>
      [entry.kind, entry.name, entry.receiver],
    )).toEqual([
      ["type", "Shape", undefined], ["type", "Name", undefined],
      ["type", "Choice", undefined], ["class", "Typed", undefined],
      ["method", "method", "Typed"], ["method", "#private", "Typed"],
      ["method", "1", "Typed"], ["method", "\"quoted\"", "Typed"],
      ["method", "[\"literal\"]", "Typed"], ["method", "[name]", "Typed"],
      ["method", "gen", "Typed"],
    ]);
    const python = await inspectOutline("sample.py");
    expect(python.map((entry) => [entry.kind, entry.name, entry.receiver, outlineLine(entry)])).toEqual([
      ["import", "package", undefined, 1], ["import", "alias", undefined, 1],
      ["import", "local", undefined, 2], ["import", "first", undefined, 3],
      ["import", "second_alias", undefined, 3], ["variable", "value", undefined, 7],
      ["function", "run", undefined, 8], ["class", "Box", undefined, 11],
      ["method", "method", "Box", 14],
    ]);
    expect(JSON.stringify(python)).not.toMatch(/nested|field|secret/u);
    const assignments = await inspectOutline("assignments.py");
    expect(assignments.map((entry) => entry.name)).toEqual([
      "a", "b", "annotated", "left", "middle", "right", "first_item", "second_item",
    ]);
    expect(JSON.stringify(assignments)).not.toMatch(/obj|items|Type|source_value/u);
  });

  test("recognizes every stable TypeScript 7 source format", async () => {
    const directory = await temporaryDirectory("inspect-typescript-formats-");
    process.chdir(directory);
    const fixtures = [
      ["sample.ts", "typescript", "export const value = 1;"],
      ["sample.tsx", "typescript", "export const value = <div />;"],
      ["sample.d.ts", "typescript", "export declare function value(\n  input: number,\n): number;"],
      ["sample.mts", "typescript", "export const value = 1;"],
      ["sample.d.mts", "typescript", "export declare function value(\n  input: number,\n): number;"],
      ["sample.cts", "typescript", "export const value = 1;"],
      ["sample.d.cts", "typescript", "export declare function value(\n  input: number,\n): number;"],
      ["sample.js", "javascript", "export const value = 1;"],
      ["sample.jsx", "javascript", "export const value = <div />;"],
      ["sample.mjs", "javascript", "export const value = 1;"],
      ["sample.cjs", "javascript", "exports.value = 1;"],
      ["sample.pyi", "python", "value: int"],
    ] as const;
    await Promise.all(fixtures.map(([name, , source]) => writeFile(name, `${source}\n`)));

    for (const [name, language] of fixtures) {
      const result = (await inspect(name)).result;
      expect(result.data.kind).toBe("code");
      expect(result.data.language).toBe(language);
      expect(result.data.parse_complete).toBe(true);
    }
    for (const name of ["sample.d.ts", "sample.d.mts", "sample.d.cts"]) {
      expect((await inspectOutline(name)).map((entry) => [
        entry.kind,
        entry.name,
        outlineLine(entry),
        outlineLine(entry, "line_end"),
      ]))
        .toEqual([["function", "value", 1, 3]]);
    }
    expect((await inspectOutline("sample.pyi")).map((entry) => [entry.kind, entry.name]))
      .toEqual([["variable", "value"]]);
  });
});

describe("inspect_file command contract", () => {
  test("reads absolute, parent-relative and outside symlink paths like hcat", async () => {
    const directory = await temporaryDirectory("inspect-paths-");
    const outside = await temporaryDirectory("inspect-outside-");
    await writeFile(path.join(outside, "value.json"), '{"value":42}\n');
    process.chdir(directory);
    await symlink(path.join(outside, "value.json"), "linked.json");
    const tool = createInspectFileTool("test", "");
    for (const input of [path.join(outside, "value.json"), path.relative(directory, path.join(outside, "value.json")), "linked.json"]) {
      const result = await tool.execute([input], executionContext);
      expect(result.exitCode).toBe(0);
      expect(JSON.parse(result.stdout!).data.outline).toHaveLength(2);
    }
    const directoryResult = await tool.execute([outside], executionContext);
    expect(JSON.parse(directoryResult.stdout!).error.code).toBe("not_regular");
  });

  test("accepts only a path operand", async () => {
    const tool = createInspectFileTool("test", "");
    for (const args of [
      ["--max-tokens", "0", "sample.go"],
      ["--source", "Pick", "sample.go"],
      ["--source-bytes", "100", "sample.go"],
    ]) {
      const result = await tool.execute(args, executionContext);
      expect(result.exitCode).toBe(1);
      expect(JSON.parse(result.stdout!).error.code).toBe("usage");
    }
  });

  test("inspects workspace @shell paths like other files", async () => {
    const directory = await temporaryDirectory("inspect-shell-path-");
    process.chdir(directory);
    await mkdir("@shell");
    await writeFile(path.join("@shell", "sample.go"), "package p\nfunc Visible() {}\n");
    const result = await inspect("@shell/sample.go");
    expect(result.exitCode).toBe(0);
    expect(result.result.data.outline.map((entry: Record<string, unknown>) => entry.name)).toEqual(["Visible"]);
  });
});

describe("inspect_file bounds and paths", () => {

  test.each(["none.bin", "outline.go"])("classifies minimum-result overflow for %s", async (name) => {
    const directory = await temporaryDirectory("inspect-file-output-limit-");
    const target = path.join(directory, name);
    await writeFile(target, "package p\nfunc Visible() {}\n");
    // Resolution stops at the filesystem root, but the reported relative path
    // retains its parent components and can exceed the result byte budget.
    const operand = "../".repeat(22_000) + target.slice(path.parse(target).root.length);
    expect(path.resolve(operand)).toBe(target);
    const result = await inspect(operand);
    expect(result.exitCode).toBe(1);
    expect(result.result.error.code).toBe("output_limit");
    expect(result.failureClass).toBe("output_limit");
  });

  test("emits LINE:HASH span identities without source bodies", async () => {
    const directory = await temporaryDirectory("inspect-file-hash-");
    process.chdir(directory);
    const source = [
      "package p",
      "func Visible() {",
      "	secret := \"body-secret\"",
      "}",
      "",
    ].join("\n");
    await writeFile("sample.go", source);
    const outline = await inspectOutline("sample.go");
    const lines = new LineMap(source);
    expect(outline.map((entry) => [entry.kind, entry.name, entry.line, entry.line_end])).toEqual([
      [
        "function",
        "Visible",
        `2:${hashLine(lines.logicalLine(2)?.text ?? "")}`,
        `4:${hashLine(lines.logicalLine(4)?.text ?? "")}`,
      ],
    ]);
    expect(JSON.stringify(outline)).not.toMatch(/body-secret|secret/u);
  });

  test("reports recovery, exact line counts, and unsupported metadata", async () => {
    const directory = await temporaryDirectory("inspect-file-");
    process.chdir(directory);
    await writeFile("broken.go", "package p\nfunc broken( {\n");
    expect((await inspect("broken.go")).result.data.parse_complete).toBe(false);
    for (const [name, content, count] of [
      ["empty.go", "", 0], ["plain.go", "a", 1], ["lf.go", "a\n", 1],
      ["crlf.go", "a\r\n", 1], ["cr.go", "a\rb", 2],
      ["only.go", "\n", 1], ["twice.go", "\n\n", 2],
    ] as const) {
      await writeFile(name, content);
      expect((await inspect(name)).result.data.line_count).toBe(count);
    }
    await writeFile("blob.bin", Uint8Array.from([0xff, 0xfe, 0xfd]));
    expect((await inspect("blob.bin")).result).toEqual({
      ok: true,
      data: {
        path: "blob.bin", kind: "none", language: null, size_bytes: 3,
        line_count: null, parse_complete: true, outline: [],
      },
      truncated: false,
      truncation: null,
    });
  });
});
