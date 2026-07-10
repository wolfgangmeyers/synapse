import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const PI_ROOT = "/Users/wolfgang.meyers/.nvm/versions/node/v26.2.0/lib/node_modules/@earendil-works/pi-coding-agent";

test("Pi 0.80.6 declares every shutdown reason required by the Phase 1 lifecycle matrix", async () => {
  const declarations = await readFile(`${PI_ROOT}/dist/core/extensions/types.d.ts`, "utf8");
  assert.match(declarations, /reason: "quit" \| "reload" \| "new" \| "resume" \| "fork"/);
});

test("Pi documents replacement callbacks as fresh-session-only contexts", async () => {
  const docs = await readFile(`${PI_ROOT}/docs/extensions.md`, "utf8");
  assert.match(docs, /ReplacedSessionContext/);
  assert.match(docs, /old `pi` \/ old command `ctx` session-bound objects are stale/);
});
