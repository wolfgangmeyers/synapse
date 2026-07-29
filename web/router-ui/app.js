localStorage.removeItem("mecha.web.messages");
localStorage.removeItem("mecha.web.conversations");

const HISTORY_LIMIT = 200;
const HISTORY_RETENTION_DAYS = 30;
const HISTORY_RETENTION_MS = HISTORY_RETENTION_DAYS * 24 * 60 * 60 * 1000;
const HISTORY_DB_NAME = "mecha-router-web-ui";
const HISTORY_DB_VERSION = 1;
const HISTORY_STORE_NAME = "conversation_messages";
const SCOPE_SEPARATOR = "\u001f";

const initialRouter = localStorage.getItem("mecha.web.activeRouter") || "http://127.0.0.1:8765";
const state = {
  socket: null,
  defaultRouter: "http://127.0.0.1:8765",
  routerUrl: initialRouter,
  agentId: agentIdForRouter(initialRouter),
  routers: loadRouters(),
  agents: [],
  selectedPeer: "",
  messages: {},
};

const elements = {
  statusDot: document.querySelector("#status-dot"),
  statusTitle: document.querySelector("#status-title"),
  statusDetail: document.querySelector("#status-detail"),
  selectedRouterLabel: document.querySelector("#selected-router-label"),
  routerSelector: document.querySelector("#router-selector"),
  routerDialog: document.querySelector("#router-dialog"),
  savedRouters: document.querySelector("#saved-routers"),
  routerUrl: document.querySelector("#router-url"),
  agentId: document.querySelector("#agent-id"),
  addRouter: document.querySelector("#add-router"),
  refresh: document.querySelector("#refresh"),
  agents: document.querySelector("#agents"),
  agentCount: document.querySelector("#agent-count"),
  peerAvatar: document.querySelector("#peer-avatar"),
  peerName: document.querySelector("#peer-name"),
  peerDetail: document.querySelector("#peer-detail"),
  messages: document.querySelector("#messages"),
  composer: document.querySelector("#composer"),
  draft: document.querySelector("#draft"),
  send: document.querySelector("#send"),
};

const config = await fetch("/ui/config").then((response) => response.json()).catch(() => ({}));
state.defaultRouter = config.defaultRouter || state.defaultRouter;
if (!localStorage.getItem("mecha.web.activeRouter")) {
  state.routerUrl = state.defaultRouter;
  state.agentId = agentIdForRouter(state.routerUrl);
}
ensureRouter(state.routerUrl);
state.messages = await loadMessages(state.routerUrl, state.agentId);
elements.routerUrl.value = state.routerUrl;
elements.agentId.value = state.agentId;
connectUiSocket();
render();

void pruneExpiredHistory();

elements.routerSelector.addEventListener("click", () => {
  renderRouters();
  elements.routerDialog.showModal();
});
elements.addRouter.addEventListener("click", () => {
  try {
    const routerUrl = normalizeRouterInput(elements.routerUrl.value);
    state.agentId = elements.agentId.value.trim();
    saveAgentIdForRouter(routerUrl, state.agentId);
    ensureRouter(routerUrl);
    void switchRouter(routerUrl, { connect: true });
  } catch (error) {
    setStatus("error", error.message);
  }
});
elements.refresh.addEventListener("click", () => sendCommand({ type: "refresh_agents" }));
elements.composer.addEventListener("submit", (event) => {
  event.preventDefault();
  sendDraft();
});
elements.draft.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    sendDraft();
  }
});

function connectUiSocket() {
  state.socket?.close();
  const protocol = location.protocol === "https:" ? "wss:" : "ws:";
  state.socket = new WebSocket(`${protocol}//${location.host}/ui/events`);
  state.socket.addEventListener("open", () => sendCommand({ type: "connect", routerUrl: state.routerUrl, agentId: state.agentId }));
  state.socket.addEventListener("message", (event) => { void handleEvent(JSON.parse(event.data)); });
  state.socket.addEventListener("close", () => {
    setStatus("reconnecting", "UI server connection closed; retrying…");
    setTimeout(connectUiSocket, 1000);
  });
}

