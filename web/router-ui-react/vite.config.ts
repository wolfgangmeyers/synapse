import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// The Vite process reaches the router directly. MECHA_WEBUI_ROUTER_URL is the
// browser-facing Web UI URL, not an upstream: using it here proxies /v1 back
// into Vite and makes remote managed launches fail.
const routerUrl = process.env.MECHA_ROUTER_URL ?? "http://127.0.0.1:8765";
const publicRouterUrl = process.env.MECHA_WEBUI_ROUTER_URL;
const allowedHosts = (() => {
  if (publicRouterUrl === undefined) return [];
  try { return [new URL(publicRouterUrl).hostname]; } catch { return []; }
})();

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": new URL("./src", import.meta.url).pathname,
    },
  },
  server: {
    host: "0.0.0.0",
    port: 8787,
    strictPort: false,
    allowedHosts: true,
    // Keep browser traffic on the web UI origin so remote clients can use the
    // same API and WebSocket surface without needing direct router access.
    proxy: {
      "/v1": { target: routerUrl, changeOrigin: true, ws: true },
    },
  },
  preview: {
    host: "0.0.0.0",
    port: 8787,
    strictPort: false,
    allowedHosts: true,
  },
  test: {
    environment: "jsdom",
    globals: false,
    include: ["tests/**/*.test.ts", "tests/**/*.test.tsx", "src/**/*.test.ts", "src/**/*.test.tsx"],
    testTimeout: 5000,
    hookTimeout: 5000,
  },
});
