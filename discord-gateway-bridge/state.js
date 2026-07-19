import { DatabaseSync } from "node:sqlite";
import { chmodSync, existsSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";

const ROUTE_RETENTION_MS = 7 * 24 * 60 * 60 * 1_000;
const RETAINED_ROWS = 1_024;
const MAX_PENDING = 100;

function protect(path) {
  for (const candidate of [path, `${path}-wal`, `${path}-shm`]) {
    if (existsSync(candidate)) chmodSync(candidate, 0o600);
  }
}

function transaction(database, path, action) {
  database.exec("BEGIN IMMEDIATE");
  try {
    const result = action();
    database.exec("COMMIT");
    protect(path);
    return result;
  } catch (error) {
    database.exec("ROLLBACK");
    throw error;
  }
}

/** Durable, content-bounded bridge state. All content disappears on completion. */
export function openBridgeState({ databasePath, clock = () => Date.now() } = {}) {
  if (typeof databasePath !== "string" || databasePath.length === 0 || databasePath === ":memory:") {
    throw new TypeError("databasePath must be a persistent local SQLite path");
  }
  mkdirSync(dirname(databasePath), { recursive: true, mode: 0o700 });
  const database = new DatabaseSync(databasePath, { enableForeignKeyConstraints: true, timeout: 5_000 });
  database.exec("PRAGMA journal_mode = WAL; PRAGMA synchronous = FULL; PRAGMA foreign_keys = ON;");
  database.exec(`
    CREATE TABLE IF NOT EXISTS inbound_outbox (
      source_message_id TEXT PRIMARY KEY,
      envelope_json TEXT NOT NULL,
      reply_token TEXT NOT NULL UNIQUE,
      channel_id TEXT NOT NULL,
      guild_id TEXT,
      created_at INTEGER NOT NULL
    );
    CREATE TABLE IF NOT EXISTS inbound_routes (
      reply_token TEXT PRIMARY KEY,
      channel_id TEXT NOT NULL,
      source_message_id TEXT NOT NULL,
      guild_id TEXT,
      created_at INTEGER NOT NULL
    );
    CREATE TABLE IF NOT EXISTS outbound (
      message_id TEXT PRIMARY KEY,
      reply_token TEXT NOT NULL,
      channel_id TEXT NOT NULL,
      source_message_id TEXT NOT NULL,
      body TEXT,
      state TEXT NOT NULL CHECK (state IN ('pending', 'succeeded', 'failed', 'ambiguous')),
      attempts INTEGER NOT NULL DEFAULT 0,
      retry_at INTEGER,
      discord_message_id TEXT,
      status_code INTEGER,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL
    );
    CREATE TABLE IF NOT EXISTS incidents (
      kind TEXT PRIMARY KEY,
      reported_at INTEGER NOT NULL,
      pending INTEGER NOT NULL DEFAULT 1
    );
  `);
  protect(databasePath);

  const now = () => Number(clock());
  const state = {
    close() { database.close(); },
    prune() {
      const cutoff = now() - ROUTE_RETENTION_MS;
      return transaction(database, databasePath, () => {
        database.prepare("DELETE FROM inbound_routes WHERE created_at < ?").run(cutoff);
        database.prepare("DELETE FROM outbound WHERE state != 'pending' AND updated_at < ?").run(cutoff);
        database.prepare("DELETE FROM inbound_routes WHERE rowid IN (SELECT rowid FROM inbound_routes ORDER BY created_at DESC, rowid DESC LIMIT -1 OFFSET ?)")
          .run(RETAINED_ROWS);
        database.prepare("DELETE FROM outbound WHERE state != 'pending' AND rowid IN (SELECT rowid FROM outbound WHERE state != 'pending' ORDER BY created_at DESC, rowid DESC LIMIT -1 OFFSET ?)")
          .run(RETAINED_ROWS);
      });
    },
    admitInbound({ sourceMessageId, envelope, replyToken, channelId, guildId = null }) {
      state.prune();
      return transaction(database, databasePath, () => {
        const existing = database.prepare("SELECT envelope_json FROM inbound_outbox WHERE source_message_id = ?").get(sourceMessageId);
        if (existing !== undefined) return { accepted: true, duplicate: true, envelope: JSON.parse(existing.envelope_json) };
        const delivered = database.prepare("SELECT reply_token FROM inbound_routes WHERE source_message_id = ?").get(sourceMessageId);
        if (delivered !== undefined) return { accepted: true, duplicate: true, replyToken: delivered.reply_token };
        const count = database.prepare("SELECT COUNT(*) AS count FROM inbound_outbox").get().count;
        if (count >= MAX_PENDING) return { accepted: false, reason: "inbound_outbox_full" };
        database.prepare(`INSERT INTO inbound_outbox (source_message_id, envelope_json, reply_token, channel_id, guild_id, created_at)
          VALUES (?, ?, ?, ?, ?, ?)`)
          .run(sourceMessageId, JSON.stringify(envelope), replyToken, channelId, guildId, now());
        return { accepted: true, duplicate: false, envelope };
      });
    },
    pendingInbound() {
      return database.prepare("SELECT source_message_id, envelope_json, reply_token, channel_id, guild_id FROM inbound_outbox ORDER BY created_at ASC")
        .all().map((row) => ({ ...row, envelope: JSON.parse(row.envelope_json) }));
    },
    markInboundAccepted(sourceMessageId) {
      transaction(database, databasePath, () => {
        const row = database.prepare("SELECT * FROM inbound_outbox WHERE source_message_id = ?").get(sourceMessageId);
        if (row === undefined) return;
        database.prepare(`INSERT INTO inbound_routes (reply_token, channel_id, source_message_id, guild_id, created_at)
          VALUES (?, ?, ?, ?, ?) ON CONFLICT(reply_token) DO NOTHING`)
          .run(row.reply_token, row.channel_id, row.source_message_id, row.guild_id, now());
        database.prepare("DELETE FROM inbound_outbox WHERE source_message_id = ?").run(sourceMessageId);
      });
      state.prune();
    },
    routeFor(replyToken) {
      const row = database.prepare("SELECT reply_token, channel_id, source_message_id FROM inbound_routes WHERE reply_token = ?").get(replyToken);
      return row === undefined ? undefined : { replyToken: row.reply_token, channelId: row.channel_id, sourceMessageId: row.source_message_id };
    },
    admitOutbound({ messageId, replyToken, body }) {
      state.prune();
      return transaction(database, databasePath, () => {
        const existing = database.prepare("SELECT state FROM outbound WHERE message_id = ?").get(messageId);
        if (existing !== undefined) return { accepted: true, duplicate: true, state: existing.state };
        const route = state.routeFor(replyToken);
        if (route === undefined) return { accepted: false, reason: "unknown_reply_token" };
        const pending = database.prepare("SELECT COUNT(*) AS count FROM outbound WHERE state = 'pending'").get().count;
        if (pending >= MAX_PENDING) return { accepted: false, reason: "outbound_full" };
        const at = now();
        database.prepare(`INSERT INTO outbound (message_id, reply_token, channel_id, source_message_id, body, state, retry_at, created_at, updated_at)
          VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?)`)
          .run(messageId, replyToken, route.channelId, route.sourceMessageId, body, at, at, at);
        return { accepted: true, duplicate: false };
      });
    },
    pendingOutbound() {
      return database.prepare(`SELECT message_id, reply_token, channel_id, source_message_id, body, attempts
        FROM outbound WHERE state = 'pending' AND retry_at <= ? ORDER BY created_at ASC`).all(now()).map((row) => ({
        messageId: row.message_id, replyToken: row.reply_token, channelId: row.channel_id,
        sourceMessageId: row.source_message_id, body: row.body, attempts: row.attempts,
      }));
    },
    retryOutbound(messageId, retryAt) {
      transaction(database, databasePath, () => database.prepare(`UPDATE outbound SET attempts = attempts + 1, retry_at = ?, updated_at = ?
        WHERE message_id = ? AND state = 'pending'`).run(retryAt, now(), messageId));
      state.prune();
    },
    completeOutbound(messageId, { state: outcome, discordMessageId = null, statusCode = null }) {
      if (!["succeeded", "failed", "ambiguous"].includes(outcome)) throw new TypeError("invalid outbound state");
      transaction(database, databasePath, () => database.prepare(`UPDATE outbound SET body = NULL, state = ?, discord_message_id = ?, status_code = ?, updated_at = ?
        WHERE message_id = ? AND state = 'pending'`).run(outcome, discordMessageId, statusCode, now(), messageId));
      state.prune();
    },
    raiseIncident(kind, intervalMs = 5 * 60 * 1_000) {
      state.prune();
      return transaction(database, databasePath, () => {
        const existing = database.prepare("SELECT reported_at, pending FROM incidents WHERE kind = ?").get(kind);
        if (existing !== undefined && (existing.pending || existing.reported_at + intervalMs > now())) return false;
        database.prepare(`INSERT INTO incidents (kind, reported_at, pending) VALUES (?, 0, 1)
          ON CONFLICT(kind) DO UPDATE SET pending = 1`).run(kind);
        return true;
      });
    },
    pendingIncidents() {
      return database.prepare("SELECT kind FROM incidents WHERE pending = 1 ORDER BY kind ASC").all().map(({ kind }) => kind);
    },
    markIncidentReported(kind) {
      transaction(database, databasePath, () => database.prepare("UPDATE incidents SET pending = 0, reported_at = ? WHERE kind = ?").run(now(), kind));
      state.prune();
    },
    counts() {
      return Object.fromEntries(["inbound_outbox", "inbound_routes", "outbound"].map((table) => [table, database.prepare(`SELECT COUNT(*) AS count FROM ${table}`).get().count]));
    },
  };
  state.prune();
  return state;
}
