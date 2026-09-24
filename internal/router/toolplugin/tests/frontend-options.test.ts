import {expect, test} from "bun:test";
import {readerOptions} from "../../../../plugins/common.ts";
import {createMCatTool} from "../../../../plugins/mcat.ts";
import {createInspectFileTool} from "../../../../plugins/inspect_file.ts";
import {createMSymbolTool} from "../../../../plugins/msymbol.ts";

test("frontends accept inline budgets through their parser", async () => {
  for (const [tool, input] of [
    [createMCatTool(""), "file.go 1:3"],
    [createInspectFileTool(""), "file.go"],
    [createMSymbolTool(""), "def file.go 1 Name"],
  ] as const) {
    for (const value of ["1", "4000", "15500"]) {
      const inline = await tool.parse(`--max-tokens=${value} ${input}`);
      const separate = await tool.parse(`--max-tokens ${value} ${input}`);
      expect(readerOptions(inline).options.maxTokens).toBe(Number(value));
      expect(readerOptions(inline).rest).toEqual(readerOptions(separate).rest);
    }
    for (const value of ["", "0", "01", "15501", "x"]) {
      await expect(async () => readerOptions(await tool.parse(`--max-tokens=${value} ${input}`))).toThrow(
        "--max-tokens requires one integer from 1 to 15500 and cannot repeat",
      );
    }
  }
});

test("inline options preserve operand indices and terminators", () => {
  expect(readerOptions(["--max-tokens=12", "a", "--", "--max-tokens=99"])).toEqual({
    options: {maxTokens: 12}, rest: ["a", "--max-tokens=99"], indices: [1, 3],
  });
  expect(() => readerOptions(["--max-tokens=12", "--max-tokens", "13"])).toThrow(
    "--max-tokens requires one integer from 1 to 15500 and cannot repeat",
  );
});
