import { afterEach, describe, expect, it } from "vitest";
import {
  EXPECTED_CAPABILITIES,
  EXPECTED_GOAL_MIGRATIONS,
  REPOSITORY_ROOT,
  fingerprintTaskResources,
  isLoopbackPortAvailable,
  startIsolatedGoRouter,
} from "../support/isolated-go-router.mjs";

const routers: Array<{ close: () => Promise<{ rootRemoved?: boolean; logText?: string }> }> = [];

function stringArray(value: unknown): string[] {
  if (!Array.isArray(value)) throw new Error("expected a string array");
  const strings: string[] = [];
  for (const entry of value) {
    if (typeof entry !== "string") throw new Error("expected a string array");
    strings.push(entry);
  }
  return strings;
}

afterEach(async () => {
  while (routers.length > 0) await routers.pop()?.close();
});

describe("isolated Go router fixture", () => {
  it("uses only task-owned database, log, attachments, and loopback port", async () => {
    const router = await startIsolatedGoRouter();
    routers.push(router);
    const taskFingerprint = await fingerprintTaskResources(router);
    expect(router.rootPath).toContain("mecha-go-router-");
    expect(router.databasePath.startsWith(router.rootPath)).toBe(true);
    expect(router.logPath.startsWith(router.rootPath)).toBe(true);
    expect(router.attachmentsPath.startsWith(router.rootPath)).toBe(true);
    expect(router.port).not.toBe(8765);
    expect(router.cwd).toBe(REPOSITORY_ROOT);
    expect(router.pid).toBeGreaterThan(0);
    expect(taskFingerprint.files.some((file) => file.path.endsWith("go-router.db"))).toBe(true);
    expect(taskFingerprint.files.some((file) => file.path.endsWith("go-router.jsonl"))).toBe(true);
    expect(taskFingerprint.attachmentsDirectory.exists).toBe(true);
    expect(stringArray(router.health.schema_migrations)).toEqual(expect.arrayContaining([...EXPECTED_GOAL_MIGRATIONS]));
    expect(EXPECTED_CAPABILITIES.every((capability) => router.capabilities[capability] === true)).toBe(true);
    // This fingerprint covers only the temporary router started by this test.
    // Live-router before/after evidence belongs to the opt-in BCB run.
    expect(taskFingerprint.rootPath).toBe(router.rootPath);
    expect(taskFingerprint.files).toEqual(expect.arrayContaining([
      expect.objectContaining({ path: "go-router.db" }),
      expect.objectContaining({ path: "go-router.jsonl" }),
    ]));
  }, 60_000);

  it("stops its child and removes only the temporary root", async () => {
    const router = await startIsolatedGoRouter();
    routers.push(router);
    const port = router.port;
    const result = await router.close();
    routers.splice(routers.indexOf(router), 1);
    expect(result.rootRemoved).toBe(true);
    expect(result.logText).toContain('"event":"router_stopped"');
    expect(await isLoopbackPortAvailable(port)).toBe(true);
  }, 60_000);
});
