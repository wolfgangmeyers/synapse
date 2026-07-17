import assert from "node:assert/strict";
import test from "node:test";

import sentinelChildExtension from "../pi-extensions/sentinel-child.js";

test("Sentinel child extension cannot relay lifecycle output", () => {
  const handlers = new Map();
  sentinelChildExtension({ on: (name, handler) => handlers.set(name, handler) });
  assert.equal(handlers.size, 0);
});
