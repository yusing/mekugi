import assert from "node:assert/strict";
import { test } from "node:test";
import { block, referenceContext } from "./markdown.ts";

test("automatic and Markdown web links navigate safely", () => {
  const html = block(
    "https://example.com/auto [Website](https://example.com/markdown) user@example.com",
  );
  assert.match(
    html,
    /href="https:\/\/example.com\/auto" target="_blank" rel="noopener noreferrer"/,
  );
  assert.match(html, /href="https:\/\/example.com\/markdown"[^>]*>Website<\/a>/);
  assert.match(html, /href="mailto:user@example.com"/);
});

test("machine-local Markdown links retain the shared file target", () => {
  const html = block(
    "[Source](</workspace/a b.go:12>) [Relative](src/a%20b.go) [File](file:///workspace/source.go)",
  );
  assert.match(html, /href="#" data-file-link="\/workspace\/a b.go:12"/);
  assert.match(html, /data-file-link="src\/a b.go"/);
  assert.match(html, /data-file-link="file:\/\/\/workspace\/source.go"/);
  assert.doesNotThrow(() => block("[Literal](a%ZZ.md)"));
});

test("untrusted links and images never execute or fetch automatically", () => {
  const html = block(
    "<script>alert(1)</script> [unsafe](javascript:alert(1)) ![Remote](https://example.com/image.png) [fragment](#heading)",
  );
  assert.doesNotMatch(html, /<script>|<img|href="javascript:|data-file-link="#heading"/);
  assert.match(html, /&lt;script&gt;/);
  assert.match(html, /Image: Remote<\/a>/);
});

test("reply sections retain original-document reference links", () => {
  const summary = "Done.\n\n[evidence]: https://example.com/evidence";
  const details = "See [evidence] for the result.";
  const context = referenceContext(`## Summary\n${summary}\n## Details\n${details}`);
  const html = block(details, context);
  assert.match(html, /href="https:\/\/example.com\/evidence"[^>]*>evidence<\/a>/);
  assert.doesNotMatch(html, /Done|<h2>/);
  assert.doesNotMatch(block(details), /<a /);
});
