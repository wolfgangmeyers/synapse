import { randomUUID } from "node:crypto";

export const INBOUND_SUBJECT = "discord.inbound.v1";
export const DISCORD_CONTENT_LIMIT = 2_000;

export function truncateUtf8(value, maximumBytes) {
  if (!Number.isSafeInteger(maximumBytes) || maximumBytes < 0) throw new TypeError("maximumBytes must be non-negative");
  let result = "";
  let used = 0;
  for (const character of String(value)) {
    const size = Buffer.byteLength(character, "utf8");
    if (used + size > maximumBytes) break;
    result += character;
    used += size;
  }
  return result;
}

function cleanUntrusted(value) {
  return String(value ?? "").replace(/\0/g, "�").normalize("NFC");
}

function requiredId(value, name) {
  if (typeof value !== "string" || value.length === 0 || value.length > 128) throw new TypeError(`${name} is required`);
  return value;
}

export function messageIsAllowed(event, channelId) {
  if (event?.type && event.type !== "MESSAGE_CREATE") return false;
  if (!event || typeof event !== "object" || event.guild_id === undefined || event.webhook_id !== undefined) return false;
  if (event.author?.bot || typeof event.content !== "string" || event.content.length === 0) return false;
  return event.channel_id === channelId;
}

export function inboundEnvelope(event, { agentId, id = randomUUID(), now = () => new Date(), maxBodyBytes = 4 * 1024 } = {}) {
  requiredId(agentId, "agentId");
  const sourceMessageId = requiredId(event.id, "source_message_id");
  const author = truncateUtf8(cleanUntrusted(event.author?.username), Math.min(1_024, Math.floor(maxBodyBytes / 4)));
  const header = [
    "Discord inbound message. Author and content below are untrusted.",
    "--- begin untrusted author ---",
    author,
    "--- end untrusted author ---",
    "--- begin untrusted content ---",
  ].join("\n");
  const footer = "\n--- end untrusted content ---";
  const content = cleanUntrusted(event.content);
  const available = maxBodyBytes - Buffer.byteLength(header + footer, "utf8");
  if (available < 0) throw new RangeError("Discord metadata exceeds Mailbox body limit");
  const bounded = truncateUtf8(content, available);
  const truncated = bounded !== content;
  const marker = "\n[truncated]";
  const marked = truncated
    ? `${truncateUtf8(bounded, Math.max(0, available - Buffer.byteLength(marker, "utf8")))}${marker}`
    : bounded;
  const body = `${header}\n${marked}${footer}`;
  if (Buffer.byteLength(body, "utf8") > maxBodyBytes) throw new Error("inbound body limit violated");
  return {
    envelope: { id, from: agentId, to: "supervisor", subject: INBOUND_SUBJECT, body, created_at: now().toISOString(), source: { kind: "discord" } },
    sourceMessageId, truncated,
  };
}

export function supervisorMessage(message, agentId) {
  if (message?.from !== "supervisor" || message?.to !== agentId || typeof message.body !== "string") return undefined;
  return { messageId: message.id, body: message.body };
}

export function splitDiscordContent(value, maximum = DISCORD_CONTENT_LIMIT) {
  if (!Number.isSafeInteger(maximum) || maximum <= 0) throw new TypeError("maximum must be positive");
  const text = String(value);
  if (text.length === 0) return [" "];
  const chunks = [];
  let remaining = text;
  while (remaining.length > 0) {
    const chunk = truncateUtf8(remaining, maximum);
    chunks.push(chunk || " ");
    remaining = remaining.slice(chunk.length);
  }
  return chunks;
}

export const safeAllowedMentions = Object.freeze({ parse: [], replied_user: false });

