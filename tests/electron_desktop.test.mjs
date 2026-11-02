import assert from "node:assert/strict";
import test from "node:test";

import {
  APP_NAME,
  BUNDLE_ID,
  DEFAULT_ROUTER_URL,
  DESKTOP_DEFAULT_AGENT_ID,
  DESKTOP_UI_PORT,
  desktopPackageOptions,
  normalizeRouterUrl,
  parseDesktopArguments,
} from "../desktop/electron/config.mjs";

test("desktop config keeps the Mac app as a web UI client only", () => {
  assert.equal(APP_NAME, "Mecha");
  assert.equal(BUNDLE_ID, "life.meyers.mecha");
  assert.equal(DEFAULT_ROUTER_URL, "http://127.0.0.1:8765");
  assert.equal(DESKTOP_DEFAULT_AGENT_ID, "wolfgang");
  assert.equal(DESKTOP_UI_PORT, 8787);
  assert.equal(parseDesktopArguments([]).router, DEFAULT_ROUTER_URL);
  assert.equal(parseDesktopArguments(["--router", "https://router.example.test:8765/path?debug=1"]).router, "https://router.example.test:8765");
});

test("desktop router argument accepts only credential-free HTTP routers", () => {
  assert.equal(normalizeRouterUrl("http://127.0.0.1:8765/path?x=1"), "http://127.0.0.1:8765");
  for (const routerUrl of [
    "ws://127.0.0.1:8765",
    "file:///tmp/router",
    "javascript:alert(1)",
    "http://user:pass@127.0.0.1:8765",
  ]) {
    assert.throws(() => normalizeRouterUrl(routerUrl), /http|credentials/);
  }
});

test("desktop package options target local unsigned macOS app packaging", () => {
  const options = desktopPackageOptions({ arch: "arm64" });
  assert.equal(options.name, "Mecha");
  assert.equal(options.platform, "darwin");
  assert.equal(options.arch, "arm64");
  assert.equal(options.appBundleId, "life.meyers.mecha");
  assert.equal(options.executableName, "Mecha");
  assert.equal(options.osxSign, false);
  assert.equal(options.prune, true);
  assert.equal(desktopPackageOptions({ arch: "x64" }).arch, "x64");
  assert.equal(options.ignore, undefined);
  assert.throws(() => desktopPackageOptions({ arch: "riscv64" }), /unsupported macOS desktop architecture/);
});

test("desktop staging copies the explicit payload and runtime dependency closure", async () => {
  const { fixture, assertPayload } = await import("./fixtures/desktop-package/fixture.mjs");
  const f = await fixture();
  try { await assertPayload(f); } finally { await f.cleanup(); }
});

test("installed Packager metadata inference accepts the explicit installed Electron version only", async () => {
  const { readFile } = await import("node:fs/promises");
  const { createRequire } = await import("node:module");
  const { pathToFileURL } = await import("node:url");
  const { fixture } = await import("./fixtures/desktop-package/fixture.mjs");
  const { installedElectronVersion } = await import("../desktop/electron/package.mjs");
  const require = createRequire(import.meta.url);
  // Internal metadata-only API of the actually installed Packager. No packager/download call.
  const { getMetadataFromPackageJSON } = await import(new URL("./infer.js", pathToFileURL(require.resolve("@electron/packager"))));
  const installed = JSON.parse(await readFile(require.resolve("electron/package.json"), "utf8"));
  assert.equal(await installedElectronVersion(process.cwd()), installed.version);
  const f = await fixture();
  try {
    await f.stage();
    for (const arch of ["arm64", "x64"]) {
      const options = { ...desktopPackageOptions({ arch }), dir: f.payload };
      await assert.rejects(getMetadataFromPackageJSON(["darwin"], options, f.payload), /dependencies\.electron/);
      const version = await installedElectronVersion(f.source);
      assert.equal(version, "43.3.0");
      await getMetadataFromPackageJSON(["darwin"], { ...options, electronVersion: version }, f.payload);
    }
  } finally { await f.cleanup(); }
});

test("packaging fails honestly before staging when installed Electron is missing or invalid", async () => {
  const { rm, writeFile, stat } = await import("node:fs/promises");
  const { fixture } = await import("./fixtures/desktop-package/fixture.mjs");
  const { packageDesktop } = await import("../desktop/electron/package.mjs");
  for (const arch of ["arm64", "x64"]) for (const version of [undefined, "^43.3.0"]) {
    const f = await fixture();
    try {
      const metadata = `${f.source}/node_modules/electron/package.json`;
      if (version === undefined) await rm(metadata);
      else await writeFile(metadata, JSON.stringify({ name: "electron", version }));
      await assert.rejects(packageDesktop({ source: f.source, arch }), /cannot resolve installed Electron version/);
      await assert.rejects(stat(f.payload), /ENOENT/);
    } finally { await f.cleanup(); }
  }
});
