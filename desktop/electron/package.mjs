#!/usr/bin/env node

import { packager } from "@electron/packager";

import { desktopPackageOptions } from "./config.mjs";

try {
  const appPaths = await packager(desktopPackageOptions());
  for (const appPath of appPaths) console.log(`MECHA_DESKTOP_PACKAGE_READY ${appPath}`);
} catch (error) {
  console.error(`desktop package failed: ${error.message}`);
  process.exitCode = 1;
}
