import assert from "node:assert/strict";
import { chmod, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { prepareSentinelChildAuth } from "../pi-extensions/mecha-messaging.js";

test("operator-provisioned child auth is ownership/mode checked and confined to an ephemeral HOME", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-auth-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const auth = join(directory, "operator-auth.json");
  await writeFile(auth, "operator-secret");
  await chmod(auth, 0o600);

  const prepared = await prepareSentinelChildAuth({ authPath: auth });
  t.after(() => prepared.cleanup());
  assert.match(prepared.home, /mecha-sentinel-auth-home-/);
  assert.match(prepared.authPath, /\.pi\/agent\/auth\.json$/);
  assert.equal(await (await import("node:fs/promises")).readFile(prepared.authPath, "utf8"), "operator-secret");
  assert.equal((await (await import("node:fs/promises")).stat(prepared.authPath)).mode & 0o777, 0o600);
  assert.equal(prepared.environment.MECHA_SENTINEL_AUTH_PATH, undefined);
  assert.equal(JSON.stringify(prepared).includes("operator-secret"), false);
  await prepared.cleanup();
  await assert.rejects((await import("node:fs/promises")).stat(prepared.home));
});

test("child auth rejects missing, symlinked, wrong-owner, and insecure material", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-auth-invalid-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await assert.rejects(prepareSentinelChildAuth({ authPath: join(directory, "missing") }), /regular 0600/);
  const insecure = join(directory, "insecure");
  await writeFile(insecure, "secret");
  await chmod(insecure, 0o644);
  await assert.rejects(prepareSentinelChildAuth({ authPath: insecure }), /regular 0600/);
  await chmod(insecure, 0o600);
  await assert.rejects(prepareSentinelChildAuth({ authPath: insecure, getuid: () => -1 }), /owned/);
});
