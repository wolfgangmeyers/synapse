#!/usr/bin/env node

import { cp, mkdir, mkdtemp, readFile, realpath, rm, stat, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { desktopPackageOptions } from "./config.mjs";

const runtimeFiles = [
  "desktop/electron/main.mjs",
  "desktop/electron/config.mjs",
  "desktop/electron/assets/Mecha.icns",
  "web/router-ui-react/public/mecha-icon.png",
];

// Stage before invoking Electron Packager: repository contents are never its input.
export async function stageDesktopPayload(source, destination) {
  const metadata = JSON.parse(await readFile(path.join(source, "package.json"), "utf8"));
  if (metadata.main !== runtimeFiles[0]) throw new Error(`unexpected desktop entry: ${metadata.main}`);
  if (!metadata.dependencies?.ws) throw new Error("missing desktop dependency: ws");
  await mkdir(destination, { recursive: true });
  for (const relative of runtimeFiles) {
    const from = path.join(source, relative);
    if (!(await stat(from)).isFile()) throw new Error(`required desktop file: ${relative}`);
    await mkdir(path.dirname(path.join(destination, relative)), { recursive: true });
    await cp(from, path.join(destination, relative));
  }
  const { devDependencies, optionalDependencies, peerDependencies, ...runtimeMetadata } = metadata;
  await writeFile(path.join(destination, "package.json"), JSON.stringify({
    ...runtimeMetadata, dependencies: { ws: metadata.dependencies.ws },
  }, null, 2) + "\n");

  async function locate(name, from) {
    // Resolve package directories rather than package.json exports (some packages hide it).
    if (!/^(?:@[\w.-]+\/)?[\w.-]+$/.test(name)) throw new Error(`invalid dependency name: ${name}`);
    let directory = from;
    while (true) {
      const candidate = path.join(directory, "node_modules", name);
      try { return await realpath(candidate); } catch (error) {
        if (error.code !== "ENOENT") throw error;
      }
      const parent = path.dirname(directory);
      if (parent === directory) return undefined;
      directory = parent;
    }
  }

  async function copyDependency(name, from, target, ancestors = new Set(), optional = false) {
    const directory = await locate(name, from);
    if (!directory) {
      if (optional) return;
      throw new Error(`missing desktop runtime dependency: ${name}`);
    }
    // An ancestor already provides cyclic dependencies through Node's normal lookup.
    if (ancestors.has(directory)) return;
    const output = path.join(target, "node_modules", name);
    await mkdir(path.dirname(output), { recursive: true });
    await cp(directory, output, {
      recursive: true,
      dereference: true,
      filter: (entry) => entry !== path.join(directory, "node_modules"),
    });
    const pkg = JSON.parse(await readFile(path.join(directory, "package.json"), "utf8"));
    const next = new Set([...ancestors, directory]);
    const optionalNames = new Set(Object.keys(pkg.optionalDependencies || {}));
    const dependencies = { ...pkg.peerDependencies, ...pkg.dependencies, ...pkg.optionalDependencies };
    for (const dependency of Object.keys(dependencies)) {
      await copyDependency(dependency, directory, output, next,
        optionalNames.has(dependency) || pkg.peerDependenciesMeta?.[dependency]?.optional === true);
    }
  }
  await copyDependency("ws", source, destination);
  return destination;
}

export async function installedElectronVersion(source) {
  try {
    const require = createRequire(path.resolve(source, "package.json"));
    const metadata = JSON.parse(await readFile(require.resolve("electron/package.json"), "utf8"));
    if (metadata.name !== "electron" || typeof metadata.version !== "string" ||
        !/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.test(metadata.version)) {
      throw new Error("expected an exact installed Electron version");
    }
    return metadata.version;
  } catch (error) {
    throw new Error(`cannot resolve installed Electron version for ${source}: ${error.message}`, { cause: error });
  }
}

export async function packageDesktop({ source = process.cwd(), arch = process.arch } = {}) {
  const electronVersion = await installedElectronVersion(source);
  const temporary = await mkdtemp(path.join(tmpdir(), "mecha-desktop-"));
  try {
    const dir = await stageDesktopPayload(source, path.join(temporary, "app"));
    const { packager } = await import("@electron/packager");
    return await packager({ ...desktopPackageOptions({ arch }), dir, electronVersion });
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    for (const appPath of await packageDesktop()) console.log(`MECHA_DESKTOP_PACKAGE_READY ${appPath}`);
  } catch (error) {
    console.error(`desktop package failed: ${error.message}`);
    process.exitCode = 1;
  }
}
