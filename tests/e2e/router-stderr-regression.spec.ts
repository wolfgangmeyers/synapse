import { test, expect } from "@playwright/test";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { startIsolatedRouter } from "./harness";

test("router stderr is captured without exposing secrets", async () => {
  const dir = await mkdtemp(join(tmpdir(), "router-stderr-fixture-"));
  const executable = join(dir, "router");
  await writeFile(executable, `#!/bin/sh\nprintf 'credential-super-secret managed provider status is unavailable\n' >&2\nprintf 'MECHA_V2_ROUTER_READY\n'\nexec sleep 60\n`, { mode: 0o700 });
  try {
    const router = await startIsolatedRouter({ repoRoot: process.cwd(), routerExecutableFixture: executable });
    try {
      await expect.poll(() => router.readinessStderr()).toBe("managed provider status is unavailable");
      expect(router.stderr()).toBe("managed provider status is unavailable");
      expect(router.stderr()).not.toContain("credential-super-secret");
      expect(router.readinessStderr()).not.toContain("credential-super-secret");
      expect(router.countStderr("credential-super-secret")).toBe(1);
      expect(router.stderrLineContains("credential-super-secret", "managed provider status")).toBe(true);
    } finally { await router.close(); }
  } finally { await rm(dir, { recursive: true, force: true }); }
});