function sendCommand(command) {
  if (state.socket?.readyState === WebSocket.OPEN) state.socket.send(JSON.stringify(command));
}

async function handleEvent(event) {
  if (event.type === "ready") return;
  if (!matchesActiveScope(event)) return;
  if (event.type === "status") {
    setStatus(event.state, event.detail, event.agentId, event.routerUrl);
    return;
  }
  if (event.type === "agents") {
    state.agents = event.agents.filter((agent) => agent.agent_id !== state.agentId);
    renderAgents();
    return;
  }
  if (event.type === "agents_error" || event.type === "command_error") {
    setStatus("error", event.message);
    return;
  }
  if (event.type === "message") {
    const message = { ...event.message, direction: "incoming" };
    await appendMessage(message.from, message);
    sendCommand({ type: "message_received", deliveryToken: event.deliveryToken });
    if (!state.selectedPeer) selectPeer(message.from);
    return;
  }
  if (event.type === "send_result") await settleOutgoing(event);
}

async function switchRouter(routerUrl, { connect = false } = {}) {
  state.routerUrl = routerUrl;
  state.agentId = agentIdForRouter(routerUrl);
  state.agents = [];
  state.selectedPeer = "";
  state.messages = await loadMessages(routerUrl, state.agentId);
  localStorage.setItem("mecha.web.activeRouter", routerUrl);
  elements.routerUrl.value = routerUrl;
  elements.agentId.value = state.agentId;
  render();
  if (connect) {
    sendCommand({ type: "connect", routerUrl, agentId: state.agentId });
    elements.routerDialog.close();
  }
}

function ensureRouter(routerUrl) {
  if (!state.routers.includes(routerUrl)) {
    state.routers.push(routerUrl);
    saveRouters();
  }
}

function removeRouter(routerUrl) {
  if (state.routers.length === 1) return;
  state.routers = state.routers.filter((router) => router !== routerUrl);
  if (state.routerUrl === routerUrl) void switchRouter(state.routers[0], { connect: true });
  saveRouters();
  renderRouters();
}

function setStatus(stateName, detail, agentId = state.agentId, routerUrl = state.routerUrl) {
  elements.statusDot.className = `status-dot ${stateName === "connected" ? "connected" : stateName === "connecting" || stateName === "reconnecting" ? "connecting" : "offline"}`;
  elements.statusTitle.textContent = stateName === "connected" ? `Connected as ${agentId}` : stateName[0].toUpperCase() + stateName.slice(1);
  elements.statusDetail.textContent = `${detail || ""}${routerUrl ? ` · ${routerUrl}` : ""}`;
}

function render() {
  elements.selectedRouterLabel.textContent = displayRouter(state.routerUrl);
  renderRouters();
  renderAgents();
  renderConversation();
}

function renderRouters() {
  elements.savedRouters.replaceChildren(...state.routers.map((routerUrl) => {
    const row = document.createElement("div");
    row.className = `router-row${routerUrl === state.routerUrl ? " active" : ""}`;
    row.tabIndex = 0;
    row.setAttribute("role", "button");
    row.addEventListener("click", () => { void switchRouter(routerUrl, { connect: true }); });
    row.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        void switchRouter(routerUrl, { connect: true });
      }
    });
    const label = document.createElement("div");
    label.innerHTML = `<strong>${escapeHtml(displayRouter(routerUrl))}</strong><div class="meta">${escapeHtml(routerUrl)}${routerUrl === state.routerUrl ? " · selected" : ""}</div>`;
    const select = document.createElement("button");
    select.type = "button";
    select.className = "secondary";
    select.textContent = "Connect as user";
    select.addEventListener("click", (event) => {
      event.stopPropagation();
      void switchRouter(routerUrl, { connect: true });
    });
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "danger";
    remove.textContent = "Remove";
    remove.disabled = state.routers.length === 1;
    remove.addEventListener("click", (event) => {
      event.stopPropagation();
      removeRouter(routerUrl);
    });
    row.append(label, select, remove);
    return row;
  }));
}

