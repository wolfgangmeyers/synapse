import { test, expect } from "@playwright/test";
import { mkdtemp, writeFile, chmod, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { buildGo } from "./harness";

test("build failures and timeouts never expose compiler stderr", async () => {
  test.skip(process.platform === "win32", "fixture uses a POSIX shell");
  const dir = await mkdtemp(join(tmpdir(), "build-redaction-"));
  const originalPath = process.env.PATH;
  const marker = "PRIVATE_BUILD_STDERR_SENTINEL";
  try {
    const go = join(dir, "go");
    await writeFile(go, `#!/bin/sh\necho ${marker} >&2\nif [ "$1" = timeout ]; then exec sleep 10; fi\nexit 23\n`);
    await chmod(go, 0o755);
    process.env.PATH = `${dir}:${originalPath ?? ""}`;
    for (const [mode, timeout, expected] of [["failure", 5000, /build failed \(23\)/], ["timeout", 200, /build timed out after 200ms/]] as const) {
      let message = "";
      try { await buildGo([mode], dir, "router", timeout); } catch (error) { message = String(error); }
      expect(message).toMatch(expected);
      expect(message).not.toContain(marker);
    }
  } finally {
    if (originalPath === undefined) delete process.env.PATH; else process.env.PATH = originalPath;
    await rm(dir, { recursive: true, force: true });
  }
});
