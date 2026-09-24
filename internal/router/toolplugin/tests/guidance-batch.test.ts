import {expect, test} from "bun:test";
import {readFile} from "node:fs/promises";

test("projected batch example preserves exits, sessions, and sibling results", async () => {
  const template = await readFile(new URL("../../../../guidance/frontend_guidance.md.tmpl", import.meta.url), "utf8");
  const source = template.match(/```js\n([\s\S]*?)\n```/u)?.[1];
  expect(source).toBeDefined();
  const AsyncFunction = Object.getPrototypeOf(async () => {}).constructor;
  for (const failing of [false, true]) {
    const output: string[] = [];
    let index = 0;
    await new AsyncFunction("tools", "text", source)({
      exec_command: async () => {
        if (index++ === 0) {
          if (failing) throw new Error("fixture failure");
          return {output: "", exit_code: 7};
        }
        return {output: "working", session_id: 123};
      },
    }, (value: string) => output.push(value));
    expect(output).toEqual([
      failing ? "source: Error: fixture failure" : "source: exit_code=7\n",
      "tests: running session_id=123\nworking",
    ]);
  }
});