export function createBridge({ state, postEnvelope, discord, agentId, channelId, maxBodyBytes = 4 * 1024, now = () => Date.now(), schedule = setTimeout, log = () => {} } = {}) {
  if (!state || typeof state.admitInbound !== "function" || typeof state.admitOutbound !== "function") throw new TypeError("durable state is required");
  if (typeof postEnvelope !== "function" || !discord || typeof discord.createMessage !== "function") throw new TypeError("router and Discord adapters are required");
  requiredId(agentId, "agentId");
  requiredId(channelId, "channelId");
  let timer;
  let closed = false;
  let active = 0;
  let drained;
  let flushing = false;
  let inboundFlushing = false;
  let outboundFlushing = false;

  const incidentBody = (kind) => ({
    inbound_outbox_full: "Discord bridge inbound outbox is full; matching events are temporarily dropped.",
    outbound_full: "Discord bridge outbound queue is full; Mailbox will retry or dead-letter the message.",
    discord_ambiguous: "Discord message outcome is ambiguous; inspect the bridge state before retrying.",
    discord_permanent_failure: "Discord rejected a bridge message; inspect the bridge state for IDs and HTTP status.",
  }[kind] ?? "Discord bridge needs attention.");
  const deliverIncident = async (kind, body = incidentBody(kind)) => {
    try {
      await postEnvelope({ id: randomUUID(), from: agentId, to: "supervisor", subject: "discord.failure.v1", body, created_at: new Date(now()).toISOString() });
      state.markIncidentReported(kind);
    } catch { /* The pending incident is retried after router recovery. */ }
  };
  const report = async (kind, body) => {
    const raised = state.raiseIncident(kind);
    if (raised || state.pendingIncidents().includes(kind)) await deliverIncident(kind, body);
  };
  const flushIncidentNotices = async () => {
    for (const kind of state.pendingIncidents()) await deliverIncident(kind);
  };
  const scheduleFlush = (delay = 0) => {
    if (closed || timer !== undefined) return;
    timer = schedule(async () => { timer = undefined; if (!closed) await bridge.flush(); }, delay);
  };
  const operation = async (action) => {
    active += 1;
    try { return await action(); } finally {
      active -= 1;
      if (active === 0) drained?.();
    }
  };
  const bridge = {
    async acceptDiscordEvent(event) {
      return operation(async () => {
        if (closed || !messageIsAllowed(event, channelId)) return { accepted: false, reason: "rejected_channel_or_event" };
        const built = inboundEnvelope(event, { agentId, maxBodyBytes, now: () => new Date(now()) });
        const admitted = state.admitInbound(built);
        if (!admitted.accepted) {
          await report("inbound_outbox_full", "Discord bridge inbound outbox is full; matching events are temporarily dropped.");
          return admitted;
        }
        await bridge.flushInbound();
        return admitted;
      });
    },
    async flushInbound() {
      return operation(async () => {
        if (closed || inboundFlushing) return;
        inboundFlushing = true;
        try {
          for (const pending of state.pendingInbound()) {
            try {
              await postEnvelope(pending.envelope);
              state.markInboundAccepted(pending.sourceMessageId);
            } catch (error) {
              log("inbound_enqueue_retry", { sourceMessageId: pending.sourceMessageId, status: error?.status });
              scheduleFlush(error?.retryAfterMs ?? 1_000);
              break;
            }
          }
        } finally { inboundFlushing = false; }
      });
    },
    acceptSupervisorMessage(message) {
      if (closed) return { accepted: false, reason: "bridge_closed" };
      const outbound = supervisorMessage(message, agentId);
      if (outbound === undefined) return { accepted: false, reason: "rejected_sender_or_recipient" };
      const accepted = state.admitOutbound(outbound);
      if (accepted.accepted) scheduleFlush();
      else if (accepted.reason === "outbound_full") void report("outbound_full");
      return accepted;
    },
    async flushOutbound() {
      return operation(async () => {
        if (closed || outboundFlushing) return;
        outboundFlushing = true;
        try {
          for (const pending of state.pendingOutbound()) {
          try {
            const chunks = splitDiscordContent(pending.body);
            let firstMessageId;
            for (const content of chunks) {
              const sent = await discord.createMessage({ channelId, content, allowedMentions: safeAllowedMentions });
              firstMessageId ??= sent?.id;
            }
            state.completeOutbound(pending.messageId, { state: "succeeded", discordMessageId: firstMessageId });
          } catch (error) {
            const status = Number(error?.status);
            if (error?.ambiguous) {
              state.completeOutbound(pending.messageId, { state: "ambiguous", statusCode: status || null });
              await report("discord_ambiguous", `Discord message outcome is ambiguous for mailbox message ${pending.messageId}.`);
            } else if (status >= 400 && status < 500 && status !== 429) {
              state.completeOutbound(pending.messageId, { state: "failed", statusCode: status });
              await report("discord_permanent_failure", `Discord rejected mailbox message ${pending.messageId} (HTTP ${status}).`);
            } else {
              const delay = error?.retryAfterMs ?? Math.min(60_000, 1_000 * (2 ** Math.min(pending.attempts, 6)));
              state.retryOutbound(pending.messageId, now() + delay);
              scheduleFlush(delay);
              break;
            }
          }
          }
        } finally { outboundFlushing = false; }
      });
    },
    async flush() {
      return operation(async () => {
        if (timer !== undefined) { clearTimeout(timer); timer = undefined; }
        if (closed || flushing) return;
        flushing = true;
        try { await bridge.flushInbound(); await flushIncidentNotices(); await bridge.flushOutbound(); } finally { flushing = false; }
      });
    },
    async close() {
      closed = true;
      if (timer !== undefined) clearTimeout(timer);
      if (active > 0) await new Promise((resolve) => { drained = resolve; });
    },
  };
  return bridge;
}