function renderAgents() {
  const known = new Set(state.agents.map((agent) => agent.agent_id));
  for (const peer of Object.keys(state.messages)) known.add(peer);
  const peers = [...known].filter((peer) => peer && peer !== state.agentId).sort();
  elements.agentCount.textContent = String(peers.length);
  elements.agents.replaceChildren(...peers.map((peer) => {
    const connected = state.agents.some((agent) => agent.agent_id === peer);
    const row = document.createElement("button");
    row.type = "button";
    row.className = `agent-row${peer === state.selectedPeer ? " active" : ""}`;
    row.innerHTML = `<span class="avatar">${escapeHtml(monogram(peer))}</span><span><strong>${escapeHtml(humanName(peer))}</strong><span class="meta">${escapeHtml(peer)} · ${connected ? "Available" : "History"}</span></span>`;
    row.addEventListener("click", () => selectPeer(peer));
    return row;
  }));
}

function selectPeer(peer) {
  state.selectedPeer = peer;
  render();
}

function sendDraft() {
  const body = elements.draft.value;
  if (!state.selectedPeer || !body.trim()) return;
  const localId = crypto.randomUUID();
  const message = { id: localId, from: state.agentId, to: state.selectedPeer, body, created_at: new Date().toISOString(), direction: "outgoing", pending: true };
  void appendMessage(state.selectedPeer, message);
  elements.draft.value = "";
  sendCommand({ type: "send_message", to: state.selectedPeer, body, localId });
}

function renderConversation() {
  const peer = state.selectedPeer;
  elements.peerAvatar.textContent = peer ? monogram(peer) : "?";
  elements.peerAvatar.className = `avatar${peer ? "" : " muted"}`;
  elements.peerName.textContent = peer ? humanName(peer) : "Pick an agent";
  elements.peerDetail.textContent = peer ? `${peer} · ${displayRouter(state.routerUrl)}` : "Connected agents appear on the left.";
  elements.draft.disabled = !peer;
  elements.send.disabled = !peer;
  if (!peer) {
    elements.messages.innerHTML = `<div class="empty-state"><h3>No conversation selected</h3><p>Choose a connected agent on ${escapeHtml(displayRouter(state.routerUrl))}, or open Routers to switch.</p></div>`;
    return;
  }
  const messages = state.messages[peer] || [];
  if (messages.length === 0) {
    elements.messages.innerHTML = `<div class="empty-state"><h3>Start a conversation with ${escapeHtml(humanName(peer))}</h3><p>Messages use ${escapeHtml(displayRouter(state.routerUrl))}.</p></div>`;
    return;
  }
  elements.messages.replaceChildren(...messages.map(renderMessage));
  elements.messages.scrollTop = elements.messages.scrollHeight;
}

function renderMessage(message) {
  const outgoing = message.direction === "outgoing" || message.from === state.agentId;
  const wrapper = document.createElement("div");
  wrapper.className = `bubble-wrap ${outgoing ? "outgoing" : "incoming"}`;
  const label = document.createElement("div");
  label.className = "bubble-label";
  label.textContent = outgoing ? (message.pending ? "You · sending" : "You") : humanName(message.from);
  const bubble = document.createElement("div");
  bubble.className = "bubble";
  bubble.textContent = message.body;
  wrapper.append(label, bubble);
  return wrapper;
}

async function appendMessage(peer, message) {
  state.messages[peer] = pruneHistoryMessages(mergeMessages(state.messages[peer] || [], [message]));
  render();
  await savePeerMessages(peer);
}

