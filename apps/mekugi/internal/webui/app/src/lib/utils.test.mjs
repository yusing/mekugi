import assert from "node:assert/strict";
import { test } from "node:test";
import { cn } from "./utils.ts";

test("component overrides preserve semantic typography and colors", () => {
  assert.equal(cn("text-sm text-muted-foreground", "text-small text-ink"), "text-small text-ink");
  assert.equal(cn("text-body text-ink", "text-negative"), "text-body text-negative");
  assert.equal(
    cn("rounded-lg shadow-sm", "rounded-control shadow-float"),
    "rounded-control shadow-float",
  );
  assert.equal(cn("bg-surface px-2", [false, "px-3"], { "bg-raised": true }), "px-3 bg-raised");
});
