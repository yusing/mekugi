import {afterEach, expect, test} from "bun:test";
import {spawnSync} from "node:child_process";
import {mkdtemp, rm, writeFile} from "node:fs/promises";
import {tmpdir} from "node:os";
import path from "node:path";
import {fileURLToPath} from "node:url";

const hostPath = fileURLToPath(new URL("../host.mjs", import.meta.url));
const temporaryDirectories: string[] = [];

afterEach(async () => {
  await Promise.all(
    temporaryDirectories.splice(0).map((directory) => rm(directory, {recursive: true, force: true})),
  );
});

test("bounds combined executor output", async () => {
  const directory = await mkdtemp(path.join(tmpdir(), "tool-plugin-limits-"));
  temporaryDirectories.push(directory);
  await writeFile(
    path.join(directory, "plugin.mjs"),
    `export default {
  apiVersion: "mekugi-tool-plugin/v1",
  id: "execution-output.test",
  tools: [{
    specification: {type: "custom", name: "execution_output_test", description: "test tool"},
    parse(input) { return input; },
    argv(input) { return [input]; },
    execute() {
      return {stdout: "x".repeat(16 * 1024 * 1024 + 1), stderr: "", exitCode: 0};
    }
  }]
};
`,
    "utf8",
  );

  const result = spawnSync("node", [hostPath], {
    cwd: directory,
    encoding: "utf8",
    env: {...process.env, NODE_NO_WARNINGS: "1"},
    input: JSON.stringify({
      operation: "execute",
      outputBudgetBytes: 16 * 1024 * 1024,
      snapshotRoot: directory,
      module: "plugin.mjs",
      index: 0,
      arguments: [],
    }),
  });
  expect(result.status).toBe(1);
  expect(result.stderr).toContain(
    "executor stdout and stderr exceed 16777216 UTF-8 bytes",
  );
});
