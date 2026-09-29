import { defineConfig } from "astro/config";

// The Go server sends `Content-Security-Policy: default-src 'self'`, so the
// build must never inline scripts or styles.
const apiTarget = process.env.NEXO_API_URL ?? "http://localhost:8080";

export default defineConfig({
  output: "static",
  build: { inlineStylesheets: "never" },
  vite: {
    build: { assetsInlineLimit: 0 },
    server: {
      proxy: {
        "/api": apiTarget,
        "/health": apiTarget,
      },
    },
  },
});
