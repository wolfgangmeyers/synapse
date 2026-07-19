import WebSocket from "ws";

const DISCORD_API = "https://discord.com/api/v10";
const GUILD_INTENTS = (1 << 0) | (1 << 9) | (1 << 15);

export function shouldAcceptGatewayMessage(event, channelId) {
  if (event?.author?.bot || event?.webhook_id !== undefined || !event?.guild_id || !event?.content) return false;
  return event.channel_id === channelId;
}

export function gatewayIdentify(token) {
  return { op: 2, d: { token, intents: GUILD_INTENTS, properties: { os: process.platform, browser: "mecha", device: "mecha" } } };
}

export function createDiscordRest({ token, fetchImpl = fetch, sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms)) } = {}) {
  if (typeof token !== "string" || token.length === 0) throw new TypeError("Discord token is required");
  let queue = Promise.resolve();
  const request = async ({ channelId, content, allowedMentions }) => {
    let attempts = 0;
    while (true) {
      try {
        const response = await fetchImpl(`${DISCORD_API}/channels/${encodeURIComponent(channelId)}/messages`, {
          method: "POST",
          headers: { authorization: `Bot ${token}`, "content-type": "application/json" },
          body: JSON.stringify({ content, allowed_mentions: allowedMentions }),
        });
        if (response.ok) return response.json();
        const payload = await response.json().catch(() => ({}));
        const retryAfterMs = Math.ceil(Number(payload.retry_after ?? response.headers.get("retry-after") ?? 0) * 1_000);
        if (response.status === 429) {
          await sleep(Math.max(1, retryAfterMs));
          continue;
        }
        if (response.status >= 500 && attempts++ < 5) { await sleep(Math.min(60_000, 1_000 * (2 ** attempts))); continue; }
        throw Object.assign(new Error(`Discord REST rejected message (${response.status})`), { status: response.status });
      } catch (error) {
        if (error?.status !== undefined) throw error;
        if (attempts++ >= 5) throw Object.assign(new Error("Discord REST outcome is ambiguous"), { ambiguous: true });
        await sleep(Math.min(60_000, 1_000 * (2 ** attempts)));
      }
    }
  };
  return { createMessage(payload) { const next = queue.then(() => request(payload)); queue = next.catch(() => {}); return next; } };
}

/** Gateway v10 transport. It is inert until the later local-secret/live-test gate starts it. */
export function createDiscordGateway({ token, channelId, onMessage, fetchImpl = fetch, webSocketFactory = (url) => new WebSocket(url), schedule = setTimeout, clearSchedule = clearTimeout, random = Math.random, log = () => {} } = {}) {
  if (typeof token !== "string" || token.length === 0 || typeof channelId !== "string" || channelId.length === 0 || typeof onMessage !== "function") throw new TypeError("token, channelId, and onMessage are required");
  let socket; let heartbeat; let awaitingHeartbeatAck = false; let stopped = false; let sequence = null; let sessionId; let resumeUrl; let gatewayUrl; let canResume = false; let sessionStart;
  const send = (frame) => socket?.readyState === WebSocket.OPEN && socket.send(JSON.stringify(frame));
  const clearHeartbeat = () => {
    clearSchedule(heartbeat);
    heartbeat = undefined;
    awaitingHeartbeatAck = false;
  };
  const scheduleHeartbeat = (intervalMs) => {
    const beat = () => {
      if (awaitingHeartbeatAck) { socket?.close(); return; }
      awaitingHeartbeatAck = true;
      send({ op: 1, d: sequence });
      heartbeat = schedule(beat, intervalMs);
    };
    heartbeat = schedule(beat, intervalMs);
  };
  const reconnect = (delay = 1_000) => { if (!stopped) schedule(connect, delay); };
  const identify = () => {
    if (sessionStart.remaining <= 0) {
      schedule(() => { sessionStart.remaining = sessionStart.total; socket?.close(); }, sessionStart.resetAfterMs);
      return;
    }
    sessionStart.remaining -= 1;
    send(gatewayIdentify(token));
  };
  const connect = () => {
    if (stopped || !gatewayUrl) return;
    socket = webSocketFactory(resumeUrl ?? gatewayUrl);
    socket.on("message", async (data) => {
      let frame; try { frame = JSON.parse(data.toString()); } catch { return; }
      if (Number.isInteger(frame.s)) sequence = frame.s;
      if (frame.op === 10) {
        scheduleHeartbeat(frame.d.heartbeat_interval);
        if (canResume && sessionId) send({ op: 6, d: { token, session_id: sessionId, seq: sequence } }); else identify();
      } else if (frame.op === 11) { awaitingHeartbeatAck = false; }
      else if (frame.op === 7) { socket.close(); }
      else if (frame.op === 9) {
        canResume = frame.d === true;
        if (!canResume) { sessionId = undefined; resumeUrl = undefined; }
        schedule(() => { socket.close(); }, Math.floor(random() * 5_000));
      }
      else if (frame.op === 0 && frame.t === "READY") { sessionId = frame.d.session_id; resumeUrl = frame.d.resume_gateway_url; canResume = true; }
      else if (frame.op === 0 && frame.t === "RESUMED") { canResume = true; }
      else if (frame.op === 0 && frame.t === "MESSAGE_CREATE" && shouldAcceptGatewayMessage(frame.d, channelId)) await onMessage(frame.d);
    });
    socket.on("close", () => { clearHeartbeat(); reconnect(); });
    socket.on("error", (error) => log("gateway_error", { message: error.message }));
  };
  return {
    async start() {
      const response = await fetchImpl(`${DISCORD_API}/gateway/bot`, { headers: { authorization: `Bot ${token}` } });
      if (!response.ok) throw new Error(`Discord Gateway discovery failed (${response.status})`);
      const bot = await response.json();
      if (bot.session_start_limit?.remaining <= 0) throw new Error("Discord session-start limit is exhausted");
      sessionStart = {
        remaining: bot.session_start_limit.remaining,
        total: bot.session_start_limit.total,
        resetAfterMs: bot.session_start_limit.reset_after,
      };
      gatewayUrl = `${bot.url}?v=10&encoding=json`;
      connect();
    },
    close() { stopped = true; clearHeartbeat(); socket?.close(); },
  };
}
