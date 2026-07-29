localStorage.removeItem("mecha.web.messages");
localStorage.removeItem("mecha.web.conversations");

const HISTORY_LIMIT = 200;
const HISTORY_RETENTION_DAYS = 30;
const HISTORY_RETENTION_MS = HISTORY_RETENTION_DAYS * 24 * 60 * 60 * 1000;
const HISTORY_DB_NAME = "mecha-router-web-ui";
const HISTORY_DB_VERSION = 2;
const HISTORY_STORE_NAME = "conversation_messages";
const IMAGE_STORE_NAME = "image_attachments";
const DRAFT_STORE_KEY = "mecha.web.drafts";
const DRAFT_LIMIT = 200;
const DRAFT_RETENTION_DAYS = 30;
const DRAFT_RETENTION_MS = DRAFT_RETENTION_DAYS * 24 * 60 * 60 * 1000;
const SELECTED_PEER_STORE_KEY = "mecha.web.selectedPeers";
const MAX_IMAGES = 3;
const MAX_IMAGE_EDGE = 1600;
const IMAGE_WEBP_QUALITY = 0.82;
const MAX_IMAGE_BYTES = 768 * 1024;
const MAX_TOTAL_IMAGE_BYTES = MAX_IMAGES * MAX_IMAGE_BYTES;
const HISTORY_IMAGE_EDGE = 360;
const HISTORY_IMAGE_QUALITY = 0.72;
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
  agentFilter: "",
  images: [],
  messages: {},
  draftPeers: new Set(),
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
  agentFilter: document.querySelector("#agent-filter"),
  agents: document.querySelector("#agents"),
  agentCount: document.querySelector("#agent-count"),
  peerAvatar: document.querySelector("#peer-avatar"),
  peerName: document.querySelector("#peer-name"),
  peerDetail: document.querySelector("#peer-detail"),
  messages: document.querySelector("#messages"),
  composer: document.querySelector("#composer"),
  imagePreview: document.querySelector("#image-preview"),
  attachImage: document.querySelector("#attach-image"),
  imageInput: document.querySelector("#image-input"),
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
state.draftPeers = loadDraftPeers(state.routerUrl, state.agentId);
state.selectedPeer = loadSelectedPeer(state.routerUrl, state.agentId);
elements.routerUrl.value = state.routerUrl;
elements.agentId.value = state.agentId;
connectUiSocket();
render();
restoreDraftForSelectedPeer();

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
elements.agentFilter.addEventListener("input", () => {
  state.agentFilter = elements.agentFilter.value;
  renderAgents();
});
elements.composer.addEventListener("submit", (event) => {
  event.preventDefault();
  sendDraft();
});
elements.draft.addEventListener("input", () => { saveCurrentDraft(); updateSendState(); });
elements.attachImage.addEventListener("click", () => elements.imageInput.click());
elements.imageInput.addEventListener("change", () => { void addImageFiles(elements.imageInput.files); elements.imageInput.value = ""; });
elements.composer.addEventListener("dragover", (event) => {
  if (!hasImageFiles(event.dataTransfer?.items)) return;
  event.preventDefault();
  elements.composer.classList.add("dragging");
});
elements.composer.addEventListener("dragleave", () => elements.composer.classList.remove("dragging"));
elements.composer.addEventListener("drop", (event) => {
  if (!hasImageFiles(event.dataTransfer?.items)) return;
  event.preventDefault();
  elements.composer.classList.remove("dragging");
  void addImageFiles(event.dataTransfer.files);
});
elements.draft.addEventListener("paste", (event) => {
  if (!hasImageFiles(event.clipboardData?.items)) return;
  void addImageFiles(event.clipboardData.files);
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
  saveCurrentDraft();
  state.routerUrl = routerUrl;
  state.agentId = agentIdForRouter(routerUrl);
  state.agents = [];
  state.selectedPeer = loadSelectedPeer(routerUrl, state.agentId);
  state.messages = await loadMessages(routerUrl, state.agentId);
  state.draftPeers = loadDraftPeers(routerUrl, state.agentId);
  localStorage.setItem("mecha.web.activeRouter", routerUrl);
  elements.routerUrl.value = routerUrl;
  elements.agentId.value = state.agentId;
  render();
  restoreDraftForSelectedPeer();
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
  elements.statusDetail.textContent = stateName === "connected" ? "" : `${detail || ""}${routerUrl ? ` · ${routerUrl}` : ""}`;
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
  const filter = state.agentFilter.trim().toLowerCase();
  const peers = state.agents
    .map((agent) => agent.agent_id)
    .filter((peer) => peer && peer !== state.agentId)
    .filter((peer) => !filter || `${peer} ${humanName(peer)}`.toLowerCase().includes(filter))
    .sort();
  elements.agentCount.textContent = String(peers.length);
  elements.agents.replaceChildren(...peers.map((peer) => {
    const row = document.createElement("button");
    row.type = "button";
    row.className = `agent-row${peer === state.selectedPeer ? " active" : ""}`;
    row.innerHTML = `<span class="avatar">${escapeHtml(monogram(peer))}</span><span><strong>${escapeHtml(humanName(peer))}</strong><span class="meta">${escapeHtml(peer)} · Available</span></span>`;
    row.addEventListener("click", () => selectPeer(peer));
    return row;
  }));
}

function selectPeer(peer) {
  state.agentFilter = "";
  elements.agentFilter.value = "";
  if (peer === state.selectedPeer) {
    renderAgents();
    restoreDraftForSelectedPeer({ focus: true });
    return;
  }
  saveCurrentDraft();
  state.selectedPeer = peer;
  saveSelectedPeer(peer);
  render();
  restoreDraftForSelectedPeer({ focus: true });
}

function sendDraft() {
  const body = elements.draft.value;
  if (!state.selectedPeer || (body.trim() === "" && state.images.length === 0)) return;
  const localId = crypto.randomUUID();
  const images = state.images.map(({ dataUrl, name, sizeBytes, mimeType }) => ({ dataUrl, name, sizeBytes, mimeType }));
  const message = { id: localId, from: state.agentId, to: state.selectedPeer, body: pendingMessageBody(body, images), attachments: images.map((image) => ({ url: image.dataUrl, mime_type: image.mimeType, size_bytes: image.sizeBytes, name: image.name })), created_at: new Date().toISOString(), direction: "outgoing", pending: true };
  void appendMessage(state.selectedPeer, message);
  elements.draft.value = "";
  state.images = [];
  renderImagePreview();
  clearCurrentDraft();
  updateSendState();
  sendCommand({ type: "send_message", to: state.selectedPeer, body, images, localId });
}

function renderConversation() {
  const peer = state.selectedPeer;
  elements.peerAvatar.textContent = peer ? monogram(peer) : "?";
  elements.peerAvatar.className = `avatar${peer ? "" : " muted"}`;
  elements.peerName.textContent = peer ? humanName(peer) : "Pick an agent";
  elements.peerDetail.textContent = peer ? `${peer} · ${displayRouter(state.routerUrl)}` : "Connected agents appear on the left.";
  elements.draft.disabled = !peer;
  elements.attachImage.disabled = !peer;
  updateSendState();
  if (!peer) {
    elements.draft.value = "";
    state.images = [];
    renderImagePreview();
    elements.messages.innerHTML = `<div class="empty-state"><h3>No conversation selected</h3><p>Choose a connected agent on ${escapeHtml(displayRouter(state.routerUrl))}, or open Routers to switch.</p></div>`;
    return;
  }
  const messages = state.messages[peer] || [];
  if (messages.length === 0) {
    elements.messages.innerHTML = `<div class="empty-state"><h3>Start a conversation with ${escapeHtml(humanName(peer))}</h3><p>Messages use ${escapeHtml(displayRouter(state.routerUrl))}.</p></div>`;
    return;
  }
  elements.messages.replaceChildren(...messages.map(renderMessage));
  scrollMessagesToBottom();
}

function scrollMessagesToBottom() {
  elements.messages.scrollTop = elements.messages.scrollHeight;
  requestAnimationFrame(() => { elements.messages.scrollTop = elements.messages.scrollHeight; });
  setTimeout(() => { elements.messages.scrollTop = elements.messages.scrollHeight; }, 100);
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
  const text = document.createElement("div");
  text.textContent = message.body;
  bubble.append(text);
  const attachments = message.attachments || imageAttachmentsFromBody(message.body);
  if (attachments.length > 0) bubble.append(renderMessageImages(attachments));
  wrapper.append(label, bubble);
  return wrapper;
}

function updateSendState() {
  elements.send.disabled = !state.selectedPeer || (elements.draft.value.trim() === "" && state.images.length === 0);
}

async function addImageFiles(fileList) {
  const files = [...(fileList || [])].filter((file) => file.type.startsWith("image/"));
  for (const file of files) {
    if (state.images.length >= MAX_IMAGES) {
      setStatus("error", `You can attach up to ${MAX_IMAGES} images`);
      break;
    }
    try {
      const image = await prepareImage(file);
      const totalBytes = state.images.reduce((sum, current) => sum + current.sizeBytes, 0) + image.sizeBytes;
      if (totalBytes > MAX_TOTAL_IMAGE_BYTES) throw new Error("Attached images are too large together");
      state.images.push(image);
    } catch (error) {
      setStatus("error", error.message || "Image could not be attached");
    }
  }
  renderImagePreview();
  updateSendState();
}

async function prepareImage(file) {
  const bitmap = await createImageBitmap(file);
  const scale = Math.min(1, MAX_IMAGE_EDGE / Math.max(bitmap.width, bitmap.height));
  const width = Math.max(1, Math.round(bitmap.width * scale));
  const height = Math.max(1, Math.round(bitmap.height * scale));
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  canvas.getContext("2d").drawImage(bitmap, 0, 0, width, height);
  bitmap.close?.();
  const blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/webp", IMAGE_WEBP_QUALITY));
  if (!blob) throw new Error("Image could not be converted");
  if (blob.size > MAX_IMAGE_BYTES) throw new Error("Image is too large after compression");
  return { dataUrl: await blobToDataUrl(blob), name: webpName(file.name), sizeBytes: blob.size, mimeType: "image/webp", width, height };
}

function renderImagePreview() {
  elements.imagePreview.replaceChildren(...state.images.map((image, index) => {
    const card = document.createElement("div");
    card.className = "image-card";
    const img = document.createElement("img");
    img.src = image.dataUrl;
    img.alt = image.name || `Image ${index + 1}`;
    const label = document.createElement("small");
    label.textContent = `${image.name || `image-${index + 1}.webp`} · ${formatBytes(image.sizeBytes)}`;
    const remove = document.createElement("button");
    remove.type = "button";
    remove.textContent = "×";
    remove.setAttribute("aria-label", `Remove ${image.name || `image ${index + 1}`}`);
    remove.addEventListener("click", () => {
      state.images.splice(index, 1);
      renderImagePreview();
      updateSendState();
    });
    card.append(img, label, remove);
    return card;
  }));
}

function renderMessageImages(attachments) {
  const gallery = document.createElement("div");
  gallery.className = "bubble-images";
  gallery.append(...attachments.slice(0, MAX_IMAGES).map((attachment, index) => {
    const image = document.createElement("img");
    image.src = attachment.url || "";
    image.alt = attachment.name || `Attached image ${index + 1}`;
    image.addEventListener("load", scrollMessagesToBottom, { once: true });
    if (attachment.cacheKey) {
      void cachedImageUrl(attachment.cacheKey).then((url) => { if (url) image.src = url; });
    }
    image.addEventListener("click", () => window.open(image.src || attachment.url, "_blank", "noopener"));
    return image;
  }));
  return gallery;
}

function imageAttachmentsFromBody(body) {
  const matches = [...String(body || "").matchAll(/\/v1\/attachments\/([A-Za-z0-9._-]+\.(?:png|jpg|webp|gif))/g)];
  return matches.map((match) => ({ url: `${state.routerUrl}${match[0]}`, name: match[1] }));
}

function pendingMessageBody(body, images) {
  const text = body.trim();
  if (images.length === 0) return body;
  return `${text}${text ? "\n\n" : ""}Attached image${images.length === 1 ? "" : "s"}:\n${images.map((image, index) => `${index + 1}. ${image.name || "image.webp"}`).join("\n")}`;
}

function hasImageFiles(items) {
  return [...(items || [])].some((item) => item.type?.startsWith("image/"));
}

function blobToDataUrl(blob) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(blob);
  });
}

