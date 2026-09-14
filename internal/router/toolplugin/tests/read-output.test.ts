import {describe, expect, test} from "bun:test";
import {selectReadOutput, type ReadPageRequest} from "../../../../plugins/read_output.ts";
import {countGPT5Tokens} from "../../../../plugins/tokens.ts";

const input = (stdout: string, stdoutKind: "" | "rows" | "json" = ""): ReadPageRequest => ({
  stdout, stderr: "", stdoutKind, stderrKind: "", position: [0, 0], stream: "stdout",
});

describe("shared read pagination", () => {
  test("never splits a verified row or manufactures a completed row", () => {
    const rows = Array.from({length: 30}, (_, i) => `${i + 1}:abcd word π🙂\n`);
    const request = input(rows.join(""), "rows");
    let joined = "";
    for (let page = 0; page < 100; page++) {
      const result = selectReadOutput(request, 80);
      expect(countGPT5Tokens(result.text)).toBeLessThanOrEqual(80);
      const payload = result.text.slice("--- stdout [rows] ---\n".length, -1);
      expect(payload.endsWith("\n")).toBe(true);
      joined += payload;
      request.position = result.position;
      if (result.complete) break;
    }
    expect(joined).toBe(rows.join(""));
    expect(() => selectReadOutput(input(`1:abcd ${"word ".repeat(200)}\n`, "rows"), 80))
      .toThrow("next complete read unit does not fit");
  });

  test("JSON pages contain complete entries and valid arrays", () => {
    const entries = Array.from({length: 25}, (_, i) => ({name: `item ${i}`, line: `${i + 1}:abcd`}));
    const request = input(JSON.stringify(entries), "json");
    const joined: unknown[] = [];
    for (let page = 0; page < 100; page++) {
      const result = selectReadOutput(request, 96);
      expect(countGPT5Tokens(result.text)).toBeLessThanOrEqual(96);
      joined.push(...JSON.parse(result.text.slice("--- stdout [json] ---\n".length, -1)));
      request.position = result.position;
      if (result.complete) break;
    }
    expect(joined).toEqual(entries);
  });

  test("JSON recovery preserves large numbers and nested raw entry values", () => {
    const source = '[9007199254740993,1e400,{"n":9007199254740993,"s":"a,]b","a":[1e400]}]';
    const result = selectReadOutput(input(source, "json"), 200);
    expect(result.text).toBe(`--- stdout [json] ---\n${source}\n`);
    expect(result.complete).toBe(true);
  });

  test("raw byte pages preserve Unicode and do not duplicate bytes", () => {
    const value = "α🙂text".repeat(100);
    const request = input(value);
    let joined = "";
    for (let page = 0; page < 100; page++) {
      const result = selectReadOutput(request, 32);
      expect(countGPT5Tokens(result.text)).toBeLessThanOrEqual(32);
      joined += result.text.slice("--- stdout [bytes] ---\n".length, -1);
      request.position = result.position;
      if (result.complete) break;
    }
    expect(joined).toBe(value);
  });

  test("actual framing, not an arbitrary 64-token cutoff, determines admission", () => {
    const result = selectReadOutput(input("x"), 32);
    expect(result.complete).toBe(true);
    expect(() => selectReadOutput(input("x"), 1)).toThrow("stream frames");
    const request = input("α");
    request.position = [1, 0];
    expect(() => selectReadOutput(request, 64)).toThrow("UTF-8");
  });
});
