import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

// The built UI is embedded into the Go binary from internal/web/dist (go:embed).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(__dirname, "src") } },
  build: { outDir: "../internal/web/dist", emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 300 },
  server: { proxy: { "/api": "http://127.0.0.1:8080", "/v1": "http://127.0.0.1:8080" } },
});
