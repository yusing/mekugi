import {expect, test} from "bun:test";
import {encode} from "../../../../plugins/node_modules/gpt-tokenizer/esm/model/gpt-5.js";
import {countGPT5Tokens, encodeGPT5, MAX_POSSIBLE_GPT5_TOKEN_BYTES, tokenBytes} from "../../../../plugins/tokens.ts";
import {formatMRunOutput, selectMRunText} from "../../../../plugins/mrun.ts";

const ordinary = {disallowedSpecial: new Set<string>()};

test("heap merges preserve the pinned tokenizer's exact token identities", () => {
  for (const value of [
    "a".repeat(4100), " ".repeat(4100), "ababcd".repeat(800),
    "🙂".repeat(1100), "中".repeat(1500), "aAb".repeat(1500),
    "'s ".repeat(2000), "first\n" + " ".repeat(4200) + "next",
    (" \r\n\t <|endoftext|> café A's 12345 🙂\n").repeat(200),
  ]) {
    expect(encodeGPT5(value)).toEqual(encode(value, ordinary));
  }
  let state = 92713;
  const alphabet = "abcABC\n\r\t 0123'🙂é中";
  for (let trial = 0; trial < 25; trial += 1) {
    const chunks: string[] = [];
    const symbols = Array.from(alphabet);
    for (let index = 0; index < 5000; index += 1) {
      state = (Math.imul(state, 1664525) + 1013904223) >>> 0;
      chunks.push(symbols[state % symbols.length]);
    }
    const value = chunks.join("");
    expect(encodeGPT5(value)).toEqual(encode(value, ordinary));
  }
});

test("long single pieces stay practical at the maximum retained byte window", () => {
  const length = 15_500 * MAX_POSSIBLE_GPT5_TOKEN_BYTES;
  expect(countGPT5Tokens(" ".repeat(length))).toBe(15_500);
  expect(countGPT5Tokens("a".repeat(length))).toBe(length / 8);
}, 15_000);

test("mrun token selection preserves Unicode and a strict independently counted ceiling", () => {
  for (const tail of [false, true]) {
    for (const budget of [0, 1, 2, 7, 42, 100]) {
      for (const value of [
        "", "short\n", "first 🙂 café 中文 👨‍👩‍👧‍👦\n".repeat(30),
        " ".repeat(5000), "<|endoftext|>\r\n",
        "\uFEFFhello world more words", "before\uFEFFhello world", "\uFEFF\uFEFF",
      ]) {
        const result = selectMRunText(value, budget, tail);
        expect(countGPT5Tokens(result.text)).toBe(result.tokens);
        expect(result.tokens).toBeLessThanOrEqual(budget);
        expect(tail ? value.endsWith(result.text) : value.startsWith(result.text)).toBe(true);
        const decoded = Buffer.concat(encodeGPT5(result.text).map(tokenBytes)).toString("utf8");
        expect(decoded).toBe(result.text);
      }
    }
  }
});

test("mrun formatting caps stderr at half the budget when stdout is present", () => {
  const tiny = formatMRunOutput(["1", "tail", "stdout", "first error"]);
  expect(tiny.stdout).not.toBe("");
  expect(tiny.stderr).toBe("");
  expect(countGPT5Tokens(tiny.stdout ?? "") + countGPT5Tokens(tiny.stderr ?? "")).toBeLessThanOrEqual(1);

  for (const budget of [2, 7, 42]) {
    const result = formatMRunOutput([
      String(budget), "head", "out ".repeat(200), "err ".repeat(200),
    ]);
    const stdoutTokens = countGPT5Tokens(result.stdout ?? "");
    const stderrTokens = countGPT5Tokens(result.stderr ?? "");
    expect(stdoutTokens).toBeGreaterThan(0);
    expect(stderrTokens).toBeLessThanOrEqual(Math.floor(budget / 2));
    expect(stdoutTokens + stderrTokens).toBeLessThanOrEqual(budget);
  }

  for (const argv of [
    [], ["0", "head", "", ""], ["15501", "head", "", ""],
    ["01", "head", "", ""], ["1", "unknown", "", ""],
  ]) {
    expect(() => formatMRunOutput(argv)).toThrow("invalid mrun output selection");
  }
});

test("shell selection budgets nested JSON framing and preserves complete row prefixes", () => {
  const value = '"path\\\\name":1:abcd "\t🙂 café 中文"\r\n'.repeat(5000);
  for (const budget of [1, 20, 123, 256, 1600, 8976]) {
    const result = formatMRunOutput([String(budget), "shell", value, ""]);
    const selected = JSON.parse(result.stdout!);
    expect(value.startsWith(selected.text)).toBe(true);
    expect(selected.text === "" || selected.text.endsWith("\n")).toBe(true);
    const framed = selected.text === "" ? 0 : countGPT5Tokens(JSON.stringify(JSON.stringify(selected.text)));
    expect(selected.tokens).toBe(framed);
    expect(framed).toBeLessThanOrEqual(budget);
  }
});
