import assert from "node:assert/strict";
import test from "node:test";

import { copyFile, mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

test("copied Pi extension loads without repository dependencies", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-extension-"));
  try {
    const extensionDirectory = join(directory, "pi-extensions");
    await mkdir(extensionDirectory);
    const extension = join(extensionDirectory, "mecha-messaging.js");
    await copyFile(new URL("../pi-extensions/mecha-messaging.js", import.meta.url), extension);
    await copyFile(new URL("../pi-extensions/mailbox-client.js", import.meta.url), join(extensionDirectory, "mailbox-client.js"));
    for (const name of ["recall-state.js", "recall-controller.js", "recall-evaluator.js", "recall-pipeline.js", "memory-capture.js"]) {
      await copyFile(new URL(`../pi-extensions/${name}`, import.meta.url), join(extensionDirectory, name));
    }
    const module = await import(pathToFileURL(extension).href);
    const client = module.createMailboxClient({ agentId: "worker", onEnvelope: () => {} });
    assert.match(client.instanceId, /^[A-Za-z0-9]{21}$/);
    client.close();
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
