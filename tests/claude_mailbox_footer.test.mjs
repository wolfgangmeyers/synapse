import assert from "node:assert/strict";
import test from "node:test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createMailboxClient } from "../tools/tmux-worker/mailbox-client.js";
import { createClaudeSidecar, createTmuxInjector } from "../tools/tmux-worker/claude_sidecar.mjs";
import { createTmuxRestClient } from "../tools/tmux-worker/tmux-rest-client.js";

const cases = [
  { from: "wolfgang", to: "supervisor", body: "first\r\n雪\u001b[31mred\u001b[0m\u0000\u0085\tend",
    expected: "Mailbox message from wolfgang\n\nfirst\n雪red\tend\n\nReply via mailbox to wolfgang" },
  { from: "supervisor", to: "worker-b", subject: "", body: "",
    expected: "Mailbox message from supervisor\nSubject: \n\n\n\nReply via mailbox to supervisor" },
  { from: "worker-a17", to: "supervisor", subject: "status", body: "done\n[existing hint]",
    expected: "Mailbox message from worker-a17\nSubject: status\n\ndone\n[existing hint]\n\nReply via mailbox to worker-a17" },
  { from: "supervisor", to: "supervisor", body: "self",
    expected: "Mailbox message from supervisor\n\nself\n\nReply via mailbox to supervisor" },
];

async function fixture(t, transport, target) {
  const requests = [];
  const argv = [];
  const socket = new EventTarget();
  socket.readyState = 0;
  const frames = [];
  let complete;
  let failText = false;
  socket.send = (raw) => {
    const frame = JSON.parse(raw);
    frames.push(frame);
    if (frame.type === "ack" || frame.type === "nack") complete?.();
  };
  socket.close = () => { socket.readyState = 3; socket.dispatchEvent(new Event("close")); };
  const frame = (value) => socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify(value) }));
  // Only external HTTP, terminal execution and clocks are replaced.
  const tmuxRest = createTmuxRestClient({ fetchImpl: async (url, options) => {
    requests.push({ url, ...options });
    if (options.method === "GET") return { ok: true, json: async () => ({ windows: [{ name: target, window_id: "@7", server_id: "a".repeat(32) }] }) };
    const { keys } = JSON.parse(options.body);
    return { ok: !(failText && keys.startsWith("Mailbox message")), json: async () => ({}) };
  } });
  if (transport === "rest") {
    t.mock.method(globalThis, "setTimeout", (callback, ms) => {
      assert.equal(ms, 0);
      queueMicrotask(callback);
      return 1;
    });
  }
  const inject = createTmuxInjector(transport === "rest" ? { tmuxRest, windowId: "@7", serverId: "a".repeat(32), enterDelayMs: 0 } : {
    tmux: (args) => {
      argv.push(args);
      if (failText && args[1] === "-l") throw new Error("external terminal failure");
      return "%7";
    },
    sleep: (ms) => assert.equal(ms, 5000),
  });
  let sidecar;
  const client = createMailboxClient({
    agentId: target, webSocketFactory: () => socket,
    fetchImpl: async () => ({ ok: true, json: async () => ({}) }),
    setTimeoutImpl: () => 1, clearTimeoutImpl: () => {},
    setIntervalImpl: () => 1, clearIntervalImpl: () => {},
    onEnvelope: (message, delivery) => sidecar.onEnvelope(message, delivery),
  });
  sidecar = createClaudeSidecar({ agentId: target, client, inject });
  t.after(() => client.close());
  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  const register = frames.find((value) => value.type === "register");
  frame({ type: "registered", agent_id: target, instance_id: client.instanceId,
    lease_id: "lease-footer", request_id: register.frame_id });
  await connected;
  return { requests, argv,
    setFailure(value) { failText = value; },
    texts: () => transport === "rest"
      ? requests.filter((request) => request.method === "POST").map((request) => JSON.parse(request.body).keys).filter((keys) => keys.startsWith("Mailbox message"))
      : argv.filter((args) => args[1] === "-l").map((args) => args.at(-1)),
    deliver(message, attempt = 1, items) {
      const done = new Promise((resolve) => { complete = resolve; });
      frame({ type: "message", message, attempt_number: attempt,
        delivery_items: items ?? [{ message_id: message.id, message_incarnation_id: "inc-1", attempt_number: attempt }] });
      return done;
    },
  };
}

