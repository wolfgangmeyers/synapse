import { test, expect } from "@playwright/test";
import { createServer } from "node:http";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { readFile, rm, stat } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { fixture, assertPayload } from "../fixtures/desktop-package/fixture.mjs";
import { packageDesktop } from "../../desktop/electron/package.mjs";
import { desktopPackageOptions, normalizeRouterUrl } from "../../desktop/electron/config.mjs";

const exec = promisify(execFile);

test("stages only the desktop runtime payload", async () => {
  const f = await fixture();
  try {
    await assertPayload(f);
    for (const arch of ["arm64", "x64"]) {
      const options = desktopPackageOptions({ arch });
      expect(options.arch).toBe(arch);
      expect(options.platform).toBe("darwin");
      expect(options.osxSign).toBe(false);
    }
    expect(normalizeRouterUrl("https://remote.example.test:8765/path")).toBe("https://remote.example.test:8765");
  } finally { await f.cleanup(); }
});

test("packaged entry opens the local HTTP UI with its icon", async ({ page }) => {
  const f = await fixture();
  const server = createServer((_request, response) => {
    response.setHeader("Content-Type", "text/html");
    response.end(`<h1>Local desktop UI</h1><div id="query"></div><script>document.querySelector('#query').textContent = new URLSearchParams(location.search).get('router') + ' / ' + new URLSearchParams(location.search).get('agent')</script>`);
  });
  try {
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const address = server.address();
    if (!address || typeof address === "string") throw new Error("missing HTTP listener");
    await f.stage();
    const metadata = JSON.parse(await readFile(path.join(f.payload, "package.json"), "utf8"));
    const capture = path.join(f.payload, "capture.json");
    await exec(process.execPath, [
      "--loader", pathToFileURL(path.resolve("tests/fixtures/desktop-package/electron-loader.mjs")).href,
      path.join(f.payload, metadata.main), "--router", "http://127.0.0.1:12345",
    ], { timeout: 10_000, env: { ...process.env, MECHA_WEBUI_URL: "", MECHA_WEBUI_PORT: String(address.port), DESKTOP_CAPTURE: capture } });
    const result = JSON.parse(await readFile(capture, "utf8"));
    expect(result.options.webPreferences).toEqual({ contextIsolation: true, nodeIntegration: false, sandbox: true });
    expect(result.options.icon).toBe(path.join(f.payload, "web/router-ui-react/public/mecha-icon.png"));
    expect((await stat(result.options.icon)).size).toBeGreaterThan(0);
    await page.route("**/*", async (route) => {
      if (new URL(route.request().url()).origin === `http://127.0.0.1:${address.port}`) await route.continue();
      else await route.abort();
    });
    await page.goto(result.url);
    await expect(page.getByRole("heading")).toHaveText("Local desktop UI");
    await expect(page.locator("#query")).toHaveText("http://127.0.0.1:12345 / wolfgang");
  } finally {
    server.closeAllConnections();
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
    await f.cleanup();
  }
});

test("rejects missing required payload inputs", async () => {
  for (const missing of [
    "desktop/electron/main.mjs", "desktop/electron/config.mjs", "desktop/electron/assets/Mecha.icns",
    "web/router-ui-react/public/mecha-icon.png", "node_modules/ws", "node_modules/runtime-child",
  ]) {
    const f = await fixture();
    try {
      await rm(path.join(f.source, missing), { recursive: true });
      await expect(f.stage()).rejects.toThrow(/ENOENT|missing desktop runtime dependency/);
    } finally { await f.cleanup(); }
  }
  for (const arch of ["arm64", "x64"]) {
    const f = await fixture();
    try {
      await rm(path.join(f.source, "node_modules/electron"), { recursive: true });
      await expect(packageDesktop({ source: f.source, arch })).rejects.toThrow(/cannot resolve installed Electron version/);
      await expect(stat(f.payload)).rejects.toThrow(/ENOENT/);
    } finally { await f.cleanup(); }
  }
});

test("packaging caller uses the selected payload and cleans up on success and failure", async () => {
  const f = await fixture();
  try {
    for (const arch of ["x64", "arm64"]) for (const fail of [false, true]) {
      const capture = path.join(f.source, `package-${fail}.json`);
      const code = `import { packageDesktop } from ${JSON.stringify(pathToFileURL(path.resolve("desktop/electron/package.mjs")).href)}; await packageDesktop({source: ${JSON.stringify(f.source)}, arch: ${JSON.stringify(arch)}});`;
      const run = exec(process.execPath, [
        "--loader", pathToFileURL(path.resolve("tests/fixtures/desktop-package/packager-loader.mjs")).href,
        "--input-type=module", "--eval", code,
      ], { timeout: 10_000, env: { ...process.env, DESKTOP_CAPTURE: capture, DESKTOP_PACKAGE_FAIL: fail ? "1" : "0" } });
      if (fail) await expect(run).rejects.toThrow(/synthetic packager failure/);
      else await run;
      const result = JSON.parse(await readFile(capture, "utf8"));
      expect(result.options.dir).not.toBe(f.source);
      expect(result.options.arch).toBe(arch);
      expect(result.options.electronVersion).toBe("43.3.0");
      expect(result.options.osxSign).toBe(false);
      expect(result.files).toContain("web/router-ui-react/public/mecha-icon.png");
      expect(result.files).not.toContain("router-proxy/router");
      await expect(stat(result.options.dir)).rejects.toThrow(/ENOENT/);
    }
  } finally { await f.cleanup(); }
});
