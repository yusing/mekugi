import {expect, test} from "bun:test";
import {spawnSync} from "node:child_process";
import {chmod, mkdtemp, rm, writeFile} from "node:fs/promises";
import {tmpdir} from "node:os";
import path from "node:path";

const preload = path.resolve(import.meta.dir, "preload.ts");
const msymbol = path.resolve(import.meta.dir, "../../../../plugins/msymbol.ts");
const lsp = path.resolve(import.meta.dir, "../../../../plugins/lsp.ts");

for (const resolver of ["gopls", "lsp"] as const) {
  for (const outcome of ["success", "failure", "spawn failure"] as const) {
    test(`${resolver} host exits after ${outcome} without retaining its deadline`, async () => {
      const directory = await mkdtemp(path.join(tmpdir(), "resolver-deadline-"));
      try {
        await writeFile(path.join(directory, "input.go"), "package p\nvar Target = 1\n");
        const executable = path.join(directory, "gopls");
        if (outcome !== "spawn failure") {
          await writeFile(executable, `#!/bin/sh\nexit ${outcome === "success" ? 0 : 1}\n`);
          await chmod(executable, 0o700);
        }
        const server = path.join(directory, "server.mjs");
        await writeFile(server, String.raw`
let input = Buffer.alloc(0);
process.stdin.on("data", (chunk) => {
  input = Buffer.concat([input, chunk]);
  while (true) {
    const end = input.indexOf("\r\n\r\n");
    if (end < 0) return;
    const length = Number(/Content-Length: ([0-9]+)/iu.exec(input.subarray(0, end).toString())?.[1]);
    if (input.length < end + 4 + length) return;
    const message = JSON.parse(input.subarray(end + 4, end + 4 + length).toString());
    input = input.subarray(end + 4 + length);
    if (message.method === "exit") process.exit(0);
    if (message.id === undefined) continue;
    const result = message.method === "initialize"
      ? {capabilities: {positionEncoding: process.argv[2] === "failure" ? "utf-8" : "utf-16"}}
      : message.method === "shutdown" ? null : [];
    const body = JSON.stringify({jsonrpc: "2.0", id: message.id, result});
    process.stdout.write("Content-Length: " + Buffer.byteLength(body) + "\r\n\r\n" + body);
  }
});
`);
        const script = resolver === "gopls" ? `
import {createMSymbolTool} from ${JSON.stringify(msymbol)};
const result = await createMSymbolTool("test").execute(
  ["refs", "input.go", "2", "Target"],
  {stdinFD: null, scriptReadFD: null, scriptWriteFD: null, outputBudgetBytes: 1024},
);
console.log(result.exitCode === 0 ? "success" : "failure");
` : `
import {runLSPQuery} from ${JSON.stringify(lsp)};
try {
  await runLSPQuery({
    command: ${JSON.stringify(outcome === "spawn failure" ? path.join(directory, "missing") : process.execPath)},
    args: ${JSON.stringify([server, outcome])}, workspace: process.cwd(),
    path: ${JSON.stringify(path.join(directory, "input.ts"))}, languageID: "typescript",
    source: "const value = 1;", position: {line: 0, character: 6}, mode: "def",
  });
  console.log("success");
} catch { console.log("failure"); }
`;
        const result = spawnSync(process.execPath, ["--preload", preload, "--eval", script], {
          cwd: directory,
          env: {...process.env, PATH: directory},
          encoding: "utf8",
          timeout: 4_000,
          killSignal: "SIGKILL",
        });
        expect(result.error).toBeUndefined();
        expect(result.status).toBe(0);
        expect(result.stderr).toBe("");
        expect(result.stdout.trim()).toBe(outcome === "success" ? "success" : "failure");
      } finally {
        await rm(directory, {recursive: true, force: true});
      }
    }, 10_000);
  }
}