async function settleOutgoing(event) {
  const touchedPeers = new Set();
  for (const peer of Object.keys(state.messages)) {
    const updated = state.messages[peer].map((message) => message.id === event.localId ? { ...message, pending: false, failed: !event.ok } : message);
    if (updated.some((message, index) => message !== state.messages[peer][index])) touchedPeers.add(peer);
    state.messages[peer] = updated;
  }
  if (event.ok && event.message?.to) {
    state.messages[event.message.to] = pruneHistoryMessages(mergeMessages(
      (state.messages[event.message.to] || []).filter((message) => message.id !== event.localId),
      [{ ...event.message, direction: "outgoing" }],
    ));
    touchedPeers.add(event.message.to);
  }
  await Promise.all([...touchedPeers].map((peer) => savePeerMessages(peer)));
  if (!event.ok) setStatus("error", event.error || "Message not sent");
  renderConversation();
}

function mergeMessages(existing, incoming) {
  const byId = new Map(existing.map((message) => [message.id, message]));
  for (const message of incoming) byId.set(message.id, { ...byId.get(message.id), ...message });
  return [...byId.values()].sort((left, right) => messageTime(left) - messageTime(right));
}

function pruneHistoryMessages(messages, now = Date.now()) {
  const cutoff = now - HISTORY_RETENTION_MS;
  return messages
    .filter((message) => messageTime(message) >= cutoff)
    .sort((left, right) => messageTime(left) - messageTime(right))
    .slice(-HISTORY_LIMIT);
}

async function loadMessages(routerUrl, agentId) {
  const db = await openHistoryDb();
  const records = await getAllFromIndex(db, "ownerScope", ownerScope(routerUrl, agentId));
  const messages = {};
  for (const record of records) {
    if (!messages[record.peer]) messages[record.peer] = [];
    messages[record.peer].push(record.message);
  }
  for (const [peer, peerMessages] of Object.entries(messages)) {
    const pruned = pruneHistoryMessages(peerMessages);
    messages[peer] = pruned;
    if (pruned.length !== peerMessages.length) await saveRecordsForPeer(db, routerUrl, agentId, peer, pruned);
  }
  return messages;
}

async function savePeerMessages(peer) {
  const db = await openHistoryDb();
  state.messages[peer] = pruneHistoryMessages(state.messages[peer] || []);
  await saveRecordsForPeer(db, state.routerUrl, state.agentId, peer, state.messages[peer]);
}

async function saveRecordsForPeer(db, routerUrl, agentId, peer, messages) {
  const peerScopeValue = peerScope(routerUrl, agentId, peer);
  const existing = await getAllKeysFromIndex(db, "peerScope", peerScopeValue);
  const transaction = db.transaction(HISTORY_STORE_NAME, "readwrite");
  const store = transaction.objectStore(HISTORY_STORE_NAME);
  for (const key of existing) store.delete(key);
  for (const message of messages) {
    const createdAtMs = messageTime(message);
    const key = `${peerScopeValue}${SCOPE_SEPARATOR}${message.id || crypto.randomUUID()}`;
    store.put({ key, ownerScope: ownerScope(routerUrl, agentId), peerScope: peerScopeValue, routerUrl, agentId, peer, message, createdAtMs, savedAtMs: Date.now() });
  }
  await requestDone(transaction);
}

async function pruneExpiredHistory() {
  const db = await openHistoryDb();
  const cutoff = Date.now() - HISTORY_RETENTION_MS;
  const transaction = db.transaction(HISTORY_STORE_NAME, "readwrite");
  const index = transaction.objectStore(HISTORY_STORE_NAME).index("createdAtMs");
  const request = index.openCursor(IDBKeyRange.upperBound(cutoff, true));
  request.onsuccess = () => {
    const cursor = request.result;
    if (!cursor) return;
    cursor.delete();
    cursor.continue();
  };
  await requestDone(transaction);
}

