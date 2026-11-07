import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { writeFileSync } from "node:fs";

// In development the API runs on :8080 (`make run`).
export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    {
      // dist/.gitkeep keeps `go:embed all:dist` compiling without a frontend build.
      name: "keep-gitkeep",
      closeBundle() {
        writeFileSync("dist/.gitkeep", "");
      },
    },
  ],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 900,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes("@xterm")) return "xterm";
          if (id.includes("@novnc")) return "novnc";
          if (id.includes("node_modules/react") || id.includes("react-router") || id.includes("@tanstack")) return "vendor";
        },
      },
    },
  },
  server: {
    proxy: {
      "/api": { target: "http://localhost:8080", ws: true },
    },
  },
});
