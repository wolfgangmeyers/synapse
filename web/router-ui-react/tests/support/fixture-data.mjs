export const routerUrl = "http://router.test";
export const agentId = "wolfgang-web";
export const peerId = "peer-agent";
export const instanceId = "instance-1";
export const leaseId = "lease-1";
export const promptExportTestId = "prompt-export";
export const clipboardPasteTestId = "clipboard-paste-target";
export function fixturePromptTemplate(overrides = {}) {
  return { id: "fixture-prompt", name: "Fixture prompt", body: "Review ${file}", ...overrides };
}
export function fixtureClipboardImage() {
  return { name: "clipboard.png", mimeType: "image/png", dataUrl: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=" };
}
export const deliveryItem = (id, sequence = 1) => ({
  message_id: id,
  message_incarnation_id: `00000000-0000-4000-8000-${String(sequence).padStart(12, "0")}`,
  attempt_number: sequence,
  delivery_expires_at: "2030-01-01T00:00:00.000Z",
});
export const message = (id, to = agentId, sequence = 1) => ({
  id,
  from: peerId,
  to,
  body: `message ${id}`,
  created_at: "2030-01-01T00:00:00.000Z",
  recorded_at: "2030-01-01T00:00:00.000Z",
  sequence,
  message_incarnation_id: deliveryItem(id, sequence).message_incarnation_id,
  attachments: [],
});
export function registeredFrame(requestId, currentInstance = instanceId) {
  return { version: "v1", frame_id: "registered-1", type: "registered", request_id: requestId, agent_id: agentId, instance_id: currentInstance, lease_id: leaseId, expires_at: "2030-01-01T00:00:00.000Z", recovered: false, agent_role: "user_interaction" };
}
