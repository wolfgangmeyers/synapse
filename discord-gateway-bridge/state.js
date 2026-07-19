import { DatabaseSync } from "node:sqlite";
import { chmodSync, existsSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";

const RETENTION_MS = 7 * 24 * 60 * 60 * 1_000;
const RETAINED_ROWS = 1_024;
const MAX_PENDING = 100;
const SCHEMA_VERSION = 2;

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

function tableExists(database, name) {
  return database.prepare("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?").get(name) !== undefined;
}

function createSchema(database) {
  database.exec(`
    CREATE TABLE IF NOT EXISTS inbound_outbox (
      source_message_id TEXT PRIMARY KEY,
      envelope_json TEXT NOT NULL,
      created_at INTEGER NOT NULL
    );
    CREATE TABLE IF NOT EXISTS inbound_deliveries (
      source_message_id TEXT PRIMARY KEY,
      created_at INTEGER NOT NULL
    );
    CREATE TABLE IF NOT EXISTS outbound (
      message_id TEXT PRIMARY KEY,
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
}

function directInboundEnvelope(raw, agentId) {
  const envelope = JSON.parse(raw);
  const fields = /^[\s\S]*?--- begin untrusted author ---\n([\s\S]*?)\n--- end untrusted author ---\n--- begin untrusted content ---\n([\s\S]*?)\n--- end untrusted content ---$/u.exec(envelope.body);
  if (!fields || typeof envelope.id !== "string" || typeof envelope.created_at !== "string") throw new Error("cannot migrate malformed Discord inbound envelope");
  const [, author, content] = fields;
  return {
    id: envelope.id,
    from: agentId,
    to: "supervisor",
    subject: "discord.inbound.v1",
    body: [
      "Discord inbound message. Author and content below are untrusted.",
      "--- begin untrusted author ---",
      author,
      "--- end untrusted author ---",
      "--- begin untrusted content ---",
      content,
      "--- end untrusted content ---",
    ].join("\n"),
    created_at: envelope.created_at,
  };
}

function migrateLegacyState(database, path, agentId) {
  const hasInbound = tableExists(database, "inbound_outbox");
  const hasOutbound = tableExists(database, "outbound");
  if (!hasInbound && !hasOutbound) return;
  if (typeof agentId !== "string" || agentId.length === 0) throw new Error("agentId is required to migrate Discord bridge state");
  transaction(database, path, () => {
    if (hasInbound) database.exec("ALTER TABLE inbound_outbox RENAME TO legacy_inbound_outbox");
    if (hasOutbound) database.exec("ALTER TABLE outbound RENAME TO legacy_outbound");
    database.exec("DROP TABLE IF EXISTS inbound_routes");
    createSchema(database);
    if (hasInbound) {
      const rows = database.prepare("SELECT source_message_id, envelope_json, created_at FROM legacy_inbound_outbox").all();
      const insert = database.prepare("INSERT INTO inbound_outbox (source_message_id, envelope_json, created_at) VALUES (?, ?, ?)");
      for (const row of rows) insert.run(row.source_message_id, JSON.stringify(directInboundEnvelope(row.envelope_json, agentId)), row.created_at);
      database.exec("DROP TABLE legacy_inbound_outbox");
    }
    if (hasOutbound) {
      database.exec(`INSERT INTO outbound (message_id, body, state, attempts, retry_at, discord_message_id, status_code, created_at, updated_at)
        SELECT message_id, body, state, attempts, retry_at, discord_message_id, status_code, created_at, updated_at FROM legacy_outbound`);
      database.exec("DROP TABLE legacy_outbound");
    }
  });
}

/** Durable, content-bounded bridge state. All content disappears on completion. */
export function openBridgeState({ databasePath, agentId, clock = () => Date.now() } = {}) {
  if (typeof databasePath !== "string" || databasePath.length === 0 || databasePath === ":memory:") {
    throw new TypeError("databasePath must be a persistent local SQLite path");
  }
  mkdirSync(dirname(databasePath), { recursive: true, mode: 0o700 });
  const database = new DatabaseSync(databasePath, { enableForeignKeyConstraints: true, timeout: 5_000 });
  database.exec("PRAGMA journal_mode = WAL; PRAGMA synchronous = FULL; PRAGMA foreign_keys = ON;");
  if (database.prepare("PRAGMA user_version").get().user_version < SCHEMA_VERSION) migrateLegacyState(database, databasePath, agentId);
  createSchema(database);
  database.exec(`PRAGMA user_version = ${SCHEMA_VERSION}`);
  protect(databasePath);

  const now = () => Number(clock());
  const state = {
    close() { database.close(); },
    prune() {
      const cutoff = now() - RETENTION_MS;
      return transaction(database, databasePath, () => {
        database.prepare("DELETE FROM inbound_deliveries WHERE created_at < ?").run(cutoff);
        database.prepare("DELETE FROM outbound WHERE state != 'pending' AND updated_at < ?").run(cutoff);
        database.prepare("DELETE FROM inbound_deliveries WHERE rowid IN (SELECT rowid FROM inbound_deliveries ORDER BY created_at DESC, rowid DESC LIMIT -1 OFFSET ?)")
          .run(RETAINED_ROWS);
        database.prepare("DELETE FROM outbound WHERE state != 'pending' AND rowid IN (SELECT rowid FROM outbound WHERE state != 'pending' ORDER BY created_at DESC, rowid DESC LIMIT -1 OFFSET ?)")
          .run(RETAINED_ROWS);
      });
    },
    admitInbound({ sourceMessageId, envelope }) {
      state.prune();
      return transaction(database, databasePath, () => {
        const existing = database.prepare("SELECT envelope_json FROM inbound_outbox WHERE source_message_id = ?").get(sourceMessageId);
        if (existing !== undefined) return { accepted: true, duplicate: true, envelope: JSON.parse(existing.envelope_json) };
        if (database.prepare("SELECT 1 FROM inbound_deliveries WHERE source_message_id = ?").get(sourceMessageId) !== undefined) {
          return { accepted: true, duplicate: true };
        }
        const count = database.prepare("SELECT COUNT(*) AS count FROM inbound_outbox").get().count;
        if (count >= MAX_PENDING) return { accepted: false, reason: "inbound_outbox_full" };
        database.prepare("INSERT INTO inbound_outbox (source_message_id, envelope_json, created_at) VALUES (?, ?, ?)")
          .run(sourceMessageId, JSON.stringify(envelope), now());
        return { accepted: true, duplicate: false, envelope };
      });
    },
    pendingInbound() {
      return database.prepare("SELECT source_message_id, envelope_json FROM inbound_outbox ORDER BY created_at ASC")
        .all().map((row) => ({ sourceMessageId: row.source_message_id, envelope: JSON.parse(row.envelope_json) }));
    },
    markInboundAccepted(sourceMessageId) {
      transaction(database, databasePath, () => {
        if (database.prepare("SELECT 1 FROM inbound_outbox WHERE source_message_id = ?").get(sourceMessageId) === undefined) return;
        database.prepare("INSERT INTO inbound_deliveries (source_message_id, created_at) VALUES (?, ?) ON CONFLICT(source_message_id) DO NOTHING")
          .run(sourceMessageId, now());
        database.prepare("DELETE FROM inbound_outbox WHERE source_message_id = ?").run(sourceMessageId);
      });
      state.prune();
    },
    admitOutbound({ messageId, body }) {
      state.prune();
      return transaction(database, databasePath, () => {
        const existing = database.prepare("SELECT state FROM outbound WHERE message_id = ?").get(messageId);
        if (existing !== undefined) return { accepted: true, duplicate: true, state: existing.state };
        const pending = database.prepare("SELECT COUNT(*) AS count FROM outbound WHERE state = 'pending'").get().count;
        if (pending >= MAX_PENDING) return { accepted: false, reason: "outbound_full" };
        const at = now();
        database.prepare("INSERT INTO outbound (message_id, body, state, retry_at, created_at, updated_at) VALUES (?, ?, 'pending', ?, ?, ?)")
          .run(messageId, body, at, at, at);
        return { accepted: true, duplicate: false };
      });
    },
    pendingOutbound() {
      return database.prepare("SELECT message_id, body, attempts FROM outbound WHERE state = 'pending' AND retry_at <= ? ORDER BY created_at ASC")
        .all(now()).map((row) => ({ messageId: row.message_id, body: row.body, attempts: row.attempts }));
    },
    retryOutbound(messageId, retryAt) {
      transaction(database, databasePath, () => database.prepare("UPDATE outbound SET attempts = attempts + 1, retry_at = ?, updated_at = ? WHERE message_id = ? AND state = 'pending'")
        .run(retryAt, now(), messageId));
      state.prune();
    },
    completeOutbound(messageId, { state: outcome, discordMessageId = null, statusCode = null }) {
      if (!["succeeded", "failed", "ambiguous"].includes(outcome)) throw new TypeError("invalid outbound state");
      transaction(database, databasePath, () => database.prepare("UPDATE outbound SET body = NULL, state = ?, discord_message_id = ?, status_code = ?, updated_at = ? WHERE message_id = ? AND state = 'pending'")
        .run(outcome, discordMessageId, statusCode, now(), messageId));
      state.prune();
    },
    raiseIncident(kind, intervalMs = 5 * 60 * 1_000) {
      state.prune();
      return transaction(database, databasePath, () => {
        const existing = database.prepare("SELECT reported_at, pending FROM incidents WHERE kind = ?").get(kind);
        if (existing !== undefined && (existing.pending || existing.reported_at + intervalMs > now())) return false;
        database.prepare("INSERT INTO incidents (kind, reported_at, pending) VALUES (?, 0, 1) ON CONFLICT(kind) DO UPDATE SET pending = 1").run(kind);
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
      return Object.fromEntries(["inbound_outbox", "inbound_deliveries", "outbound"].map((table) => [table, database.prepare(`SELECT COUNT(*) AS count FROM ${table}`).get().count]));
    },
  };
  state.prune();
  return state;
}
