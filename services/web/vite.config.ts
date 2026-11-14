import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const gen = fileURLToPath(new URL("../../gen/ts/trenddetective", import.meta.url));
const repoRoot = fileURLToPath(new URL("../..", import.meta.url));

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { "@gen": gen },
  },
  // MapLibre's worker is an ES module with imports of its own.
  worker: { format: "es" },
  build: {
    // MapLibre alone is about 1 MB. It loads lazily in its own chunk, so the
    // page itself stays small; the limit sits just above the map's chunk.
    chunkSizeWarningLimit: 1200,
  },
  server: {
    port: 5173,
    strictPort: true,
    fs: { allow: [repoRoot] },
  },
});
