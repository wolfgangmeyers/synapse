const agentId = "wolfgang-web";
const peerId = "peer-agent";
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
