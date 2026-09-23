import {afterEach, beforeEach, describe, expect, spyOn, test} from "bun:test";
import {spawnSync} from "node:child_process";
import {chmod, mkdtemp, mkdir, readFile, rm, symlink, writeFile} from "node:fs/promises";
import {tmpdir} from "node:os";
import path from "node:path";
import {pathToFileURL} from "node:url";

import {BoundedTextOutput, countGPT5Tokens} from "../../../../plugins/common.ts";
import {createMCatTool} from "../../../../plugins/mcat.ts";
import {createMSymbolTool} from "../../../../plugins/msymbol.ts";
import {runLSPQuery} from "../../../../plugins/lsp.ts";
import {
  createInspectFileTool,
  goDeclarationRange,
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
  const directory = await temporaryDirectory("msymbol-gopls-");
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
  const tool = createInspectFileTool(String.raw`\A.+\z`);
  const execution = await tool.execute(argv, executionContext);
  return {...execution, raw: execution.stdout ?? "", result: JSON.parse(execution.stdout ?? "")};
}

async function inspectOutline(name: string): Promise<Record<string, unknown>[]> {
  return (await inspect(name)).result.data.outline;
}

function outlineLine(entry: Record<string, unknown>, field = "line"): number {
	const value = entry[field];
	if (!Number.isSafeInteger(value) || Number(value) < 1) {
		throw new Error(`invalid ${field} identity ${String(value)}`);
	}
	return Number(value);
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

function formatMCatRow(_line: number, content: string): string {
  return `${content}\n`;
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

describe("bounded text output", () => {
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
    const unicode = new BoundedTextOutput(1);
    expect(unicode.append("🙂")).toBe(true);
    expect(unicode.append("x")).toBe(false);
    expect(unicode.current).toBe("🙂");

    const compressible = new BoundedTextOutput(15_000);
    const row = " x".repeat(8000);
    expect(Buffer.byteLength(row)).toBeGreaterThan(15_000);
    expect(countGPT5Tokens(row)).toBeLessThan(15_000);
    expect(compressible.append(row)).toBe(true);
    expect(compressible.append(" later\n")).toBe(true);
    expect(compressible.incomplete).toBe(false);
  });

  test("uses strict GPT-5 limits with complete rows", () => {
    const exact = new BoundedTextOutput(15_500);
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

    const tooLarge = new BoundedTextOutput(15_500);
    const aboveMaximum = contentWithFormattedTokenCount(15_501, (content) => `${content}\n`);
    expect(tooLarge.append(`${aboveMaximum}\n`)).toBe(false);
    expect(tooLarge.current).toBe("");
    expect(tooLarge.incomplete).toBe(true);
  });
});

describe("mcat line limits", () => {
  test("selects complete head/tail lines before optional token limits", async () => {
    const directory = await temporaryDirectory("mcat-lines-");
    const file = path.join(directory, "rows.txt");
    const tool = createMCatTool("");
    await writeFile(file, "one\r\ntwo\rthree\nfour");
    for (const [flags, expected] of [
      [["-n", "2"], formatMCatRow(1, "one") + formatMCatRow(2, "two")],
      [["--tail", "-n", "2"], formatMCatRow(3, "three") + formatMCatRow(4, "four")],
      [["-n", "2", "--max-tokens", "100"], formatMCatRow(1, "one") + formatMCatRow(2, "two")],
      [["--tail", "-n", "2", "--max-tokens", "100"], formatMCatRow(3, "three") + formatMCatRow(4, "four")],
    ] as const) {
      const result = await tool.execute([...flags, file], executionContext);
      expect(result.stdout).toBe(expected);
      expect(result.exitCode).toBe(1);
      expect(result.stderr).toContain("2-line limit");
    }
    const range = await tool.execute(["--tail", "-n", "1", file, "1:2"], executionContext);
    expect(range.stdout).toBe(formatMCatRow(2, "two"));
    expect(await tool.parse('-n 2 --tail "rows.txt"')).toEqual(
      ["-n", "2", "--tail", "rows.txt"]);
    for (const flags of [["-n"], ["-n", "0"], ["-n", "01"], ["-n", "1", "-n", "2"], ["-n", "9007199254740992"]]) {
      expect((await tool.execute([...flags, file], executionContext)).failureClass).toBe("invalid_arguments");
    }
  });

  test("line-only mode admits long complete rows beyond the default token ceiling", async () => {
    const directory = await temporaryDirectory("mcat-line-long-");
    const file = path.join(directory, "rows.txt");
    const content = "word ".repeat(20000);
    await writeFile(file, `${content}\nend`);
    const tool = createMCatTool("");
    const head = await tool.execute(["-n", "1", file], executionContext);
    expect(head.stdout).toBe(formatMCatRow(1, content));
    expect(head.stderr).toContain("1-line limit");
    expect(head.stderr).not.toContain("token limit");
    const tail = await tool.execute(["--tail", "-n", "2", file], executionContext);
    expect(tail.stdout).toBe(formatMCatRow(1, content) + formatMCatRow(2, "end"));
    expect(tail.exitCode).toBe(0);
    const limited = await tool.execute(["-n", "1", "--max-tokens", "20", file], executionContext);
    expect(limited.stdout).toBe("");
    const tailLimited = await tool.execute(["--tail", "-n", "2", "--max-tokens", "20", file], executionContext);
    expect(tailLimited.stdout).toBe(formatMCatRow(2, "end"));
    await writeFile(file, `first\n${"x".repeat(2_000_000)}`);
    const smallHead = await tool.execute(["-n", "1", file], executionContext);
    expect(smallHead.stdout).toBe(formatMCatRow(1, "first"));
    expect(smallHead.stderr).toContain("row 2 exceeds");
    expect(smallHead.omittedOutput).toBeUndefined();
    // Skipping unselected rows must not skip whole-source UTF-8 validation.
    await writeFile(file, Buffer.concat([Buffer.from("first\n"), Buffer.from([0xff])]));
    expect((await tool.execute(["-n", "1", file], executionContext)).stderr).toContain("not UTF-8");

    await writeFile(file, "");
    expect((await tool.execute(["--tail", "-n", "2", file], executionContext)).exitCode).toBe(0);
  });
});

