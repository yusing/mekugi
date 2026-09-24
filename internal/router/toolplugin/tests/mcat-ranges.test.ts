import {afterEach, expect, test} from "bun:test";
import {mkdtemp, rm, writeFile} from "node:fs/promises";
import {tmpdir} from "node:os";
import path from "node:path";

import {createMCatTool} from "../../../../plugins/mcat.ts";

const temporaryDirectories: string[] = [];
const executionContext = {stdinFD: null, scriptReadFD: null, scriptWriteFD: null, outputBudgetBytes: 16 * 1024 * 1024};

async function temporaryDirectory(): Promise<string> {
  const directory = await mkdtemp(path.join(tmpdir(), "mcat-ranges-"));
  temporaryDirectories.push(directory);
  return directory;
}

afterEach(async () => {
  await Promise.all(temporaryDirectories.splice(0).map((directory) =>
    rm(directory, {recursive: true, force: true})));
});

test("accepts colon and dash spans with the same inclusive rows", async () => {
  const directory = await temporaryDirectory();
  const file = path.join(directory, "rows.txt");
  await writeFile(file, "one\ntwo\nthree\n", "utf8");
  const tool = createMCatTool("");

  const colon = await tool.execute([file, "2:3"], executionContext);
  const dash = await tool.execute([file, "2-3"], executionContext);
  expect(colon).toEqual({stdout: "two\nthree\n", exitCode: 0});
  expect(dash).toEqual(colon);
});

test("--number uses absolute source lines and numbers blank rows like nl -ba", async () => {
  const directory = await temporaryDirectory();
  const file = path.join(directory, "rows.txt");
  await writeFile(file, "first\r\n\r\nthird", "utf8");
  const tool = createMCatTool("");
  expect((await tool.execute(["--number", file], executionContext)).stdout).toBe(
    "     1\tfirst\n     2\t\n     3\tthird\n");
  expect((await tool.execute([file, "2:3", "--number"], executionContext)).stdout).toBe(
    "     2\t\n     3\tthird\n");
  const limited = await tool.execute(["--number", "-n", "1", file, "2:3"], executionContext);
  expect(limited.stdout).toBe("     2\t\n");
  expect(limited.omittedOutput?.stdout).toBe("     3\tthird\n");
  const tail = await tool.execute(["--number", "--tail", "-n", "1", file], executionContext);
  expect(tail.stdout).toBe("     3\tthird\n");
  expect(tail.omittedOutput?.stdout).toBe("     1\tfirst\n     2\t\n");
  expect((await tool.execute(["--number", "--number", file], executionContext)).failureClass).toBe("invalid_arguments");
});

test("reports the exact omitted EOF span and preserves start-past-EOF failure status", async () => {
  const directory = await temporaryDirectory();
  const file = path.join(directory, "rows.txt");
  await writeFile(file, "one\ntwo\nthree\n", "utf8");
  const tool = createMCatTool("");

  expect(await tool.execute([file, "1:9"], executionContext)).toEqual({
    stdout: "one\ntwo\nthree\n",
    stderr: "mcat: rows 4:9 past EOF (3 rows)\n",
    exitCode: 0,
  });
  expect(await tool.execute([file, "4:9"], executionContext)).toEqual({
    stderr: "mcat: rows 4:9 past EOF (3 rows)\n",
    exitCode: 1,
    failureClass: "reader_error",
  });
});
