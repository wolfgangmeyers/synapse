// @vitest-environment node

import { resolve } from "node:path";
import { loadConfigFromFile } from "vite";
import { describe, expect, it } from "vitest";

function restoreEnvironment(name: string, value: string | undefined): void {
  if (value === undefined) delete process.env[name];
  else process.env[name] = value;
}

describe("Vite development proxy", () => {
  it("gives the Web UI router override precedence for the proxy target and allowed host", async () => {
    const previousWebUiUrl = process.env.MECHA_WEBUI_ROUTER_URL;
    const previousRouterUrl = process.env.MECHA_ROUTER_URL;
    const webUiUrl = "https://preview.example.test:8765";
    process.env.MECHA_WEBUI_ROUTER_URL = webUiUrl;
    process.env.MECHA_ROUTER_URL = "http://127.0.0.1:9876";

    try {
      const loaded = await loadConfigFromFile({ command: "serve", mode: "test" }, resolve(process.cwd(), "vite.config.ts"));
      expect(loaded?.config.server?.proxy).toMatchObject({ "/v1": { target: webUiUrl, changeOrigin: true, ws: true } });
      expect(loaded?.config.server?.allowedHosts).toEqual(["preview.example.test"]);
    } finally {
      restoreEnvironment("MECHA_WEBUI_ROUTER_URL", previousWebUiUrl);
      restoreEnvironment("MECHA_ROUTER_URL", previousRouterUrl);
    }
  });
});