describe("mcat omitted rows", () => {
  test("resumes only omitted rows across logical terminators and bounded selections", async () => {
    const directory = await temporaryDirectory("mcat-resume-");
    const file = path.join(directory, "rows.txt");
    const tool = createMCatTool("");
    for (const ending of ["\n", "\r", "\r\n"]) {
      await writeFile(file, ["one", "", "three", "four", "five"].join(ending));
      const head = await tool.execute(["-n", "2", file, "2:9"], executionContext);
      expect(head.stdout).toBe(formatMCatRow(2, "") + formatMCatRow(3, "three"));
      expect(head.omittedOutput?.stdout).toBe(formatMCatRow(4, "four") + formatMCatRow(5, "five"));
      expect(head.stderr).toContain("[out of range]");
      const rest = await tool.execute([file, "4:5"], executionContext);
      expect(rest).toEqual({
        stdout: formatMCatRow(4, "four") + formatMCatRow(5, "five"),
        exitCode: 0,
      });
      const limited = await tool.execute(["-n", "1", file, "2:3"], executionContext);
      expect(limited.stdout).toBe(formatMCatRow(2, ""));
      expect(limited.stderr).toContain("1-line limit");
      expect(limited.exitCode).toBe(1);
      expect(limited.omittedOutput?.stdout).toBe(formatMCatRow(3, "three"));
      const tail = await tool.execute(["--tail", "-n", "1", file], executionContext);
      expect(tail.omittedOutput?.stdout).toBe(["one", "", "three", "four"].map((row, i) => formatMCatRow(i + 1, row)).join(""));
    }
  });

  test("reports the first unadmitted row for strict token budgets", async () => {
    const directory = await temporaryDirectory("mcat-resume-token-");
    const file = path.join(directory, "rows.txt");
    const tool = createMCatTool("");
    await writeFile(file, "first\nsecond\nthird\n");
    const budget = countGPT5Tokens(formatMCatRow(1, "first"));
    const first = await tool.execute(["--max-tokens", String(budget), file], executionContext);
    expect(first.stdout).toBe(formatMCatRow(1, "first"));
    expect(first.omittedOutput?.stdout).toBe(formatMCatRow(2, "second") + formatMCatRow(3, "third"));
    const none = await tool.execute(["--max-tokens", "1", file], executionContext);
    expect(none.stdout).toBe("");
    expect(none.omittedOutput?.stdout.split("\n").filter(Boolean)).toHaveLength(3);
    expect(none.omittedOutput?.stdoutKind).toBe("rows");
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
    for (const tool of [createMCatTool(""), createInspectFileTool("")]) {
      const before = await tool.execute(["--max-tokens", "200", file], executionContext);
      const after = await tool.execute([file, "--max-tokens", "200"], executionContext);
      expect(await tool.execute([file, "--max-tokens", "200", "--"], executionContext)).toEqual(before);
      expect(after).toEqual(before);
      for (const input of [`--max-tokens 200 ${JSON.stringify(file)}`, `${JSON.stringify(file)} --max-tokens 200`]) {
        const argv = await tool.parse(input);
        expect(await tool.execute(argv, executionContext)).toEqual(before);
      }
    }
  });

  test("bounds omitted rows even when selected line-only rows bypass tokenization", async () => {
    const directory = await temporaryDirectory("reader-omitted-bound-");
    const file = path.join(directory, "rows.txt");
    await writeFile(file, `first\n${"x".repeat(4 * 1024 * 1024)}\nlast\n`);
    const result = await createMCatTool("").execute([file, "-n", "1"], executionContext);
    expect(result.stdout).toBe(formatMCatRow(1, "first"));
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain("row 2 exceeds");
    expect(result.omittedOutput).toBeUndefined();
  });

  test("tail output remains usable when retained rows exceed capacity", async () => {
    const directory = await temporaryDirectory("reader-capacity-");
    const file = path.join(directory, "rows.txt");
    await writeFile(file, `${("x".repeat(20_000) + "\n").repeat(850)}last\n`);
    const result = await createMCatTool("").execute([file, "-n", "1", "--tail"], executionContext);
    expect(result.stdout).toBe(formatMCatRow(851, "last"));
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain("recovery bound");
    expect(result.omittedOutput).toBeUndefined();
  });
});

