import { defineConfig } from "astro/config";

// The Go server sends `Content-Security-Policy: default-src 'self'`, so the
// build must never inline scripts or styles.
const apiTarget = process.env.MILO_API_URL ?? "http://localhost:8080";

export default defineConfig({
  output: "static",
  build: { inlineStylesheets: "never" },
  vite: {
    build: { assetsInlineLimit: 0 },
    server: {
      proxy: {
        // The server refuses requests whose Origin differs from its Host. Behind this dev proxy the
        // browser's Origin is the dev server's address, so it is rewritten to match the Host the proxy sends.
        "/api": {
          target: apiTarget,
          configure: (proxy) => {
            proxy.on("proxyReq", (req) => {
              if (req.getHeader("origin")) req.setHeader("origin", `http://${req.getHeader("host")}`);
            });
          },
        },
        "/health": apiTarget,
      },
    },
  },
});
