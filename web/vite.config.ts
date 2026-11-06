import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { writeFileSync } from "node:fs";

// In development the API runs on :8080 (`make run`).
export default defineConfig({
  plugins: [
    react(),
    {
      // dist/.gitkeep keeps `go:embed all:dist` compiling without a frontend build.
      name: "keep-gitkeep",
      closeBundle() {
        writeFileSync("dist/.gitkeep", "");
      },
    },
  ],
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    proxy: {
      "/api": { target: "http://localhost:8080", ws: true },
    },
  },
});
