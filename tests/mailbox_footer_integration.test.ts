import assert from "node:assert/strict";
import test from "node:test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createMailboxClient } from "../pi-extensions/mecha-messaging/mailbox-client.ts";
import { createPiMailboxDeliveryCoordinator, type DeliveryCoordinatorOptions } from "../pi-extensions/mecha-messaging/delivery-coordinator.ts";

// Only the external socket and Pi injection boundary are replaced.
class MailboxSocket extends EventTarget {
  readyState = 0;
  sent: Record<string, unknown>[] = [];
  send(payload: string) { this.sent.push(JSON.parse(payload)); }
  close() { this.readyState = 3; this.dispatchEvent(new Event("close")); }
  frame(frame: Record<string, unknown>) {
    this.dispatchEvent(new MessageEvent("message", { data: JSON.stringify(frame) }));
  }
}

async function registeredCoordinator(t: test.TestContext, agentId: string, now = () => 1_000) {
  const socket = new MailboxSocket();
  const calls: Parameters<DeliveryCoordinatorOptions["pi"]["sendMessage"]>[] = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => { calls.push(args); } },
    sessionContext: { sessionManager: {
      getBranch: () => [], getSessionId: () => "footer-session", isPersisted: () => true,
      getSessionFile: () => "/unused-footer-session.jsonl",
    } },
    readSessionFile: () => "", now, pendingReinjectionDelayMs: 5_000,
    client: { sendReceipt: (type, delivery, reason) => client.sendReceipt(type, delivery, reason) },
  });
  let completed: (() => void) | undefined;
  const client = createMailboxClient({
    agentId, webSocketFactory: () => socket,
    fetchImpl: async () => new Response(JSON.stringify({}), { status: 200 }),
    onEnvelope: async (message, delivery) => {
      await coordinator.onEnvelope(message, delivery);
      completed?.();
    },
  });
  t.after(() => { coordinator.close(); client.close(); });
  const connecting = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  const register = socket.sent.find((frame) => frame.type === "register");
  assert.ok(register);
  socket.frame({ type: "registered", agent_id: agentId, instance_id: client.instanceId,
    lease_id: "footer-lease", request_id: register.frame_id });
  await connecting;
  return { calls, deliver: async (frame: Record<string, unknown>) => {
    const done = new Promise<void>((resolve) => { completed = resolve; });
    socket.frame(frame);
    await done;
  } };
}

test("real mailbox ingress preserves sender-specific Pi content and options", { timeout: 10_000 }, async (t) => {
  const cases = [
    { from: "wolfgang", to: "supervisor", subject: "status", body: "first\n雪",
      expected: "Mailbox message from wolfgang\nSubject: status\n\nfirst\n雪\n\nReply via mailbox to wolfgang" },
    { from: "supervisor", to: "worker", body: "",
      expected: "Mailbox message from supervisor\n\n\n\nReply via mailbox to supervisor" },
    { from: "worker-a17", to: "supervisor", subject: "", body: "done\n\n[existing hint]",
      expected: "Mailbox message from worker-a17\nSubject: \n\ndone\n\n[existing hint]\n\nReply via mailbox to worker-a17" },
    { from: "supervisor", to: "supervisor", body: "self",
      expected: "Mailbox message from supervisor\n\nself\n\nReply via mailbox to supervisor" },
  ];
  for (const [index, value] of cases.entries()) {
    const { calls, deliver } = await registeredCoordinator(t, value.to);
    const id = `footer-${index}`;
    const { expected, ...message } = value;
    const item = { message_id: id, message_incarnation_id: `incarnation-${index}`,
      attempt_number: 1, delivery_expires_at: "2030-01-01T00:00:30.000Z" };
    await deliver({ type: "message", message: { id, ...message }, attempt_number: 1, delivery_items: [item] });
    assert.deepEqual(calls, [[{
      customType: "mecha-agent-message", content: expected, display: true,
      details: { message_id: id, from: value.from, to: value.to,
        ...(value.subject === undefined ? {} : { subject: value.subject }),
        message_incarnation_id: item.message_incarnation_id,
        delivery_items: [{ messageId: id, messageIncarnationId: item.message_incarnation_id,
          attemptNumber: 1, deliveryExpiresAt: item.delivery_expires_at }] },
    }, { deliverAs: "steer", triggerTurn: true }]]);
  }
});

test("real mailbox ingress renders identical text on retry", { timeout: 10_000 }, async (t) => {
  let time = 1_000;
  const { calls, deliver } = await registeredCoordinator(t, "supervisor", () => time);
  const message = { id: "retry", from: "worker-a17", to: "supervisor", subject: "status", body: "done\n雪" };
  await deliver({ type: "message", message, attempt_number: 1 });
  time += 5_000;
  await deliver({ type: "message", message, attempt_number: 2 });
  assert.equal(calls.length, 2);
  assert.equal(calls[0][0].content, "Mailbox message from worker-a17\nSubject: status\n\ndone\n雪\n\nReply via mailbox to worker-a17");
  assert.equal(calls[1][0].content, calls[0][0].content);
  assert.deepEqual(calls.map((call) => call[1]), Array(2).fill({ deliverAs: "steer", triggerTurn: true }));
});

test("Go-built mixed batch reaches Pi with every original sender instruction", { timeout: 120_000 }, async (t) => {
  const directory = mkdtempSync(join(tmpdir(), "mailbox-footer-batch-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const output = join(directory, "batch.json");
  execFileSync("go", ["test", "./internal/domain", "-run", "^TestMailboxFooterBatchFixture$", "-count=1"], {
    cwd: new URL("../router-proxy/", import.meta.url),
    env: { ...process.env, MAILBOX_FOOTER_BATCH_OUTPUT: output }, timeout: 90_000,
  });
  const fixture = JSON.parse(readFileSync(output, "utf8"));
  const { calls, deliver } = await registeredCoordinator(t, "batch-target");
  await deliver({ type: "message", message: fixture.message, attempt_number: 1, delivery_items: fixture.items });
  assert.equal(calls.length, 1);
  assert.equal(calls[0][0].content, "Mailbox message from wolfgang\n\nMailbox message batch\n\nFrom: wolfgang\n\nfirst body\n\n[Reply via mailbox to wolfgang.]\n\n---\n\nFrom: supervisor\nSubject: \n\nsecond\n雪\n\n[Reply via mailbox to supervisor.]\n\n---\n\nFrom: worker-a17\nSubject: status\n\nthird body\n[existing hint]\n\n[Reply via mailbox to worker-a17.]\n\nReply via mailbox to wolfgang");
  assert.deepEqual(calls[0][1], { deliverAs: "steer", triggerTurn: true });
});