describe("mcat tail", () => {
  test("returns a whole-row suffix within the budget, including ranges", async () => {
    const directory = await temporaryDirectory("mcat-tail-");
    const file = path.join(directory, "rows with spaces.txt");
    await writeFile(file, "first\r\nsecond\rthird\nlast", "utf8");
    const tool = createMCatTool("");
    const last = formatMCatRow(4, "last");
    const budget = countGPT5Tokens(last);
    const result = await tool.execute(["--tail", "--max-tokens", String(budget), file], executionContext);
    expect(result.stdout).toBe(last);
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain("output incomplete");
    expect(countGPT5Tokens(result.stdout!)).toBeLessThanOrEqual(budget);

    const complete = await tool.execute(["--max-tokens", "200", "--tail", file, "2:3"], executionContext);
    expect(complete.stdout).toBe(formatMCatRow(2, "second") + formatMCatRow(3, "third"));
    expect(complete.exitCode).toBe(0);
    expect(await tool.parse('--tail --max-tokens 100 "rows with spaces.txt" 0:2')).toEqual([
      "--tail", "--max-tokens", "100", "rows with spaces.txt", "1:2",
    ]);
  });

  test("continues after oversized rows but never skips an oversized final row", async () => {
    const directory = await temporaryDirectory("mcat-tail-long-");
    const file = path.join(directory, "rows");
    const tool = createMCatTool("");
    for (const large of [" x".repeat(1000), "a".repeat(2_000_000)]) {
      await writeFile(file, `first\n${large}\nlast\n`, "utf8");
      const result = await tool.execute(["--tail", "--max-tokens", "20", file], executionContext);
      expect(result.stdout).toBe(formatMCatRow(3, "last"));
      expect(result.exitCode).toBe(1);
      await writeFile(file, `first\n${large}`, "utf8");
      const final = await tool.execute(["--tail", "--max-tokens", "20", file], executionContext);
      expect(final.stdout).toBe("");
      expect(final.exitCode).toBe(1);
    }
  });

  test("bounds a long scan while retaining the final rows", async () => {
    const directory = await temporaryDirectory("mcat-tail-scan-");
    const file = path.join(directory, "rows");
    await writeFile(file, "same\n".repeat(10_000), "utf8");
    const result = await createMCatTool("").execute(
      ["--tail", "--max-tokens", "100", file], executionContext);
    expect(result.stdout).toEndWith(formatMCatRow(10_000, "same"));
    expect(countGPT5Tokens(result.stdout!)).toBeLessThanOrEqual(100);
    expect(result.exitCode).toBe(1);
  });

  test("handles a long single-piece final row without quadratic tokenization", async () => {
    const directory = await temporaryDirectory("mcat-tail-long-piece-");
    const file = path.join(directory, "rows");
    const content = " ".repeat(1_500_000);
    await writeFile(file, "before\n".repeat(50) + content, "utf8");
    const result = await createMCatTool("").execute(
      ["--tail", "--max-tokens", "15500", file], executionContext);
    expect(result.stdout).toEndWith(formatMCatRow(51, content));
    expect(result.exitCode).toBe(0);
  }, 15_000);

  test("validates flags and all source bytes, even outside the retained suffix", async () => {
    const directory = await temporaryDirectory("mcat-tail-validation-");
    const file = path.join(directory, "rows");
    const tool = createMCatTool("");
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

describe("mcat built-in plugin", () => {
  test("keeps the private description call-local", () => {
    const description = plugin.tools[0].specification.description.replace(/\s+/g, " ");
    expect(description).toContain("Read one or more UTF-8 files or inclusive logical-line ranges");
    expect(description).toContain("raw rows without line or hash prefixes");
    expect(description).toContain("Usage: mcat [-n N] [--max-tokens N] [--tail] PATH [START:END] [PATH [START:END] ...]");
    expect(description).toContain("START:END is a separate operand after its path, inclusive of both endpoints. -n counts rows within that range.");
    expect(description).toContain("mcat src/main.go 100:150");
    expect(description).toContain("rows 100–150 (51 rows)");
    expect(description).toContain("mcat -n 20 src/main.go");
    expect(description).toContain("first 20 rows");
    expect(description).not.toContain("mcat -n 20 src/main.go 100:150");
    expect(description).toContain("Limited output contains complete rows and exits nonzero; retained omissions provide per-file mread recovery.");
  });

  test("declares a multi-file regex grammar", () => {
    const format = plugin.tools[0].specification.format;
    expect(format?.syntax).toBe("regex");
    if (format === undefined) {
      throw new Error("mcat grammar format is missing");
    }
    for (const input of [
      "plain.txt",
      "plain.txt 2:9",
      "plain.txt 0:9",
      "\"second file.txt\" 2:3",
      "first.txt 1:2 second.txt 3:4",
      `"quoted\\"file.txt"`,
    ]) {
      expect(rustRegexMatches(format.definition, input)).toBe(true);
    }
    for (const input of [
      "",
      "\nplain.txt",
      "plain.txt\n",
      "plain.txt\nsecond.txt",
      "\"unterminated",
    ]) {
      expect(rustRegexMatches(format.definition, input)).toBe(false);
    }
  });

  test("preserves quoted option-like paths through execution", async () => {
    const directory = await temporaryDirectory("mcat-option-path-");
    process.chdir(directory);
    const tool = createMCatTool("");
    for (const name of ["--tail", "--max-tokens", "--preview-bytes", "-n"]) {
      const file = path.join(directory, name);
      await writeFile(file, "first\nsecond\n");
      for (const [prefix, range, expected] of [
        ["", "", formatMCatRow(1, "first") + formatMCatRow(2, "second")],
        ["-n 1 --tail ", " 2:2", formatMCatRow(2, "second")],
      ]) {
        const argv = await tool.parse(`${prefix}${JSON.stringify(name)}${range}`);
        expect(argv).toContain(`./${name}`);
        expect(await tool.execute(argv, executionContext)).toMatchObject({stdout: expected, exitCode: 0});
      }
    }
  });

  test("parses one path and optional range into shell arguments", async () => {
    const tool = createMCatTool("start: TEST");
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
    const directory = await temporaryDirectory("mcat-plugin-");
    process.chdir(directory);
    await writeFile("plain.txt", "alpha\r\nbeta\rgamma\n", "utf8");
    await writeFile("second file.txt", "one\ntwo\nthree", "utf8");
    await writeFile("token-spellings.txt", "<|endoftext|> <|im_start|> <|fim_prefix|>\n", "utf8");

    const tool = createMCatTool("start: TEST");
    const whole = await tool.execute(["plain.txt"], executionContext);
    expect(whole).toEqual({
      stdout: [
        formatMCatRow(1, "alpha"),
        formatMCatRow(2, "beta"),
        formatMCatRow(3, "gamma"),
      ].join(""),
      exitCode: 0,
    });

    const range = await tool.execute(["plain.txt", "2:3"], executionContext);
    expect(range).toEqual({
      stdout: [
        formatMCatRow(2, "beta"),
        formatMCatRow(3, "gamma"),
      ].join(""),
      exitCode: 0,
    });

    const zeroRange = await tool.execute(["plain.txt", "0:3"], executionContext);
    expect(zeroRange).toEqual(whole);

    const overrun = await tool.execute(["plain.txt", "2:5"], executionContext);
    expect(overrun).toEqual({
      stdout: [
        formatMCatRow(2, "beta"),
        formatMCatRow(3, "gamma"),
      ].join(""),
      stderr: "mcat: 4-5: [out of range]\n",
      exitCode: 0,
    });

    const tokenSpellings = await tool.execute(["token-spellings.txt"], executionContext);
    expect(tokenSpellings).toEqual({
      stdout: formatMCatRow(1, "<|endoftext|> <|im_start|> <|fim_prefix|>"),
      exitCode: 0,
    });

    const outside = await tool.execute(["plain.txt", "4:5"], executionContext);
    expect(outside).toEqual({
      stderr: "mcat: start line 4 is past EOF (3 lines)\n",
      exitCode: 1,
      failureClass: "reader_error",
    });

    const missing = await tool.execute(["missing.txt"], executionContext);
    expect(missing).toEqual({
      stderr: "mcat: ENOENT: no such file or directory\n",
      exitCode: 1,
      failureClass: "not_found",
    });

    expect(await tool.execute(["@shell/call-id"], executionContext)).toEqual(missing);
  });

  test("rejects malformed ranges, non-regular files, and invalid UTF-8", async () => {
    const directory = await temporaryDirectory("mcat-plugin-");
    process.chdir(directory);
    await writeFile("short.txt", "one\n", "utf8");
    await writeFile("binary.txt", Uint8Array.from([0xff]));
    await mkdir("folder");
    if (process.platform !== "win32") {
      const created = spawnSync("mkfifo", ["pipe"]);
      expect(created.status).toBe(0);
    }

    const tool = createMCatTool("start: TEST");
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
    const directory = await temporaryDirectory("mcat-limit-");
    process.chdir(directory);
    const first = contentWithFormattedTokenCount(6_000, (content) => formatMCatRow(1, content));
    await writeFile("large.txt", `${first}\nsecond\nthird\n`, "utf8");

    const tool = createMCatTool("start: TEST");
    const result = await tool.execute(["large.txt"], executionContext);
    expect(result).toEqual({
      stdout: formatMCatRow(1, first),
      stderr: "mcat: output incomplete: 6000-token limit reached\n",
      omittedOutput: {stdout: formatMCatRow(2, "second") + formatMCatRow(3, "third"), stderr: "", stdoutKind: "rows"},
      exitCode: 1,
      failureClass: "output_limit",
    });

    const override = await tool.execute(["--max-tokens", "8000", "large.txt"], executionContext);
    expect(override).toEqual({
      stdout: formatMCatRow(1, first) + formatMCatRow(2, "second") + formatMCatRow(3, "third"),
      exitCode: 0,
    });
  });

  test("discards an unavoidably over-limit row while streaming", async () => {
    const directory = await temporaryDirectory("mcat-oversized-row-");
    process.chdir(directory);
    await writeFile("large.txt", " ".repeat(15_500 * 128 + 1), "utf8");

    const tool = createMCatTool("start: TEST");
    const result = await tool.execute(["large.txt"], executionContext);
    expect(result).toEqual({
      stdout: "",
      stderr: "mcat: row 1 exceeds the 1984000-byte inspection bound; use a byte-window reader\n",
      exitCode: 1,
      failureClass: "output_limit",
    });
  });

});

describe("msymbol built-in plugin", () => {
  const symbolRow = (sourcePath: string, line: number, text: string): string =>
    `${JSON.stringify(sourcePath)}:${line} ${text}\n`;

  test("keeps its executable contract behavioral", () => {
	const description = plugin.tools[1].specification.description.replace(/\s+/g, " ");
    expect(description).toContain("msymbol [--max-tokens N] [--workspace ROOT] (def|refs) PATH LINE SYMBOL [N]");
    expect(description).toContain('"PATH":LINE TEXT');
    expect(description).not.toContain("HASH");
    expect(description).toContain("Ambiguous selectors");
    for (const persistent of ["rename", "audit", "before editing"]) {
      expect(description).not.toContain(persistent);
    }
  });

  test("keeps a BOM in Go resolver byte offsets and complete first rows", async () => {
    const directory = await temporaryDirectory("msymbol-bom-");
    process.chdir(directory);
    const source = "\uFEFFpackage p\nfunc Pick() {\n  println(1)\n}\n";
    const target = path.join(directory, "sample.go");
    await writeFile(target, source);
    const fake = await installFakeGopls();
    await fake.respond(`${target}:2:6-10\n`);
    const result = await createMSymbolTool("").execute(["refs", "sample.go", "2", "Pick"], executionContext);
    expect(result).toMatchObject({exitCode: 0});
    expect(await readFile(fake.callsPath, "utf8")).toContain(`:#${Buffer.byteLength(source.slice(0, source.indexOf("Pick")))}`);
    await fake.respond(definitionJSON(target, source, source.indexOf("Pick"), "Pick"));
    const definition = await createMSymbolTool("").execute(["def", "sample.go", "2", "Pick"], executionContext);
    expect(definition.exitCode).toBe(0);
    expect(definition.stdout).toBe([2, 3, 4].map((line) =>
      symbolRow("sample.go", line, source.split("\n")[line - 1])).join(""));
    const goInspection = await createInspectFileTool("").execute(["sample.go"], executionContext);
    expect(JSON.parse(goInspection.stdout!).data).toMatchObject({
      parse_complete: true,
      outline: [{kind: "function", name: "Pick", line: 2, line_end: 4}],
    });
    await writeFile("sample.ts", "\uFEFFfunction pick() {}\n");
    const inspected = await createInspectFileTool("").execute(["sample.ts"], executionContext);
    expect(inspected.exitCode).toBe(0);
    expect(JSON.parse(inspected.stdout!).data.outline[0].line).toBe(1);
  });

  test("parses BOM Markdown frontmatter and JSON with original row identities", async () => {
    const directory = await temporaryDirectory("inspect-bom-");
    process.chdir(directory);
    const tool = createInspectFileTool("");
    await writeFile("frontmatter.md", "\uFEFF---\ntitle: Example\n---\n# Heading\n");
    const markdown = await tool.execute(["frontmatter.md"], executionContext);
    expect(JSON.parse(markdown.stdout!).data).toMatchObject({
      parse_complete: true,
      outline: [
        {kind: "frontmatter", name: "title", line: 2},
        {kind: "heading", line: 4},
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
      expect(data.outline[0].line).toBe(1);
    }
  });

	test("queries a current line in an explicit workspace", async () => {
    const directory = await temporaryDirectory("msymbol-current-");
    const caller = await temporaryDirectory("msymbol-caller-");
    const source = "package p\nfunc Pick() {}\nfunc Use() { Pick() }\n";
    await writeFile(path.join(directory, "sample.go"), source);
    process.chdir(caller);
    const fake = await installFakeGopls();
    const tool = createMSymbolTool("");
    await fake.respond(`${path.join(directory, "sample.go")}:3:14-18\n`);
    const result = await tool.execute(["--workspace", directory, "refs", "sample.go", "3", "Pick"], executionContext);
    expect(result.exitCode).toBe(0);
    expect(result.stdout).toBe(symbolRow(path.join(directory, "sample.go"), 3, "func Use() { Pick() }"));
    expect(result.stderr).toBe(`msymbol: input ${JSON.stringify(path.join(directory, "sample.go"))}:3 (current snapshot)\n`);
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
    const directory = await temporaryDirectory("msymbol-source-failures-");
    const outside = await temporaryDirectory("msymbol-outside-");
    process.chdir(directory);
    await mkdir("directory.go");
    await writeFile("invalid.go", Uint8Array.from([0xff]));
    await writeFile("unsupported.bin", "content");
    await writeFile(path.join(outside, "source.go"), "package p\n");
    await symlink(path.join(outside, "source.go"), "outside.go");
    const fake = await installFakeGopls();
    const tool = createMSymbolTool("");
    for (const [source, diagnostic] of [
      ["missing.go", "path does not exist"],
      ["directory.go", "path is not a regular file"],
      ["invalid.go", "path is not UTF-8"],
      ["unsupported.bin", "path has an unsupported msymbol source format"],
      [path.join(outside, "source.go"), "path is outside the workspace"],
      ["outside.go", "path resolves outside the workspace"],
    ]) {
      const result = await tool.execute(["refs", source, "1", "target"], executionContext);
      expect(result).toEqual({stderr: `msymbol: ${diagnostic}\n`, exitCode: 1, failureClass: "invalid_source"});
    }
    expect(await readFile(fake.callsPath, "utf8")).toBe("");
  });

  test("plain-line selection retains exact tokens and ambiguity checks before resolver startup", async () => {
    const directory = await temporaryDirectory("msymbol-plain-validation-");
    process.chdir(directory);
    await writeFile("sample.go", 'package p\nfunc Use() { target := 1; _ = target; _ = "target" }\n');
    const fake = await installFakeGopls();
    const tool = createMSymbolTool("");
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
    const directory = await temporaryDirectory("msymbol-current-ts-");
    const caller = await temporaryDirectory("msymbol-current-ts-caller-");
    await writeFile(path.join(directory, "tsconfig.json"), '{"include":["*.ts"]}');
    await writeFile(path.join(directory, "sample.ts"), "export const target = 42;\nconsole.log(target);\n");
    process.chdir(caller);
    process.env.PATH = `${pluginBin}${path.delimiter}${originalPath ?? ""}`;
    const result = await createMSymbolTool("").execute([
      "--workspace", directory, "def", "sample.ts", "2", "target",
    ], executionContext);
    expect(result.exitCode).toBe(0);
    expect(result.stdout).toContain(symbolRow(path.join(directory, "sample.ts"), 1, "export const target = 42;"));
    expect(result.stderr).toContain(`${JSON.stringify(path.join(directory, "sample.ts"))}:2 (current snapshot)`);
  }, 30_000);

  test.skipIf(Bun.which("gopls") === null)("Go field references include differently named internal and external tests", async () => {
    const directory = await temporaryDirectory("msymbol-go-callers-");
    process.chdir(directory);
    await writeFile("go.mod", "module example.com/callers\n\ngo 1.26\n");
    await writeFile("state.go", "package callers\ntype State struct { Removed int }\n");
    await writeFile("capture_order_test.go", "package callers\nfunc capture(s State) int { return s.Removed }\n");
    await writeFile("consumer_test.go", 'package callers_test\nimport "example.com/callers"\nfunc use(s callers.State) int { return s.Removed }\n');
    const result = await createMSymbolTool("start: TEST").execute(
      ["refs", "state.go", "2", "Removed"], executionContext,
    );
    expect(result.exitCode).toBe(0);
    expect(result.stderr ?? "").not.toContain("skipped");
    expect(result.stdout).toContain('"state.go":2 ');
    expect(result.stdout).toContain('"capture_order_test.go":2 ');
    expect(result.stdout).toContain('"consumer_test.go":3 ');
  }, 30_000);

  test("validates the Go token selector before starting gopls", async () => {
    const directory = await temporaryDirectory("msymbol-plugin-");
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
    const tool = createMSymbolTool("start: TEST");
    for (const argv of [
      ["refs", "path with spaces.go", "3", "名稱"],
      ["refs", "path with spaces.go", "3", "名稱", "3"],
      ["refs", "path with spaces.go", "3", "名稱", "01"],
      ["refs", "path with spaces.go", "3", "func"],
      ["refs", "path with spaces.go", "3", "Name"],
    ]) {
      const result = await tool.execute(argv, executionContext);
      expect(result.exitCode).toBe(1);
      expect(result.stdout).toBeUndefined();
    }
    expect(await readFile(fake.callsPath, "utf8")).toBe("");

    const selected = await tool.execute(
      ["refs", "path with spaces.go", "3", "名稱", "2"],
      executionContext,
    );
    expect(selected).toEqual({
      stdout: "",
      stderr: "msymbol: input \"path with spaces.go\":3 (current snapshot)\n",
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
    const directory = await temporaryDirectory("msymbol-label-");
    process.chdir(directory);
    const source = "target: while (false) break target;\n";
    await writeFile("input.js", source);
    process.env.PATH = await temporaryDirectory("msymbol-label-empty-path-");
    const result = await createMSymbolTool("start: TEST").execute(
      ["refs", "input.js", "1", "target"],
      executionContext,
    );
    expect(result).toEqual({
      stderr: "msymbol: target is not a symbol token on the selected line\n",
      exitCode: 1,
      failureClass: "resolver_error",
    });
  });

  test("accepts canonical in-workspace paths and rejects workspace escapes before gopls", async () => {
    const directory = await temporaryDirectory("msymbol-path-");
    process.chdir(directory);
    const source = "package sample\nfunc Use() { Target() }\n";
    const inputPath = path.join(directory, "input.go");
    await writeFile(inputPath, source, "utf8");
    const fake = await installFakeGopls();
    const tool = createMSymbolTool("start: TEST");
    expect(await tool.execute(["refs", inputPath, "2", "Target"], executionContext)).toMatchObject({
      stdout: "",
      exitCode: 0,
      terminationReason: "resolver_cleanup",
    });

    const externalDirectory = await temporaryDirectory("msymbol-external-");
    const externalPath = path.join(externalDirectory, "external.go");
    await writeFile(externalPath, source, "utf8");
    await symlink(externalPath, "escaped.go");
    for (const escapedPath of [externalPath, "escaped.go"]) {
      const result = await tool.execute(["refs", escapedPath, "2", "Target"], executionContext);
      expect(result.exitCode).toBe(1);
      expect(result.stderr).toContain("outside the workspace");
      expect(result.stdout).toBeUndefined();
    }
    expect((await readFile(fake.callsPath, "utf8")).trim().split("\n")).toHaveLength(1);
  });

  test("expands only exact top-level and direct-method outline declarations", async () => {
    const directory = await temporaryDirectory("msymbol-def-");
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
	const lines = new LineMap(source);
    const fake = await installFakeGopls();
    const tool = createMSymbolTool("start: TEST");
    const cases = [
      {name: "A", from: 3, to: 3},
      {name: "B", from: 5, to: 7},
      {name: "C", from: 8, to: 10},
      {name: "D", from: 11, to: 13},
      {name: "M", from: 15, to: 17},
    ];

    for (const testCase of cases) {
      const nameOffset = source.indexOf(testCase.name, lines.logicalLine(testCase.from)?.from);
      const response = definitionJSON(filePath, source, nameOffset, testCase.name);
      await fake.respond(response);
      const result = await tool.execute(
        ["def", "declarations.go", "18", testCase.name],
        executionContext,
      );
      let expected = "";
      for (let lineNumber = testCase.from; lineNumber <= testCase.to; lineNumber += 1) {
        const text = lines.logicalLine(lineNumber)?.text;
        if (text === undefined) {
          throw new Error(`missing fixture line ${lineNumber}`);
        }
        expected += symbolRow("declarations.go", lineNumber, text);
      }
      expect(result).toMatchObject({
        stdout: expected,
        exitCode: 0,
        terminationReason: "resolver_cleanup",
      });
    }
  });

  test("falls back to the definition line for unowned or uncertain declarations", async () => {
    const directory = await temporaryDirectory("msymbol-def-");
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
    const result = await createMSymbolTool("start: TEST").execute(
      ["def", "field.go", "5", "Field"],
      executionContext,
    );
    expect(result).toMatchObject({
      stdout: symbolRow("field.go", 3, "  Field int"),
      exitCode: 0,
      terminationReason: "resolver_cleanup",
    });
  });

  test("deduplicates canonical reference rows and reports skipped locations", async () => {
    const directory = await temporaryDirectory("msymbol-refs-");
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
    const externalDirectory = await temporaryDirectory("msymbol-external-");
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
    const result = await createMSymbolTool("start: TEST").execute(
      ["refs", "input.go", "2", "Target"],
      executionContext,
    );
    expect(result).toEqual({
      stdout: symbolRow("result.go", 2, "func Target() {}")
        + symbolRow("input.go", 2, "func Use() { Target() }"),
      stderr: "gopls note\nmsymbol: input \"input.go\":2 (current snapshot)\nmsymbol: skipped 1 location outside workspace, 1 location not Go, 1 location not regular, 1 location not UTF-8, 1 location unavailable\n",
      exitCode: 0,
      terminationReason: "resolver_cleanup",
    });
    expect(await readFile(fake.callsPath, "utf8")).toContain("references -d");
  });

  test("fails an uneditable definition and a missing gopls without useful stdout", async () => {
    const directory = await temporaryDirectory("msymbol-failure-");
    process.chdir(directory);
    const source = "package sample\nfunc Use() { Target() }\n";
    await writeFile("input.go", source, "utf8");
    const externalDirectory = await temporaryDirectory("msymbol-external-");
    const externalSource = "package external\nfunc Target() {}\n";
    const externalPath = path.join(externalDirectory, "external.go");
    await writeFile(externalPath, externalSource, "utf8");
    const response = definitionJSON(externalPath, externalSource, externalSource.indexOf("Target"), "Target");
    const fake = await installFakeGopls();
    await fake.respond(response);
    const tool = createMSymbolTool("start: TEST");
    const external = await tool.execute(["def", "input.go", "2", "Target"], executionContext);
    expect(external).toEqual({
      stderr: "msymbol: input \"input.go\":2 (current snapshot)\nmsymbol: skipped 1 location outside workspace\nmsymbol: definition has no editable workspace location\n",
      exitCode: 1,
      failureClass: "no_editable_location",
      terminationReason: "resolver_cleanup",
    });

    await fake.respond("", "query failed\n", 2);
    const failed = await tool.execute(["refs", "input.go", "2", "Target"], executionContext);
    expect(failed).toEqual({stderr: "msymbol: query failed\n", exitCode: 1, failureClass: "resolver_error", terminationReason: "resolver_cleanup"});

    const emptyPath = await temporaryDirectory("msymbol-empty-path-");
    process.env.PATH = emptyPath;
    const unavailable = await tool.execute(["refs", "input.go", "2", "Target"], executionContext);
    expect(unavailable).toEqual({stderr: "msymbol: gopls is unavailable; expose gopls on the executor PATH\n", exitCode: 1, failureClass: "dependency_unavailable", terminationReason: "resolver_cleanup"});
  });

  test("fails without query output when the selected input changes during gopls", async () => {
    const directory = await temporaryDirectory("msymbol-changing-input-");
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

    const result = await createMSymbolTool("start: TEST").execute(
      ["refs", "input.go", "2", "Target"],
      executionContext,
    );
    expect(result).toEqual({stderr: "msymbol: input changed during query\n", exitCode: 1, failureClass: "resolver_error", terminationReason: "resolver_cleanup"});
  });

  test("applies the shared whole-row token admission to references", async () => {
    const directory = await temporaryDirectory("msymbol-limit-");
    process.chdir(directory);
    const inputSource = "package sample\nfunc Use() { Target() }\n";
    await writeFile("input.go", inputSource, "utf8");
    const resultPath = path.join(directory, "large.go");
    const first = contentWithFormattedTokenCount(
      15_000,
      (content) => symbolRow("large.go", 1, content),
    );
    await writeFile(resultPath, `${first}\nsecond\nthird\n`, "utf8");
    const externalDirectory = await temporaryDirectory("msymbol-limit-external-");
    const externalPath = path.join(externalDirectory, "external.go");
    await writeFile(externalPath, "package external\n", "utf8");
    const goplsStdout = [
      ...[1, 2, 3].map((line) => `${resultPath}:${line}:1-2`),
      `${externalPath}:1:1-2`,
    ].join("\n") + "\n";
    const fake = await installFakeGopls();
    await fake.respond(goplsStdout);
    const result = await createMSymbolTool("start: TEST").execute(
      ["refs", "input.go", "2", "Target"],
      executionContext,
    );
    expect(result).toEqual({
      omittedOutput: {stdout: symbolRow("large.go", 1, first) + symbolRow("large.go", 2, "second") + symbolRow("large.go", 3, "third"), stderr: "", stdoutKind: "rows"},
      stdout: "",
      stderr: expect.stringContaining("msymbol: skipped 1 location outside workspace\n"
        + "msymbol: output incomplete: 4000-token limit reached\n"),
      exitCode: 1,
      failureClass: "output_limit",
      terminationReason: "resolver_cleanup",
    });
  });

  test("retained references survive source removal without another resolver call", async () => {
    const directory = await temporaryDirectory("msymbol-retained-");
    process.chdir(directory);
    const source = "package sample\nfunc Use() { Target() }\n";
    await writeFile("input.go", source);
    const huge = "var Target = 0 // " + "word ".repeat(16000);
    await writeFile("uses.go", huge + "\nfunc Other() { Target() }\n");
    const fake = await installFakeGopls();
    await fake.respond(`${path.join(directory, "uses.go")}:1:5-11\n${path.join(directory, "uses.go")}:2:16-22\n`);
    const result = await createMSymbolTool("start: TEST").execute(
      ["refs", "input.go", "2", "Target"], executionContext,
    );
    expect(result.exitCode).toBe(1);
    expect(result.stdout).toBe("");
    expect(result.stderr).toContain("output incomplete");
    await rm("uses.go");
    expect(result.omittedOutput?.stdout).toBe(
      symbolRow("uses.go", 1, huge) + symbolRow("uses.go", 2, "func Other() { Target() }"),
    );
    expect((await readFile(fake.callsPath, "utf8")).trim().split("\n")).toHaveLength(1);
  });

  test("resolves TypeScript 7 and Python definitions through their LSP servers", async () => {
    const directory = await temporaryDirectory("msymbol-lsp-");
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
    const tool = createMSymbolTool("start: TEST");
    const typescript = await tool.execute(
      ["def", "input.ts", "2", "target"],
      executionContext,
    );
    expect(typescript).toMatchObject({exitCode: 0});
    expect(typescript.stdout).toContain(symbolRow("target.ts", 1, "export function target(value: number) {"));
    expect(typescript.stdout).toContain(symbolRow("target.ts", 3, "}"));

    const ambient = await tool.execute(
      ["def", "ambient_input.ts", "2", "ambientTarget"],
      executionContext,
    );
    expect(ambient).toMatchObject({exitCode: 0});
    for (const [line, text] of ambientTarget.trimEnd().split("\n").entries()) {
      expect(ambient.stdout).toContain(symbolRow("ambient.d.ts", line + 1, text));
    }

    const python = await tool.execute(
      ["def", "input.py", "2", "target"],
      executionContext,
    );
    expect(python).toMatchObject({exitCode: 0});
    expect(python.stdout).toContain(symbolRow("target.py", 1, "def target(value: int) -> int:"));
    expect(python.stdout).toContain(symbolRow("target.py", 2, "    return value + 1"));
    const stub = await tool.execute(
      ["refs", "sample.pyi", "1", "stub_target"],
      executionContext,
    );
    expect(stub).toMatchObject({exitCode: 0});

    process.env.PATH = await temporaryDirectory("msymbol-empty-lsp-path-");
    expect(await tool.execute(
      ["refs", "input.ts", "2", "target"],
      executionContext,
    )).toEqual({stderr: "msymbol: tsc is unavailable; expose TypeScript 7 tsc with --lsp support on the executor PATH\n", exitCode: 1, failureClass: "dependency_unavailable", terminationReason: "resolver_cleanup"});
    expect(await tool.execute(
      ["refs", "input.py", "2", "target"],
      executionContext,
    )).toEqual({stderr: "msymbol: pyright-langserver is unavailable; expose pyright-langserver on the executor PATH\n", exitCode: 1, failureClass: "dependency_unavailable", terminationReason: "resolver_cleanup"});
  });

  test("reaps a language server that ignores shutdown", async () => {
    const directory = await temporaryDirectory("msymbol-lsp-shutdown-");
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
    const directory = await temporaryDirectory("msymbol-typescript-formats-");
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
    const tool = createMSymbolTool("start: TEST");

    for (const [name, source] of fixtures) {
      const result = await tool.execute(
        ["refs", name, "1", "target", "1"],
        executionContext,
      );
      expect(result).toMatchObject({exitCode: 0});
      // The real server may log shutdown timing; backend stderr is preserved.
      expect(result.stderr ?? "").toContain("(current snapshot)");
    }
  }, 30_000);
});

describe("inspect_file built-in plugin", () => {

  test("embeds its outline-only shape schema", async () => {
    const inspectFileDescription = createInspectFileTool("").specification.description;
    const marker = "Result shape schema:\n";
    const schema = JSON.parse(inspectFileDescription.slice(
      inspectFileDescription.indexOf(marker) + marker.length,
    ));
    expect(schema.success.data.outline).toBe("outline_entry[]");
    expect(schema).not.toHaveProperty("selected_entry_source");
    expect(JSON.stringify(schema.outline_entry)).not.toContain("source");
    for (const persistent of ["before editing", "Reason carefully"]) {
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

  test("validates each outline boundary once per inspected snapshot", async () => {
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
      for (const entry of first.result.data.outline) {
        expect(entry.line).toBe(1);
        expect(entry.line_end).toBe(entry.line);
      }
      await writeFile("dense.json", "[true, false]\r\n");
      const second = await inspect("dense.json");
      expect(logicalLine).toHaveBeenCalledTimes(2);
      expect(second.result.data.outline).toHaveLength(3);
      for (const entry of second.result.data.outline) {
        expect(entry.line).toBe(1);
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
    const tool = createMSymbolTool("start: TEST");
    const result = await tool.execute(["def", "scope.go", "6", "localVar"], executionContext);
    expect(result).toMatchObject({ stdout: `"scope.go":3 ${source.split("\n")[2]}\n`, exitCode: 0, terminationReason: "resolver_cleanup" });
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
  test("reads absolute, parent-relative and outside symlink paths like mcat", async () => {
    const directory = await temporaryDirectory("inspect-paths-");
    const outside = await temporaryDirectory("inspect-outside-");
    await writeFile(path.join(outside, "value.json"), '{"value":42}\n');
    process.chdir(directory);
    await symlink(path.join(outside, "value.json"), "linked.json");
    const tool = createInspectFileTool("");
    for (const input of [path.join(outside, "value.json"), path.relative(directory, path.join(outside, "value.json")), "linked.json"]) {
      const result = await tool.execute([input], executionContext);
      expect(result.exitCode).toBe(0);
      expect(JSON.parse(result.stdout!).data.outline).toHaveLength(2);
    }
    const directoryResult = await tool.execute([outside], executionContext);
    expect(JSON.parse(directoryResult.stdout!).error.code).toBe("not_regular");
  });

  test("accepts only a path operand", async () => {
    const tool = createInspectFileTool("");
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

  test("keeps the inspect_file default token ceiling at 4000", async () => {
    const directory = await temporaryDirectory("inspect-file-default-budget-");
    process.chdir(directory);
    const source = [
      "package p",
      ...Array.from({length: 300}, (_, index) => `func Generated${index}() {}`),
    ].join("\n");
    await writeFile("many.go", source);
    const tool = createInspectFileTool("");
    const complete = await tool.execute(["--max-tokens", "15500", "many.go"], executionContext);
    expect(complete.exitCode).toBe(0);
    const completeJSON = JSON.parse(complete.stdout);
    expect(completeJSON.truncated).toBe(false);
    const completeTokens = countGPT5Tokens(complete.stdout);
    expect(completeTokens).toBeGreaterThan(4000);
    expect(completeTokens).toBeLessThanOrEqual(6000);

    const defaultResult = await tool.execute(["many.go"], executionContext);
    expect(defaultResult.exitCode).toBe(1);
    expect(defaultResult.failureClass).toBe("output_limit");
    const defaultJSON = JSON.parse(defaultResult.stdout);
    expect(defaultJSON.truncated).toBe(true);
    expect(defaultJSON.truncation.reason).toBe("output_tokens");
  });

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

  test("emits numeric span identities without source bodies", async () => {
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
    expect(outline.map((entry) => [entry.kind, entry.name, entry.line, entry.line_end])).toEqual([
      [
        "function",
        "Visible",
        2,
        4,
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
