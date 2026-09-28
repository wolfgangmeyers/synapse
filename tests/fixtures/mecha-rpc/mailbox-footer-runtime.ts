import { readFileSync, writeFileSync } from "node:fs";
import { createInterface } from "node:readline";
import { createMechaRpcExtension } from "../../../pi-extensions/mecha-rpc/index.ts";
import { encodeHiddenEnvelope } from "../../../pi-extensions/mecha-rpc/protocol.ts";

type RecordValue = Record<string, unknown>;
type Command = { handler: (args: string, context: RecordValue) => unknown };
const commands = new Map<string, Command>();
const handlers = new Map<string, (event: RecordValue, context: RecordValue) => unknown>();
const calls: RecordValue[] = [];
const requests: RecordValue[] = [];
const generation = "123e4567-e89b-42d3-a456-426614174000";
const output = process.argv[2]!;
const pi = {
  getAllTools: () => [], getActiveTools: () => [], setActiveTools() {}, getCommands: () => [...commands.keys()],
  registerTool() {}, registerCommand(name: string, command: Command) { commands.set(name, command); },
  on(name: string, handler: (event: RecordValue, context: RecordValue) => unknown) { handlers.set(name, handler); },
  events: { on() {} },
  sendMessage(message: RecordValue, options?: RecordValue) { calls.push({ message, options }); },
};
const context = { sessionManager: { getSessionId: () => "footer-test", getHeader: () => ({}), getEntries: () => [] } };
const extension = createMechaRpcExtension(pi, { getRuntimeGeneration: () => generation, getSessionEpoch: () => 1 });
handlers.get("session_start")!({}, context);
const desired = JSON.parse(readFileSync("router/contract/managed-tools-v1.json", "utf8"));
const manifest = extension.hashManifest(desired);
await commands.get("mecha-bootstrap-v1")!.handler(encodeHiddenEnvelope({
  protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174001",
  runtime_generation: generation, session_epoch: 1, desired_manifest: desired, manifest_sha256: manifest,
  restored_request_ids: [], inline_policy: { enabled: false, credits: {} },
  recall_projection: { enabled: false, stores: [], content: "" },
}), context);
calls.length = 0;
process.stdout.write(JSON.stringify({ ready: true, manifest }) + "\n");
// Text capture only. This fixture does not implement Pi RPC responses,
// transcript persistence, append receipts, or settlement.
for await (const line of createInterface({ input: process.stdin })) {
  const request = JSON.parse(line);
  requests.push(request);
  const [name, argument] = request.message.slice(1).split(" ");
  await commands.get(name)!.handler(argument, context);
  writeFileSync(output, JSON.stringify({ calls, requests }));
}
