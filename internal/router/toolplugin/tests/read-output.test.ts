import {describe, expect, test} from "bun:test";
import {selectReadOutput, type ReadPageRequest} from "../../../../plugins/read_output.ts";
import {countGPT5Tokens} from "../../../../plugins/tokens.ts";

const input = (stdout: string, stdoutKind: "" | "rows" | "json" = ""): ReadPageRequest => ({
  stdout, stderr: "", stdoutKind, stderrKind: "", position: [0, 0], stream: "stdout",
});

describe("shared read pagination", () => {
  test("never splits a complete row or manufactures a completed row", () => {
    const rows = Array.from({length: 30}, (_, i) => `${i + 1}:abcd word π🙂\n`);
    const request = input(rows.join(""), "rows");
    let joined = "";
    for (let page = 0; page < 100; page++) {
      const result = selectReadOutput(request, 80);
      expect(countGPT5Tokens(result.text)).toBeLessThanOrEqual(80);
      const payload = result.text;
      expect(payload.endsWith("\n")).toBe(true);
      joined += payload;
      request.position = result.position;
      if (result.complete) break;
    }
    expect(joined).toBe(rows.join(""));
    const oversizedRow = `1:abcd ${"word ".repeat(200)}\n`;
    expect(selectReadOutput(input(oversizedRow, "rows"), 80)).toEqual({
      text: "", position: [0, 0], complete: false,
      neededTokens: countGPT5Tokens(oversizedRow),
    });
  });

  test("JSON pages contain complete entries and valid arrays", () => {
    const entries = Array.from({length: 25}, (_, i) => ({name: `item ${i}`, line: `${i + 1}:abcd`}));
    const request = input(JSON.stringify(entries), "json");
    const joined: unknown[] = [];
    for (let page = 0; page < 100; page++) {
      const result = selectReadOutput(request, 96);
      expect(countGPT5Tokens(result.text)).toBeLessThanOrEqual(96);
      joined.push(...JSON.parse(result.text));
      request.position = result.position;
      if (result.complete) break;
    }
    expect(joined).toEqual(entries);
  });

  test("JSON recovery preserves large numbers and nested raw entry values", () => {
    const source = '[9007199254740993,1e400,{"n":9007199254740993,"s":"a,]b","a":[1e400]}]';
    const result = selectReadOutput(input(source, "json"), 200);
    expect(result.text).toBe(source);
    expect(result.complete).toBe(true);
  });

  test("raw byte pages preserve Unicode and do not duplicate bytes", () => {
    const value = "α🙂text".repeat(100);
    const request = input(value);
    let joined = "";
    for (let page = 0; page < 100; page++) {
      const result = selectReadOutput(request, 32);
      expect(countGPT5Tokens(result.text)).toBeLessThanOrEqual(32);
      joined += result.text;
      request.position = result.position;
      if (result.complete) break;
    }
    expect(joined).toBe(value);
  });

  test("actual framing, not an arbitrary 64-token cutoff, determines admission", () => {
    const result = selectReadOutput(input("x"), 32);
    expect(result.complete).toBe(true);
    expect(selectReadOutput(input("x"), 1).text).toBe("x");
    const request = input("α");
    request.position = [1, 0];
    expect(() => selectReadOutput(request, 64)).toThrow("UTF-8");
  });
});

describe("read stream frames", () => {
  test.each([
    ["", "", ""],
    ["out", "", "out"],
    ["out\n", "", "out\n"],
    ["", "err", "[stderr bytes]\nerr\n[/stderr]\n"],
    ["out", "err", "[stdout bytes]\nout\n[/stdout]\n[stderr bytes]\nerr\n[/stderr]\n"],
    ["out\n", "err\n", "[stdout bytes]\nout\n\n[/stdout]\n[stderr bytes]\nerr\n\n[/stderr]\n"],
  ])("stdout %j, stderr %j", (stdout, stderr, expected) => {
    const result = selectReadOutput({...input(stdout), stderr, stream: ""}, 100);
    expect(result.text).toBe(expected);
    expect(result.complete).toBe(true);
  });

  test("selection suppresses other streams and their frames", () => {
    const request = {...input("out"), stderr: "err"};
    expect(selectReadOutput(request, 100).text).toBe("out");
    expect(selectReadOutput({...request, stream: "stderr"}, 100).text)
      .toBe("[stderr bytes]\nerr\n[/stderr]\n");
  });

  test("empty JSON placeholders do not appear before or after their entries", () => {
    const request: ReadPageRequest = {...input("x".repeat(200)), stderr: "[1]", stderrKind: "json", stream: ""};
    const first = selectReadOutput(request, 4);
    expect(first.text).not.toContain("stderr");
    expect(first.position[1]).toBe(0);
    const last = selectReadOutput({...request, position: [200, 1]}, 100);
    expect(last.text).toBe("");
    expect(last.complete).toBe(true);
  });

  test("adding stderr frames cannot exceed the page budget", () => {
    const request: ReadPageRequest = {...input("short output"), stderr: "diagnostic ".repeat(30), stream: ""};
    let out = "", err = "";
    for (let page = 0; page < 100; page++) {
      const result = selectReadOutput(request, 24);
      expect(countGPT5Tokens(result.text)).toBeLessThanOrEqual(24);
      const framed = /\[(stdout|stderr) bytes\]\n([\s\S]*?)\n\[\/\1\]\n/g;
      if (result.text.startsWith("[")) {
        for (const match of result.text.matchAll(framed)) {
          if (match[1] === "stdout") out += match[2];
          else err += match[2];
        }
      } else out += result.text;
      request.position = result.position;
      if (result.complete) break;
    }
    expect(out).toBe(request.stdout);
    expect(err).toBe(request.stderr);
  });
});
