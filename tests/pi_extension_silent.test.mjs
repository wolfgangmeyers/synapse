import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { resolveAgentId } from "../pi-extensions/mecha-messaging.js";

test("Pi mailbox extension never writes diagnostics to the console", async () => {
  const source = await readFile(new URL("../pi-extensions/mecha-messaging.js", import.meta.url), "utf8");
  assert.doesNotMatch(source, /console\.(?:log|warn|error)/);
});

test("Pi mailbox identity resolves from the current tmux window without identity configuration", () => {
  assert.equal(
    resolveAgentId(
      { TMUX: "/tmp/tmux-1/default,1,0", TMUX_PANE: "%7" },
      { queryWindowName: () => "worker_1" },
    ),
    "worker_1",
  );
});

test("Pi mailbox identity fails closed outside tmux or for an invalid window name", () => {
  assert.equal(resolveAgentId({}, { queryWindowName: () => "worker-1" }), undefined);
  assert.throws(
    () => resolveAgentId(
      { TMUX: "x", TMUX_PANE: "%7" },
      { queryWindowName: () => "bad name" },
    ),
    /identity is invalid/,
  );
});
