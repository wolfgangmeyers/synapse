import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

describe("Vite development proxy", () => {
  it("gives the Web UI router override precedence for the proxy target and allowed host", async () => {
    const source = await readFile(resolve(process.cwd(), "vite.config.ts"), "utf8");

    expect(source).toContain("const routerUrl = process.env.MECHA_WEBUI_ROUTER_URL ?? process.env.MECHA_ROUTER_URL ??");
    expect(source).toContain("const publicRouterUrl = process.env.MECHA_WEBUI_ROUTER_URL;");
    expect(source).toContain('"/v1": { target: routerUrl, changeOrigin: true, ws: true }');
  });
});
