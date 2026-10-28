import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";
import { fileURLToPath, URL } from "node:url";

// The Go host embeds ../dist. Browser connections go directly to selected connectors.
export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  resolve: { alias: { $lib: fileURLToPath(new URL("./src/lib", import.meta.url)) } },
  // Syntax grammars are separate chunks loaded on first use; the largest (C++)
  // is about 800 kB.
  build: { outDir: "../dist/app", emptyOutDir: true, chunkSizeWarningLimit: 1000 },
});
