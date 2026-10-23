import assert from "node:assert/strict";
import { test } from "node:test";
import { markedBlock, toolLabel } from "./markdown.ts";
import { hasDetails, reasoningBody } from "./kinds.ts";

test("only bound UTF-8 spans become attachment markers", () => {
  const text = "圖 [Image 1] and [Image 1] <script>";
  const html = markedBlock(text, [{ Start: 4, End: 13, Kind: 0 }]);
  assert.equal((html.match(/text-marker-image/g) ?? []).length, 1);
  assert.match(html, /and \[Image 1\] &lt;script&gt;/);
  assert.match(html, /圖 <span[^>]+>\[Image 1\]<\/span>/);
  assert.doesNotMatch(markedBlock(text, [{ Start: 1, End: 13, Kind: 0 }]), /text-marker/);
});

test("bound markers remain escaped inside code and emphasis", () => {
  assert.match(
    markedBlock("`<b>`", [{ Start: 1, End: 4, Kind: 1 }]),
    /<code><span[^>]+>&lt;b&gt;<\/span><\/code>/,
  );
  assert.match(markedBlock("**[Image 1]**", [{ Start: 2, End: 11, Kind: 0 }]), /<strong><span/);
});

test("empty reasoning and output have no details", () => {
  assert.equal(reasoningBody("**Checking**\n\n", "Checking"), "");
  assert.equal(
    hasDetails({ kind: "reasoning", values: [{ label: "Checking", body: "**Checking**" }] }),
    false,
  );
  assert.equal(hasDetails({ kind: "tool", values: [{ tail: ["", " "] }] }), false);
  assert.equal(
    hasDetails({
      kind: "reasoning",
      values: [{ label: "Checking", body: "**Checking**\nEvidence" }],
    }),
    true,
  );
});

test("search patterns and paths retain distinct operands", () => {
  const html = toolLabel("`a|b, c` in `src/a.go` `src/b.go`", "Search");
  assert.match(html, /<code class="text-accent">a\|b, c<\/code>/);
  assert.match(html, /in <code class="tool-operand">src\/a.go<\/code>/);
  assert.equal(
    toolLabel("`cat`, `python3`", "Run"),
    '<code class="tool-operand">cat</code> <code class="tool-operand">python3</code>',
  );
  assert.match(toolLabel("`printf 'a, b'`", "Run"), /a, b/);
});
