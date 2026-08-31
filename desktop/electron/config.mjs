import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";

export const APP_NAME = "Mecha";
export const BUNDLE_ID = "life.meyers.mecha";
export const DEFAULT_ROUTER_URL = "http://127.0.0.1:8765";
export const DESKTOP_DEFAULT_AGENT_ID = "wolfgang";
export const DESKTOP_UI_PORT = 8787;
export const WINDOW_WIDTH = 1180;
export const WINDOW_HEIGHT = 820;

export const iconPngPath = fileURLToPath(new URL("../../web/router-ui-react/public/mecha-icon.png", import.meta.url));
export const iconIcnsPath = fileURLToPath(new URL("./assets/Mecha.icns", import.meta.url));

export function parseDesktopArguments(argv = []) {
  const options = { router: process.env.MECHA_ROUTER_URL || DEFAULT_ROUTER_URL };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === "--router") {
      const value = argv[index + 1];
      if (value === undefined || value.startsWith("--")) throw new Error("--router requires a value");
      options.router = normalizeRouterUrl(value);
      index += 1;
      continue;
    }
    if (argument === "--help") return { help: true };
  }
  options.router = normalizeRouterUrl(options.router);
  return options;
}

export function normalizeRouterUrl(value) {
  const url = new URL(String(value));
  if (!["http:", "https:"].includes(url.protocol)) throw new Error("router URL must start with http:// or https://");
  if (url.hostname === "") throw new Error("router URL must include a host");
  if (url.username !== "" || url.password !== "") throw new Error("router URL must not include credentials");
  url.pathname = "";
  url.search = "";
  url.hash = "";
  return url.toString().replace(/\/$/, "");
}

export function desktopPackageOptions({ arch = process.arch } = {}) {
  return {
    dir: ".",
    name: APP_NAME,
    platform: "darwin",
    arch: electronArch(arch),
    out: "dist/desktop",
    overwrite: true,
    appBundleId: BUNDLE_ID,
    appVersion: "0.1.0",
    executableName: APP_NAME,
    icon: existsSync(iconIcnsPath) ? iconIcnsPath.replace(/\.icns$/, "") : undefined,
    osxSign: false,
    prune: true,
    ignore: [
      /^\/\.git(?:\/|$)/,
      /^\/\.worktrees(?:\/|$)/,
      /^\/\.claude(?:\/|$)/,
      /^\/\.gradle(?:\/|$)/,
      /^\/\.kotlin(?:\/|$)/,
      /^\/\.pi(?:\/|$)/,
      /^\/\.pytest_cache(?:\/|$)/,
      /^\/\.ruff_cache(?:\/|$)/,
      /^\/dist(?:\/|$)/,
      /^\/node_modules\/(?!ws(?:\/|$))/,
      /^\/app(?:\/|$)/,
      /^\/discord-gateway-bridge(?:\/|$)/,
      /^\/docs(?:\/|$)/,
      /^\/gradle(?:\/|$)/,
      /^\/harness(?:\/|$)/,
      /^\/personas(?:\/|$)/,
      /^\/pi-extensions(?:\/|$)/,
      /^\/protocol(?:\/|$)/,
      /^\/tests(?:\/|$)/,
      /^\/tools\//,
      /^\/web\/router-ui-react(?:\/|$)/,
      /^\/[^/]+\.md$/,
      /^\/build\.gradle\.kts$/,
      /^\/package-lock\.json$/,
      /^\/gradle\.properties$/,
      /^\/gradlew(?:\.bat)?$/,
      /^\/local\.properties$/,
      /^\/settings\.gradle\.kts$/,
    ],
  };
}

function electronArch(arch) {
  if (arch === "x64" || arch === "arm64") return arch;
  throw new Error(`unsupported macOS desktop architecture: ${arch}`);
}
