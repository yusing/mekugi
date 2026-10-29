import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const app = new URL("../../", import.meta.url);
const manifest = JSON.parse(readFileSync(new URL("public/manifest.webmanifest", app), "utf8"));

test("installation launches the existing app with stable identity and standalone display", () => {
  const index = readFileSync(new URL("index.html", app), "utf8");
  assert.match(index, /<link rel="manifest" href="\/manifest.webmanifest"/);
  assert.equal(manifest.name, "Mekugi");
  assert.equal(manifest.id, "/");
  assert.equal(manifest.start_url, "/");
  assert.equal(manifest.scope, "/");
  assert.equal(manifest.display, "standalone");
  assert.notEqual(manifest.prefer_related_applications, true);
});

test("install and Apple touch icons have the advertised PNG dimensions", () => {
  assert.deepEqual(
    manifest.icons.map((icon) => icon.sizes),
    ["192x192", "512x512"],
  );
  for (const icon of manifest.icons) {
    assert.equal(icon.type, "image/png");
    const png = readFileSync(new URL(`public${icon.src}`, app));
    assert.equal(`${png.readUInt32BE(16)}x${png.readUInt32BE(20)}`, icon.sizes);
  }
  const index = readFileSync(new URL("index.html", app), "utf8");
  const favicon = index.match(/<link rel="icon" type="image\/png" sizes="64x64" href="([^"]+)"/);
  assert.ok(favicon);
  const faviconPNG = readFileSync(new URL(`public${favicon[1]}`, app));
  assert.equal(faviconPNG.readUInt32BE(16), 64);
  assert.equal(faviconPNG.readUInt32BE(20), 64);
  const touch = index.match(/<link rel="apple-touch-icon" href="([^"]+)"/);
  assert.ok(touch);
  const png = readFileSync(new URL(`public${touch[1]}`, app));
  assert.equal(png.readUInt32BE(16), 180);
  assert.equal(png.readUInt32BE(20), 180);
});