for (const transport of ["rest", "subprocess"]) {
  for (const [index, value] of cases.entries()) {
    test(`${transport}: exact sender footer and original content ${index}`, { timeout: 10000 }, async (t) => {
      const f = await fixture(t, transport, value.to);
      const { expected, ...original } = value;
      const message = { id: `message-${index}`, ...original };
      const before = structuredClone(message);
      await f.deliver(message);
      assert.deepEqual(message, before);
      assert.deepEqual(f.texts(), [expected]);
      if (transport === "rest") {
        assert.deepEqual(f.requests.map((request) => [request.method, request.url, request.body]), [
          ["GET", "http://127.0.0.1:8765/v1/tmux/windows", undefined],
          ...["\u001b[200~", expected, "\u001b[201~", "\r"].map((keys) => ["POST", `http://127.0.0.1:8765/v1/tmux/windows/${value.to}/keys`, JSON.stringify({ keys, window_id: "@7", server_id: "a".repeat(32) })]),
        ]);
      } else {
        assert.deepEqual(f.argv, [
          ["display-message", "-p", "-t", `mecha:${value.to}`, "#{pane_id}"],
          ["send-keys", "-t", `mecha:${value.to}`, "\u001b[200~"],
          ["send-keys", "-l", "-t", `mecha:${value.to}`, expected],
          ["send-keys", "-t", `mecha:${value.to}`, "\u001b[201~"],
          ["send-keys", "-t", `mecha:${value.to}`, "\r"],
        ]);
      }
    });
  }
  test(`${transport}: retry preserves rendered text`, { timeout: 10000 }, async (t) => {
    const f = await fixture(t, transport, "supervisor");
    const message = { id: "retry", from: "worker-a17", to: "supervisor", subject: "status", body: "done\n雪\n[existing hint]" };
    f.setFailure(true);
    await f.deliver(message);
    f.setFailure(false);
    await f.deliver(message, 2);
    const expected = "Mailbox message from worker-a17\nSubject: status\n\ndone\n雪\n[existing hint]\n\nReply via mailbox to worker-a17";
    assert.deepEqual(f.texts(), [expected, expected]);
  });
}

test("Go-built mixed batch retains every item footer at Claude REST output", { timeout: 60000 }, async (t) => {
  const dir = mkdtempSync(join(tmpdir(), "mailbox40-claude-batch-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const output = join(dir, "batch.json");
  execFileSync("go", ["test", "./internal/domain", "-run", "^TestMailboxFooterBatchFixture$", "-count=1"], {
    cwd: new URL("../router-proxy/", import.meta.url),
    env: { ...process.env, MAILBOX_FOOTER_BATCH_OUTPUT: output }, timeout: 45000,
  });
  const { message, items } = JSON.parse(readFileSync(output, "utf8"));
  const f = await fixture(t, "rest", "batch-target");
  await f.deliver(message, 1, items);
  assert.deepEqual(f.texts(), ["Mailbox message from wolfgang\n\nMailbox message batch\n\nFrom: wolfgang\n\nfirst body\n\n[Reply via mailbox to wolfgang.]\n\n---\n\nFrom: supervisor\nSubject: \n\nsecond\n雪\n\n[Reply via mailbox to supervisor.]\n\n---\n\nFrom: worker-a17\nSubject: status\n\nthird body\n[existing hint]\n\n[Reply via mailbox to worker-a17.]\n\nReply via mailbox to wolfgang"]);
});