function webpName(name) {
  const base = String(name || "image").replace(/\.[^.]*$/, "") || "image";
  return `${base}.webp`;
}

function formatBytes(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

async function messageWithCachedImages(peer, acceptedMessage, pendingMessage) {
  const pendingAttachments = pendingMessage?.attachments || [];
  const attachments = await Promise.all((acceptedMessage.attachments || []).map(async (attachment, index) => {
    const pendingUrl = pendingAttachments[index]?.url || "";
    if (!pendingUrl.startsWith("data:image/")) return attachment;
    const cacheKey = `${peerScope(state.routerUrl, state.agentId, peer)}${SCOPE_SEPARATOR}${acceptedMessage.id}${SCOPE_SEPARATOR}${attachment.id || index}`;
    await saveCachedImage(cacheKey, pendingUrl, attachment);
    return { ...attachment, cacheKey };
  }));
  return { ...acceptedMessage, attachments, direction: "outgoing" };
}

async function saveCachedImage(key, dataUrl, attachment) {
  const blob = await historyThumbnailBlob(dataUrl);
  const db = await openHistoryDb();
  const transaction = db.transaction(IMAGE_STORE_NAME, "readwrite");
  transaction.objectStore(IMAGE_STORE_NAME).put({ key, blob, mime_type: "image/webp", name: attachment.name, savedAtMs: Date.now() });
  await requestDone(transaction);
}

async function historyThumbnailBlob(dataUrl) {
  const sourceBlob = await (await fetch(dataUrl)).blob();
  const bitmap = await createImageBitmap(sourceBlob);
  const scale = Math.min(1, HISTORY_IMAGE_EDGE / Math.max(bitmap.width, bitmap.height));
  const width = Math.max(1, Math.round(bitmap.width * scale));
  const height = Math.max(1, Math.round(bitmap.height * scale));
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  canvas.getContext("2d").drawImage(bitmap, 0, 0, width, height);
  bitmap.close?.();
  const blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/webp", HISTORY_IMAGE_QUALITY));
  if (!blob) throw new Error("Image history preview could not be saved");
  return blob;
}

async function cachedImageUrl(key) {
  try {
    const db = await openHistoryDb();
    const record = await new Promise((resolve, reject) => {
      const request = db.transaction(IMAGE_STORE_NAME, "readonly").objectStore(IMAGE_STORE_NAME).get(key);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    return record?.blob ? URL.createObjectURL(record.blob) : "";
  } catch {
    return "";
  }
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
      [await messageWithCachedImages(event.message.to, event.message, state.messages[event.message.to]?.find((message) => message.id === event.localId))],
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
    store.put({ key, ownerScope: ownerScope(routerUrl, agentId), peerScope: peerScopeValue, routerUrl, agentId, peer, message: messageForStorage(message), createdAtMs, savedAtMs: Date.now() });
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
      if (!db.objectStoreNames.contains(IMAGE_STORE_NAME)) db.createObjectStore(IMAGE_STORE_NAME, { keyPath: "key" });
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

function messageForStorage(message) {
  if (!Array.isArray(message.attachments)) return message;
  const attachments = message.attachments
    .filter((attachment) => !String(attachment.url || "").startsWith("data:image/"))
    .map(({ url, id, mime_type, size_bytes, name, cacheKey }) => ({ url, id, mime_type, size_bytes, name, cacheKey }));
  return { ...message, attachments };
}
function ownerScope(routerUrl, agentId) { return `${routerUrl}${SCOPE_SEPARATOR}${agentId}`; }
function peerScope(routerUrl, agentId, peer) { return `${ownerScope(routerUrl, agentId)}${SCOPE_SEPARATOR}${peer}`; }
function messageTime(message) { return Date.parse(message.created_at || message.createdAt || "") || Date.now(); }
function draftScope(routerUrl = state.routerUrl, agentId = state.agentId, peer = state.selectedPeer) { return peer ? peerScope(routerUrl, agentId, peer) : ""; }
function loadDrafts() {
  try { return pruneDrafts(JSON.parse(localStorage.getItem(DRAFT_STORE_KEY) || "{}")); } catch { return {}; }
}
function saveDrafts(drafts) { localStorage.setItem(DRAFT_STORE_KEY, JSON.stringify(pruneDrafts(drafts))); }
function pruneDrafts(drafts, now = Date.now()) {
  const cutoff = now - DRAFT_RETENTION_MS;
  return Object.fromEntries(Object.entries(drafts)
    .map(([key, draft]) => [key, typeof draft === "string" ? { body: draft, updatedAtMs: now } : draft])
    .filter(([, draft]) => draft?.body && (draft.updatedAtMs || 0) >= cutoff)
    .sort(([, left], [, right]) => (left.updatedAtMs || 0) - (right.updatedAtMs || 0))
    .slice(-DRAFT_LIMIT));
}
function loadDraftPeers(routerUrl, agentId) {
  const prefix = `${ownerScope(routerUrl, agentId)}${SCOPE_SEPARATOR}`;
  return new Set(Object.entries(loadDrafts()).filter(([key, draft]) => key.startsWith(prefix) && draft.body).map(([key]) => key.slice(prefix.length)));
}
function saveCurrentDraft() {
  const key = draftScope();
  if (!key) return;
  const drafts = loadDrafts();
  const value = elements.draft.value;
  if (value) drafts[key] = { body: value, updatedAtMs: Date.now() };
  else delete drafts[key];
  saveDrafts(drafts);
  state.draftPeers = loadDraftPeers(state.routerUrl, state.agentId);
}
function clearCurrentDraft() {
  const key = draftScope();
  if (!key) return;
  const drafts = loadDrafts();
  delete drafts[key];
  saveDrafts(drafts);
  state.draftPeers = loadDraftPeers(state.routerUrl, state.agentId);
}
function restoreDraftForSelectedPeer({ focus = false } = {}) {
  const key = draftScope();
  elements.draft.value = key ? loadDrafts()[key]?.body || "" : "";
  if (focus && key) focusDraftAtEnd();
}
function focusDraftAtEnd() {
  elements.draft.focus({ preventScroll: true });
  const end = elements.draft.value.length;
  elements.draft.setSelectionRange(end, end);
}
function loadSelectedPeers() {
  try { return JSON.parse(localStorage.getItem(SELECTED_PEER_STORE_KEY) || "{}"); } catch { return {}; }
}
function loadSelectedPeer(routerUrl, agentId) { return loadSelectedPeers()[ownerScope(routerUrl, agentId)] || ""; }
function saveSelectedPeer(peer) {
  const selectedPeers = loadSelectedPeers();
  selectedPeers[ownerScope(state.routerUrl, state.agentId)] = peer;
  localStorage.setItem(SELECTED_PEER_STORE_KEY, JSON.stringify(selectedPeers));
}

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
