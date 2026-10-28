import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { registerHooks, stripTypeScriptTypes } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, test } from "node:test";
import { pathToFileURL } from "node:url";
import { compile, compileModule } from "svelte/compiler";
import { render } from "svelte/server";

const fixtures = pathToFileURL(mkdtempSync(join(tmpdir(), "mekugi-button-"))).href;
after(() => rmSync(new URL(fixtures), { recursive: true, force: true }));

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier.startsWith("$lib/"))
      specifier = new URL(specifier.slice(5), import.meta.url).href;
    if (specifier.startsWith(".") || specifier.startsWith("file:")) {
      const url = new URL(specifier, context.parentURL);
      if (url.pathname.endsWith(".js") && !existsSync(url)) {
        const source = new URL(url.href.replace(/\.js$/, ".ts"));
        if (existsSync(source)) specifier = source.href;
      } else if (!/\.(?:ts|mjs|js|svelte)$/.test(url.pathname)) {
        const index = new URL(`${url.href}/index.ts`);
        const source = new URL(`${url.href}.ts`);
        if (existsSync(index)) specifier = index.href;
        else if (existsSync(source)) specifier = source.href;
      }
    }
    // The generated fixture lives outside the app; packages resolve from here.
    if (context.parentURL?.startsWith(fixtures) && !/^(?:\.|\/|[a-z]+:)/.test(specifier))
      return nextResolve(specifier, { ...context, parentURL: import.meta.url });
    return nextResolve(specifier, context);
  },
  load(url, context, nextLoad) {
    if (url.endsWith(".svelte") || /\.svelte\.(?:ts|js)$/.test(url)) {
      const source = readFileSync(new URL(url), "utf8");
      const options = { filename: new URL(url).pathname, generate: "server" };
      const result = url.endsWith(".svelte")
        ? compile(source, options)
        : compileModule(url.endsWith(".ts") ? stripTypeScriptTypes(source) : source, options);
      return { format: "module", source: result.js.code, shortCircuit: true };
    }
    if (url.endsWith(".ts") && url.startsWith("file:")) {
      const source = stripTypeScriptTypes(readFileSync(new URL(url), "utf8"));
      return { format: "module", source, shortCircuit: true };
    }
    return nextLoad(url, context);
  },
});

// A tooltip wraps the button without changing what it submits or is called.
test("tooltip buttons keep their form type, identity and accessible name", async () => {
  const button = new URL("./ui/Button.svelte", import.meta.url).href;
  const tooltip = new URL("./ui/tooltip/index.ts", import.meta.url).href;
  const fixture = new URL(`${fixtures}/Fixture.svelte`);
  writeFileSync(
    fixture,
    `<script>
  import Button from ${JSON.stringify(button)};
  import * as Tooltip from ${JSON.stringify(tooltip)};
</script>
<Tooltip.Provider>
  <Button id="submit-check" type="submit" tooltip icon="send" label="Send check" />
  <Button id="reset-check" type="reset" tooltip label="Reset check">Reset</Button>
  <Button id="plain-check" tooltip icon="close" label="Close check" />
</Tooltip.Provider>
`,
  );
  const { default: Fixture } = await import(fixture.href);
  const html = render(Fixture).body;
  const tag = (id) => html.match(new RegExp(`<button[^>]*\\bid="${id}"[^>]*>`))?.[0] ?? "";
  for (const [id, type, label] of [
    ["submit-check", "submit", "Send check"],
    ["reset-check", "reset", "Reset check"],
    ["plain-check", "button", "Close check"],
  ]) {
    const element = tag(id);
    assert.ok(element, `${id} renders one button carrying its id`);
    assert.match(element, new RegExp(`\\btype="${type}"`));
    assert.match(element, new RegExp(`\\baria-label="${label}"`));
    assert.doesNotMatch(element, /\btitle=/);
  }
});