function openHistoryDb() {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(HISTORY_DB_NAME, HISTORY_DB_VERSION);
    request.onupgradeneeded = () => {
      const db = request.result;
      const store = db.objectStoreNames.contains(HISTORY_STORE_NAME)
        ? request.transaction.objectStore(HISTORY_STORE_NAME)
        : db.createObjectStore(HISTORY_STORE_NAME, { keyPath: "key" });
      if (!store.indexNames.contains("ownerScope")) store.createIndex("ownerScope", "ownerScope", { unique: false });
      if (!store.indexNames.contains("peerScope")) store.createIndex("peerScope", "peerScope", { unique: false });
      if (!store.indexNames.contains("createdAtMs")) store.createIndex("createdAtMs", "createdAtMs", { unique: false });
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function getAllFromIndex(db, indexName, value) {
  return new Promise((resolve, reject) => {
    const request = db.transaction(HISTORY_STORE_NAME, "readonly").objectStore(HISTORY_STORE_NAME).index(indexName).getAll(value);
    request.onsuccess = () => resolve(request.result || []);
    request.onerror = () => reject(request.error);
  });
}

function getAllKeysFromIndex(db, indexName, value) {
  return new Promise((resolve, reject) => {
    const request = db.transaction(HISTORY_STORE_NAME, "readonly").objectStore(HISTORY_STORE_NAME).index(indexName).getAllKeys(value);
    request.onsuccess = () => resolve(request.result || []);
    request.onerror = () => reject(request.error);
  });
}

function requestDone(transaction) {
  return new Promise((resolve, reject) => {
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error);
    transaction.onabort = () => reject(transaction.error || new Error("IndexedDB transaction aborted"));
  });
}

function ownerScope(routerUrl, agentId) { return `${routerUrl}${SCOPE_SEPARATOR}${agentId}`; }
function peerScope(routerUrl, agentId, peer) { return `${ownerScope(routerUrl, agentId)}${SCOPE_SEPARATOR}${peer}`; }
function messageTime(message) { return Date.parse(message.created_at || message.createdAt || "") || Date.now(); }

function normalizeRouterInput(value) {
  const candidate = value.trim();
  const withScheme = /^[a-z]+:\/\//i.test(candidate) ? candidate : `http://${candidate}`;
  const url = new URL(withScheme);
  if (!["http:", "https:"].includes(url.protocol)) throw new Error("Router URL must start with http:// or https://");
  if (url.username !== "" || url.password !== "") throw new Error("Router URL must not include credentials");
  url.pathname = "";
  url.search = "";
  url.hash = "";
  return url.toString().replace(/\/$/, "");
}
function displayRouter(routerUrl) { return new URL(routerUrl).host; }
function loadRouters() {
  try { return JSON.parse(localStorage.getItem("mecha.web.routers") || "[]"); } catch { return []; }
}
function saveRouters() { localStorage.setItem("mecha.web.routers", JSON.stringify(state.routers)); }
function matchesActiveScope(event) {
  if (!["agents", "agents_error", "message", "send_result", "status"].includes(event.type)) return true;
  return event.routerUrl === state.routerUrl && event.agentId === state.agentId;
}
function loadAgentIds() {
  try { return JSON.parse(localStorage.getItem("mecha.web.agentIds") || "{}"); } catch { return {}; }
}
function agentIdForRouter(routerUrl) { return loadAgentIds()[routerUrl] || localStorage.getItem("mecha.web.agentId") || "wolfgang-web"; }
function saveAgentIdForRouter(routerUrl, agentId) {
  const agentIds = loadAgentIds();
  agentIds[routerUrl] = agentId;
  localStorage.setItem("mecha.web.agentIds", JSON.stringify(agentIds));
  localStorage.setItem("mecha.web.agentId", agentId);
}
function humanName(id) { return id.split(/[._-]+/).filter(Boolean).map((part) => part[0].toUpperCase() + part.slice(1)).join(" ") || id; }
function monogram(id) { return (id.match(/[A-Za-z0-9]/)?.[0] || "?").toUpperCase(); }
function escapeHtml(value) {
  return String(value).replace(/[&<>'"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" }[char]));
}
