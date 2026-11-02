import assert from "node:assert/strict";
import { cp, mkdir, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { stageDesktopPayload } from "../../../desktop/electron/package.mjs";

const root = fileURLToPath(new URL("../../../", import.meta.url));
export async function fixture() {
  const temporary = await mkdtemp(path.join(tmpdir(), "desktop-payload-test-"));
  const source = path.join(temporary, "source");
  const payload = path.join(temporary, "payload");
  async function put(relative, content) {
    const target = path.join(source, relative);
    await mkdir(path.dirname(target), { recursive: true });
    await writeFile(target, content);
  }
  try {
    for (const relative of ["desktop/electron/main.mjs", "desktop/electron/config.mjs"]) {
      await put(relative, await readFile(path.join(root, relative)));
    }
    for (const relative of ["desktop/electron/assets/Mecha.icns", "web/router-ui-react/public/mecha-icon.png"]) {
      await mkdir(path.dirname(path.join(source, relative)), { recursive: true });
      await cp(path.join(root, relative), path.join(source, relative));
    }
    await put("package.json", JSON.stringify({
      name: "desktop-fixture", version: "1.0.0", type: "module", main: "desktop/electron/main.mjs",
      dependencies: { ws: "1.0.0", ajv: "1", nanoid: "1", unrelated: "1" },
      devDependencies: { electron: "^43.0.0", "@electron/packager": "1" },
    }));
    await put("node_modules/electron/package.json", JSON.stringify({ name: "electron", version: "43.3.0" }));
    await put("node_modules/ws/package.json", JSON.stringify({ name: "ws", version: "1.0.0", dependencies: { "runtime-child": "1" }, peerDependencies: { "optional-peer": "1" }, peerDependenciesMeta: { "optional-peer": { optional: true } } }));
    await put("node_modules/ws/index.js", "module.exports = require('runtime-child');\n");
    await put("node_modules/runtime-child/package.json", JSON.stringify({ name: "runtime-child", version: "1.0.0", dependencies: { "runtime-leaf": "1" } }));
    await put("node_modules/runtime-child/index.js", "module.exports = require('runtime-leaf');\n");
    await put("node_modules/runtime-child/node_modules/runtime-leaf/package.json", JSON.stringify({ name: "runtime-leaf", version: "1.0.0" }));
    await put("node_modules/runtime-child/node_modules/runtime-leaf/index.js", "module.exports = 'runtime closure retained';\n");
    for (const relative of [
      "router-proxy/router", "router/main.mjs", "auth-helper/main.mjs", ".env", ".git/config",
      "dist/desktop/old.app/file", "web/router-ui-react/dist/index.html", "desktop/electron/.secret",
      "node_modules/ws/node_modules/dev-only/index.js", "node_modules/electron/index.js",
      "node_modules/ajv/index.js", "node_modules/nanoid/index.js", "node_modules/unrelated/index.js",
      "tests/private.json", "future-backend/secret", "desktop/electron/package.mjs",
    ]) await put(relative, "must not ship");
    return { source, payload, stage: () => stageDesktopPayload(source, payload), cleanup: () => rm(temporary, { recursive: true, force: true }) };
  } catch (error) {
    await rm(temporary, { recursive: true, force: true });
    throw error;
  }
}

export async function files(directory, prefix = "") {
  const result = [];
  for (const entry of await readdir(path.join(directory, prefix), { withFileTypes: true })) {
    const relative = path.posix.join(prefix, entry.name);
    if (entry.isDirectory()) result.push(...await files(directory, relative));
    else result.push(relative);
  }
  return result.sort();
}

export async function assertPayload(f) {
  const before = await readFile(path.join(f.source, "package.json"), "utf8");
  await f.stage();
  assert.deepEqual(await files(f.payload), [
    "desktop/electron/assets/Mecha.icns", "desktop/electron/config.mjs", "desktop/electron/main.mjs",
    "node_modules/ws/index.js", "node_modules/ws/package.json",
    "node_modules/ws/node_modules/runtime-child/index.js", "node_modules/ws/node_modules/runtime-child/package.json",
    "node_modules/ws/node_modules/runtime-child/node_modules/runtime-leaf/index.js",
    "node_modules/ws/node_modules/runtime-child/node_modules/runtime-leaf/package.json",
    "package.json", "web/router-ui-react/public/mecha-icon.png",
  ].sort());
  const pkg = JSON.parse(await readFile(path.join(f.payload, "package.json"), "utf8"));
  assert.equal(pkg.main, "desktop/electron/main.mjs");
  assert.deepEqual(pkg.dependencies, { ws: "1.0.0" });
  assert.equal(pkg.devDependencies, undefined);
  assert.equal(await readFile(path.join(f.source, "package.json"), "utf8"), before);
  const { createRequire } = await import("node:module");
  assert.equal(createRequire(path.join(f.payload, "package.json"))("ws"), "runtime closure retained");
}
