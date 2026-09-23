import {describe, expect, test} from "bun:test";

import {
  classifySourcePath,
  decodeGoStringLiteral,
  decodeQuotedOperand,
  interpreterIdentity,
  isGoIdentifier,
  lineBounds,
  lineCount,
  parsePositiveInteger,
  parseShellHeader,
  SharedCoreError,
} from "../core-v1.mjs";

describe("shared core v1", () => {
  test("owns logical lines", () => {
    expect(lineCount("a\r\nb\rc\n")).toBe(3);
    expect(lineBounds("a\r\nb", 1)).toEqual({
      byteStart: 0,
      byteContentEnd: 1,
      byteEnd: 3,
    });
    expect(lineBounds("a", 2)).toBeNull();
  });

  test("owns portable syntax and source classification", () => {
    expect(parsePositiveInteger("12")).toBe(12);
    expect(decodeQuotedOperand('"a b" rest')).toEqual({value: "a b", rest: " rest"});
    expect(classifySourcePath("source.d.ts")).toMatchObject({
      kind: "code",
      language: "typescript",
      semanticResolver: "typescript",
    });
    expect(classifySourcePath("source.txt")).toBeNull();
    expect(isGoIdentifier("世界9")).toBe(true);
    expect(isGoIdentifier("func")).toBe(false);
    expect(decodeGoStringLiteral('"example.com/pkg"')).toBe("example.com/pkg");
  });

  test("parses shell headers without applying execution policy", () => {
    expect(parseShellHeader("#!/usr/bin/env -S python3 -u\n#!params={\"login\":true}\nprint(1)"))
      .toEqual({
        interpreter: ["python3", "-u"],
        body: "print(1)",
        paramsLine: 2,
        params: {login: true},
      });
    expect(interpreterIdentity("C:\\Tools\\Node.EXE")).toBe("node");
  });

  test("returns stable error codes", () => {
    try {
      parsePositiveInteger("0");
      throw new Error("expected parsePositiveInteger to fail");
    } catch (error) {
      expect(error).toBeInstanceOf(SharedCoreError);
      expect((error as SharedCoreError).code).toBe("invalid_positive_integer");
    }
  });
});
