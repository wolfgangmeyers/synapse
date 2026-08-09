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
  assert.equal(DESKTOP_UI_PORT, 8788);
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
  assert.ok(options.ignore.some((pattern) => pattern.test("/.worktrees/router-delivery-batching/.git")));
  assert.ok(options.ignore.some((pattern) => pattern.test("/dist/desktop")));
  assert.ok(options.ignore.some((pattern) => pattern.test("/app/src/main/java/life/meyers/mecha/MainActivity.kt")));
  assert.ok(options.ignore.some((pattern) => pattern.test("/discord-gateway-bridge/main.mjs")));
  assert.ok(options.ignore.some((pattern) => pattern.test("/personas/wolfgang.md")));
  assert.ok(options.ignore.some((pattern) => pattern.test("/.gradle/caches/modules-2")));
  assert.ok(options.ignore.some((pattern) => pattern.test("/node_modules/electron/index.js")));
  assert.ok(options.ignore.every((pattern) => !pattern.test("/node_modules/ws/index.js")));
  assert.ok(options.ignore.every((pattern) => !pattern.test("/tools/router_web_ui.mjs")));
  assert.ok(options.ignore.every((pattern) => !pattern.test("/web/router-ui/app.js")));
  assert.throws(() => desktopPackageOptions({ arch: "riscv64" }), /unsupported macOS desktop architecture/);
});
