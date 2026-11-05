import WebSocket from "ws";
import { fixturePeerAgent } from "./isolated-go-router.mjs";

function frameFromMessage(data) {
  const payload = data?.toString?.() ?? String(data);
  return JSON.parse(payload);
}

/** @param {WebSocket} socket @param {(frame: unknown) => boolean} [predicate] @param {number} [timeoutMs] @returns {Promise<unknown>} */
export function waitForWebSocketFrame(socket, predicate = () => true, timeoutMs = 10_000) {
  return new Promise((resolveFrame, rejectFrame) => {
    const timer = setTimeout(() => {
      socket.off("message", onMessage);
      rejectFrame(new Error("timed out waiting for router WebSocket frame"));
    }, timeoutMs);
    const onMessage = (data) => {
      let frame;
      try { frame = frameFromMessage(data); } catch (error) {
        clearTimeout(timer);
        socket.off("message", onMessage);
        rejectFrame(error);
        return;
      }
      if (!predicate(frame)) return;
      clearTimeout(timer);
      socket.off("message", onMessage);
      resolveFrame(frame);
    };
    socket.on("message", onMessage);
  });
}

/** @returns {Promise<{agent_id: string, instance_id: string, lease_id: string, socket: WebSocket, registered: Record<string, unknown>, send: (frame: unknown) => void, close: () => Promise<void>}>} */
export async function connectFixturePeer(router, overrides = {}) {
  const peer = fixturePeerAgent(overrides);
  const socket = new WebSocket(router.websocketUrl);
  await new Promise((resolveOpen, rejectOpen) => {
    socket.once("open", resolveOpen);
    socket.once("error", rejectOpen);
  });
  const frameId = overrides.frame_id ?? "fixture-register";
  socket.send(JSON.stringify({
    version: "v1",
    frame_id: frameId,
    type: "register",
    agent_id: peer.agent_id,
    instance_id: peer.instance_id,
    agent_role: peer.role,
    client_type: "test",
    ack_confirmation: "correlated_v1",
    conversation_sync: "v1",
    delivery_receipts: "delivery_items_v1",
  }));
  const registered = await waitForWebSocketFrame(socket, (frame) => frame.type === "registered" && frame.request_id === frameId);
  return {
    ...peer,
    socket,
    registered,
    send(frame) { socket.send(JSON.stringify(frame)); },
    async close() {
      if (socket.readyState === WebSocket.CLOSED) return;
      await new Promise((resolveClose) => {
        socket.once("close", resolveClose);
        socket.close(1000, "fixture complete");
      });
    },
  };
}
