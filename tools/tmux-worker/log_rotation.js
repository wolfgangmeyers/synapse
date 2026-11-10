import { renameSync, rmSync, statSync } from "node:fs";

/**
 * Size-capped log rotation, shared by every append-only log in the harness.
 *
 * When the live file reaches `maxBytes` it is renamed to `<logPath>.1` and the
 * previous `.1` is discarded, so a log costs at most two generations on disk. The
 * size is checked before the caller's append, so each generation can exceed
 * `maxBytes` by its final record.
 *
 * Returns true when a rotation happened. Throws if the rotation cannot complete —
 * callers decide whether that should cost the pending record. `harness_spec.md` 15.1
 * ("Logs must rotate") is the requirement, and documents each log's limit.
 */
export function rotateSizeCappedLog(logPath, maxBytes) {
  if (typeof logPath !== "string" || logPath.length === 0) throw new TypeError("logPath is required");
  if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) throw new TypeError("maxBytes must be a positive safe integer");
  try {
    if (statSync(logPath).size < maxBytes) return false;
  } catch (error) {
    if (error?.code === "ENOENT") return false;
    throw error;
  }
  rmSync(`${logPath}.1`, { force: true });
  renameSync(logPath, `${logPath}.1`);
  return true;
}
