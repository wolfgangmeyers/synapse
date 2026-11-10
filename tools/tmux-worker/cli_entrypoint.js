import { realpathSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

function canonicalPath(path) {
  try {
    return realpathSync(path);
  } catch {
    return resolve(path);
  }
}

/**
 * True when `moduleUrl` names the module the process was started with.
 *
 * Canonicalize both sides: the loader resolves symlinks in `import.meta.url` but not in
 * `process.argv[1]`, so a launch through a symlinked directory compares unequal on
 * spelling alone and the module silently declines to run its CLI.
 *
 * `moduleUrl` is required — a shared helper cannot default to the caller's
 * `import.meta.url`. Call it as `isCliEntrypoint(import.meta.url)`.
 */
export function isCliEntrypoint(moduleUrl, argvPath = process.argv[1]) {
  if (typeof moduleUrl !== "string" || moduleUrl === "") {
    throw new TypeError("moduleUrl is required");
  }
  if (typeof argvPath !== "string" || argvPath === "") return false;
  return canonicalPath(argvPath) === canonicalPath(fileURLToPath(moduleUrl));
}
