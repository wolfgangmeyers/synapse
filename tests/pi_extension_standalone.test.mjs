import assert from "node:assert/strict";
import test from "node:test";

import { copyFile, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

test("copied Pi extension loads without repository dependencies", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-extension-"));
  const extension = join(directory, "mecha-messaging.js");
  try {
    await copyFile(new URL("../pi-extensions/mecha-messaging.js", import.meta.url), extension);
    const module = await import(pathToFileURL(extension).href);
    const client = module.createMailboxClient({ agentId: "worker", onEnvelope: () => {} });

    assert.match(client.instanceId, /^[A-Za-z0-9]{21}$/);
    client.close();
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
