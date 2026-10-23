import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

// The Go host embeds ../dist. Browser connections go directly to selected connectors.
export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  // Syntax grammars are separate chunks loaded on first use; the largest (C++)
  // is about 800 kB.
  build: { outDir: "../dist/app", emptyOutDir: true, chunkSizeWarningLimit: 1000 },
});
