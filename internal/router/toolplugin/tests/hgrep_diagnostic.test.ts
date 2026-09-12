import {afterEach, expect, test} from "bun:test";
import {mkdtemp, readFile, rm, writeFile} from "node:fs/promises";
import {tmpdir} from "node:os";
import path from "node:path";

import {formatVerifiedRow} from "mekugi:core/v1";
import {createHGrepTool} from "../../../../plugins/hgrep.ts";

const originalPath = process.env.PATH;
const directories: string[] = [];

afterEach(async () => {
  if (originalPath === undefined) {
    delete process.env.PATH;
  } else {
    process.env.PATH = originalPath;
  }
  await Promise.all(directories.splice(0).map((directory) => rm(directory, {recursive: true, force: true})));
});

test.each([
  ["rg: missing.txt: No such file or directory (os error 2)", "No such file or directory (os error 2)"],
  ["rg: private: path.txt: Permission denied (os error 13)", "Permission denied (os error 13)"],
  ["rg: missing.txt: IO error for operation on missing.txt: No such file or directory (os error 2)", "No such file or directory (os error 2)"],
  ["regex parse error:\nerror: unclosed character class", "error: unclosed character class"],
])("preserves useful ripgrep diagnostics without redundant paths: %s", async (diagnostic, expected) => {
  const directory = await mkdtemp(path.join(tmpdir(), "hgrep-diagnostic-"));
  directories.push(directory);
  const executable = path.join(directory, "rg");
  await writeFile(executable, '#!/bin/sh\ncat "${0}.stderr" >&2\nexit 2\n', {mode: 0o700});
  await writeFile(`${executable}.stderr`, `${diagnostic}\n`, "utf8");
  process.env.PATH = `${directory}${path.delimiter}${originalPath ?? ""}`;

  const result = await createHGrepTool("description", "start: TEST").execute(["needle", "missing.txt"], {
    stdinFD: null,
    scriptReadFD: null,
    scriptWriteFD: null,
    outputBudgetBytes: 16 * 1024 * 1024,
  });
  expect(result).toEqual({stderr: `hgrep: ${expected}\n`, exitCode: 1, failureClass: "search_error"});
});

test("bounds incomplete JSON events, preserves admitted rows and reaps rg", async () => {
  const directory = await mkdtemp(path.join(tmpdir(), "hgrep-wire-bound-"));
  directories.push(directory);
  const executable = path.join(directory, "rg");
  const source = path.join(directory, "source.txt");
  await writeFile(source, "needle\n");
  const event = JSON.stringify({type: "match", data: {
    path: {text: source}, lines: {text: "needle\n"}, absolute_offset: 0,
  }});
  await writeFile(`${executable}.event`, `${event}\n`);
  await writeFile(executable, `#!/usr/bin/python3
import os, sys, time
with open(__file__ + ".pid", "w") as pid:
    pid.write(str(os.getpid()))
with open(__file__ + ".event", "rb") as event:
    sys.stdout.buffer.write(event.read())
sys.stdout.buffer.flush()
for _ in range(300):
    sys.stdout.buffer.write(b"x" * 65536)
    sys.stdout.buffer.flush()
time.sleep(60)
`, {mode: 0o700});
  process.env.PATH = `${directory}${path.delimiter}${originalPath ?? ""}`;
  const result = await createHGrepTool("test", "").execute(["needle", source], {
    stdinFD: null, scriptReadFD: null, scriptWriteFD: null, outputBudgetBytes: 16 * 1024 * 1024,
  });
  expect(result).toEqual({
    stdout: `${JSON.stringify(source)}:${formatVerifiedRow(1, "needle")}`,
    stderr: "hgrep: output incomplete: rg JSON event exceeds the 16777216-byte wire bound\n",
    exitCode: 1,
    failureClass: "output_limit",
  });
  const pid = Number(await readFile(`${executable}.pid`, "utf8"));
  expect(() => process.kill(pid, 0)).toThrow();
}, 10_000);
