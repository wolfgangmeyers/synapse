import { HISTORY_RETENTION_MS, mergeMessages, messageKey, messageTime, pruneHistoryMessages, reconcileHistoryMessages } from "./history.js";
import {
  canSendDraft,
  draftScope,
  historyRequestMatches,
  matchesActiveScope,
  ownerScope,
  peerScope,
  pruneDrafts,
  replaceAgentArrival,
  replacePendingMessage,
  SCOPE_SEPARATOR,
  settlePendingMessages,
  applyLifecycleSnapshot,
  upsertLifecycleAgent,
} from "./state.js";
import { renderMarkdownBody, textWithoutMarkdownImages } from "./markdown.js";

localStorage.removeItem("mecha.web.messages");
localStorage.removeItem("mecha.web.conversations");

const HISTORY_DB_NAME = "mecha-router-web-ui";
const HISTORY_DB_VERSION = 2;
const HISTORY_STORE_NAME = "conversation_messages";
const IMAGE_STORE_NAME = "image_attachments";
const DRAFT_STORE_KEY = "mecha.web.drafts";
const SELECTED_PEER_STORE_KEY = "mecha.web.selectedPeers";
const MAX_IMAGES = 3;
const MAX_IMAGE_EDGE = 1600;
const IMAGE_JPEG_QUALITY = 0.92;
const MAX_IMAGE_BYTES = 768 * 1024;
const MAX_TOTAL_IMAGE_BYTES = MAX_IMAGES * MAX_IMAGE_BYTES;
const HISTORY_IMAGE_EDGE = 360;
const HISTORY_IMAGE_QUALITY = 0.88;
const MESSAGE_BOTTOM_THRESHOLD_PX = 80;
const ARRIVED_MESSAGE_VISIBLE_MS = 60_000;
const WEB_DEFAULT_AGENT_ID = "wolfgang-web";

const initialRouter = localStorage.getItem("mecha.web.activeRouter") || "http://127.0.0.1:8765";
const state = {
  socket: null,
  defaultRouter: "http://127.0.0.1:8765",
  defaultAgentId: WEB_DEFAULT_AGENT_ID,
  routerUrl: initialRouter,
  agentId: agentIdForRouter(initialRouter, WEB_DEFAULT_AGENT_ID),
  routers: loadRouters(),
  routerAliases: loadRouterAliases(),
  routerEditor: null,
  agents: [],
  reminders: [],
  selectedPeer: "",
  agentFilter: "",
  images: [],
  messages: {},
  historyRequests: new Map(),
  draftPeers: new Set(),
  expandedSubagents: new Set(),
  lastLifecycleSnapshotVersion: 0,
  renderedMessageCounts: {},
  renderedMessageSignatures: {},
  renderedLastMessageKeys: {},
  renderedPeer: "",
  followMessages: true,
  arrivalsByAgent: new Map(),
  arrivalTimers: new Map(),
  reminderDialogPeer: "",
};

const elements = {
  statusDot: document.querySelector("#status-dot"),
  statusTitle: document.querySelector("#status-title"),
  statusDetail: document.querySelector("#status-detail"),
  selectedRouterLabel: document.querySelector("#selected-router-label"),
  routerSelector: document.querySelector("#router-selector"),
  routerDialog: document.querySelector("#router-dialog"),
  savedRouters: document.querySelector("#saved-routers"),
  openAddRouter: document.querySelector("#open-add-router"),
  routerSetup: document.querySelector("#router-setup"),
  routerFormTitle: document.querySelector("#router-form-title"),
  routerUrl: document.querySelector("#router-url"),
  routerAlias: document.querySelector("#router-alias"),
  agentId: document.querySelector("#agent-id"),
  addRouter: document.querySelector("#add-router"),
  cancelRouterEdit: document.querySelector("#cancel-router-edit"),
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
  arrivedDialog: document.querySelector("#arrived-dialog"),
  arrivedDialogSender: document.querySelector("#arrived-dialog-sender"),
  arrivedDialogMeta: document.querySelector("#arrived-dialog-meta"),
  arrivedDialogBody: document.querySelector("#arrived-dialog-body"),
  remindersDialog: document.querySelector("#reminders-dialog"),
  remindersDialogBody: document.querySelector("#reminders-dialog-body"),
};

const config = await fetch("/ui/config").then((response) => response.json()).catch(() => ({}));
state.defaultRouter = config.defaultRouter || state.defaultRouter;
state.defaultAgentId = config.defaultAgentId || state.defaultAgentId;
if (!localStorage.getItem("mecha.web.activeRouter")) {
  state.routerUrl = state.defaultRouter;
  state.agentId = agentIdForRouter(state.routerUrl, state.defaultAgentId);
}
ensureRouter(state.routerUrl);
state.messages = await loadMessages(state.routerUrl, state.agentId);
state.draftPeers = loadDraftPeers(state.routerUrl, state.agentId);
state.selectedPeer = loadSelectedPeer(state.routerUrl, state.agentId);
elements.routerUrl.value = state.routerUrl;
elements.routerAlias.value = state.routerAliases[state.routerUrl] || "";
elements.agentId.value = state.agentId;
connectUiSocket();
render();
restoreDraftForSelectedPeer();

void pruneExpiredHistory();

elements.routerSelector.addEventListener("click", () => {
  closeRouterEditor();
  renderRouters();
  elements.routerDialog.showModal();
});
elements.openAddRouter.addEventListener("click", () => openRouterEditor("add"));
elements.cancelRouterEdit.addEventListener("click", closeRouterEditor);
elements.addRouter.addEventListener("click", () => {
  void saveRouterEditor();
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
elements.draft.addEventListener("input", () => { normalizeDraftEscapedNewlines(); saveCurrentDraft(); updateSendState(); });
elements.attachImage.addEventListener("click", () => elements.imageInput.click());
elements.imageInput.addEventListener("change", () => { void addImageFiles(elements.imageInput.files); elements.imageInput.value = ""; });
elements.messages.addEventListener("scroll", () => { state.followMessages = isMessagesNearBottom(); });
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
  if (!matchesActiveScope(event, state)) return;
  if (event.type === "status") {
    setStatus(event.state, event.detail, event.agentId, event.routerUrl);
    if (event.state === "connected") backfillSelectedConversation();
    return;
  }
  if (event.type === "reminders") {
    state.reminders = Array.isArray(event.reminders) ? event.reminders : [];
    renderAgents();
    if (elements.remindersDialog?.open && state.reminderDialogPeer) renderReminderDialog(state.reminderDialogPeer);
    return;
  }
  if (event.type === "agents") {
    state.agents = event.agents
      .filter((agent) => agent.agent_id !== state.agentId)
      .map((agent) => mergeAgentSnapshot(agent));
    if (Number.isSafeInteger(event.activitySnapshotVersion)) state.lastLifecycleSnapshotVersion = Math.max(state.lastLifecycleSnapshotVersion, event.activitySnapshotVersion);
    renderAgents();
    renderConversation();
    return;
  }
  if (event.type === "subagent_state_snapshot") {
    applyLifecycleSnapshotEvent(event);
    return;
  }
  if (event.type === "subagent_state_update") {
    applyLifecycleUpdateEvent(event.agent, event.routerVersion);
    return;
  }
  if (event.type === "subagent_state_replay") {
    for (const agent of event.updates || []) applyLifecycleUpdateEvent(agent, event.routerVersion, { render: false });
    renderAgents();
    renderConversation();
    return;
  }
  if (event.type === "agent_activity") {
    updateAgentActivity(event.agent, event.activityState, event.subagentCount, event.subagents);
    return;
  }
  if (event.type === "message_arrival") {
    showMessageArrival(event);
    return;
  }
  if (event.type === "reminders_error") {
    elements.statusDetail.textContent = `Reminder refresh failed: ${event.message || "Unknown error"}`;
    return;
  }
  if (event.type === "agents_error" || event.type === "command_error" || event.type === "history_error") {
    setStatus("error", event.message);
    return;
  }
  if (event.type === "history") {
    if (!historyRequestMatches(event, state.selectedPeer, state.historyRequests.get(event.peer))) return;
    const scope = { routerUrl: state.routerUrl, agentId: state.agentId };
    state.messages[event.peer] = pruneHistoryMessages(reconcileHistoryMessages(
      state.messages[event.peer] || [],
      event.messages || [],
      event.highWaterSequence || 0,
    ));
    await savePeerMessages(event.peer, scope, state.messages[event.peer]);
    renderConversation();
    return;
  }
  if (event.type === "conversation_message") {
    const peer = event.message.from === state.agentId ? event.message.to : event.message.from;
    if (!peer || peer === state.agentId) return;
    await appendMessage(peer, event.message);
    if (!state.selectedPeer) selectPeer(peer);
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

function showMessageArrival(event) {
  if (!event.agent || event.agent === state.agentId || !["agent", "discord", "reminder"].includes(event.sourceKind)) return;
  const arrival = {
    agent: event.agent,
    from: event.from || "Unknown sender",
    subject: event.subject,
    sourceKind: event.sourceKind,
    preview: event.preview || "",
    body: event.body || "",
    createdAt: event.createdAt,
    messageId: event.messageId,
  };
  replaceAgentArrival(state.arrivalsByAgent, arrival);
  const existingTimer = state.arrivalTimers.get(event.agent);
  if (existingTimer !== undefined) clearTimeout(existingTimer);
  state.arrivalTimers.set(event.agent, setTimeout(() => hideMessageArrival(event.agent), ARRIVED_MESSAGE_VISIBLE_MS));
  renderAgents();
  renderConversation();
}

function hideMessageArrival(agent) {
  state.arrivalsByAgent.delete(agent);
  const timer = state.arrivalTimers.get(agent);
  if (timer !== undefined) clearTimeout(timer);
  state.arrivalTimers.delete(agent);
  renderAgents();
  renderConversation();
}

function clearAllMessageArrivals() {
  for (const timer of state.arrivalTimers.values()) clearTimeout(timer);
  state.arrivalsByAgent.clear();
  state.arrivalTimers.clear();
}

function showArrivedMessageDialog(arrival) {
  if (!arrival) return;
  elements.arrivedDialogSender.textContent = humanName(arrival.from || "Unknown sender");
  const timestamp = arrival.createdAt || "";
  const parts = [arrival.from ? `From ${arrival.from}` : "", arrival.subject ? `Subject: ${arrival.subject}` : "", timestamp ? new Date(timestamp).toLocaleString() : ""].filter(Boolean);
  elements.arrivedDialogMeta.textContent = parts.join(" · ");
  elements.arrivedDialogBody.replaceChildren(renderMarkdownBody(document, arrival.body || ""));
  elements.arrivedDialog.showModal();
}

async function switchRouter(routerUrl, { connect = false } = {}) {
  saveCurrentDraft();
  state.routerUrl = routerUrl;
  state.agentId = agentIdForRouter(routerUrl, state.defaultAgentId);
  state.agents = [];
  state.reminders = [];
  state.expandedSubagents.clear();
  state.lastLifecycleSnapshotVersion = 0;
  state.selectedPeer = loadSelectedPeer(routerUrl, state.agentId);
  state.messages = await loadMessages(routerUrl, state.agentId);
  state.historyRequests = new Map();
  clearAllMessageArrivals();
  state.draftPeers = loadDraftPeers(routerUrl, state.agentId);
  localStorage.setItem("mecha.web.activeRouter", routerUrl);
  elements.routerUrl.value = routerUrl;
  elements.routerAlias.value = state.routerAliases[routerUrl] || "";
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
  delete state.routerAliases[routerUrl];
  saveRouterAliases();
  deleteAgentIdForRouter(routerUrl);
  if (state.routerUrl === routerUrl) void switchRouter(state.routers[0], { connect: true });
  saveRouters();
  if (state.routerEditor?.routerUrl === routerUrl) closeRouterEditor();
  renderRouters();
}

function replaceRouter(previousUrl, nextUrl) {
  state.routers = state.routers.map((router) => router === previousUrl ? nextUrl : router);
  if (!state.routers.includes(nextUrl)) state.routers.push(nextUrl);
  if (previousUrl !== nextUrl) {
    delete state.routerAliases[previousUrl];
    saveRouterAliases();
    deleteAgentIdForRouter(previousUrl);
  }
  saveRouters();
}

function setStatus(stateName, detail, agentId = state.agentId, routerUrl = state.routerUrl) {
  elements.statusDot.className = `status-dot ${stateName === "connected" ? "connected" : stateName === "connecting" || stateName === "reconnecting" ? "connecting" : "offline"}`;
  elements.statusTitle.textContent = stateName === "connected" ? `Connected as ${agentId}` : stateName[0].toUpperCase() + stateName.slice(1);
  elements.statusDetail.textContent = stateName === "connected" ? "" : `${detail || ""}${routerUrl ? ` · ${displayRouter(routerUrl)}` : ""}`;
}

function render() {
  elements.selectedRouterLabel.textContent = displayRouter(state.routerUrl);
  renderRouters();
  renderRouterEditor();
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
    label.className = "router-label";
    const title = document.createElement("strong");
    const alias = state.routerAliases[routerUrl] || "";
    title.textContent = alias || new URL(routerUrl).host;
    const meta = document.createElement("div");
    meta.className = "meta";
    meta.textContent = `${alias ? routerUrl : ""}${routerUrl === state.routerUrl ? `${alias ? " · " : ""}selected` : ""}`;
    label.append(title);
    if (meta.textContent) label.append(meta);
    const actions = document.createElement("div");
    actions.className = "router-row-actions";
    const edit = document.createElement("button");
    edit.type = "button";
    edit.className = "icon-button secondary";
    edit.append(icon("fa-pen"));
    edit.setAttribute("aria-label", `Edit ${displayRouter(routerUrl)}`);
    edit.addEventListener("click", (event) => {
      event.stopPropagation();
      openRouterEditor("edit", routerUrl);
    });
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "icon-button danger";
    remove.append(icon("fa-trash"));
    remove.setAttribute("aria-label", `Remove ${displayRouter(routerUrl)}`);
    remove.disabled = state.routers.length === 1;
    remove.addEventListener("click", (event) => {
      event.stopPropagation();
      removeRouter(routerUrl);
    });
    actions.append(edit, remove);
    row.append(label, actions);
    return row;
  }));
}

function openRouterEditor(mode, routerUrl = "") {
  state.routerEditor = { mode, routerUrl };
  elements.routerUrl.value = routerUrl || state.routerUrl;
  elements.routerAlias.value = routerUrl ? state.routerAliases[routerUrl] || "" : "";
  elements.agentId.value = agentIdForRouter(routerUrl || state.routerUrl, state.defaultAgentId);
  renderRouterEditor();
  elements.routerUrl.focus();
}

function closeRouterEditor() {
  state.routerEditor = null;
  renderRouterEditor();
}

function renderRouterEditor() {
  const editor = state.routerEditor;
  elements.routerSetup.hidden = !editor;
  if (!editor) return;
  elements.routerFormTitle.textContent = editor.mode === "edit" ? "Edit router" : "Add router";
  elements.addRouter.textContent = editor.mode === "edit" ? "Save router" : "Add router";
}

async function saveRouterEditor() {
  try {
    const editor = state.routerEditor || { mode: "add", routerUrl: "" };
    const previousUrl = editor.mode === "edit" ? editor.routerUrl : "";
    const routerUrl = normalizeRouterInput(elements.routerUrl.value);
    state.agentId = elements.agentId.value.trim();
    saveAgentIdForRouter(routerUrl, state.agentId);
    if (previousUrl && previousUrl !== routerUrl) replaceRouter(previousUrl, routerUrl);
    else ensureRouter(routerUrl);
    setRouterAlias(routerUrl, elements.routerAlias.value);
    closeRouterEditor();
    await switchRouter(routerUrl, { connect: true });
  } catch (error) {
    setStatus("error", error.message);
  }
}

function renderAgents() {
  const filter = state.agentFilter.trim().toLowerCase();
  const peers = state.agents
    .filter((agent) => agent.agent_id && agent.agent_id !== state.agentId)
    .filter((agent) => !filter || `${agent.agent_id} ${humanName(agent.agent_id)}`.toLowerCase().includes(filter))
    .sort((left, right) => left.agent_id.localeCompare(right.agent_id));
  elements.agentCount.textContent = String(peers.length);
  elements.agents.replaceChildren(...peers.map((agent) => {
    const peer = agent.agent_id;
    const active = agent.activity_state === "active";
    const compacting = agent.activity_state === "compacting";
    const subagents = normalizeSubagents(agent.subagents);
    const subagentCount = Math.max(subagents.length, Number.isSafeInteger(agent.subagent_count) ? agent.subagent_count : 0);
    const canExpandSubagents = subagents.length > 0;
    const expanded = canExpandSubagents && state.expandedSubagents.has(peer);
    const arrival = state.arrivalsByAgent.get(peer);
    const reminders = state.reminders.filter((reminder) => reminder.target === peer && ["scheduled", "due", "message_queued"].includes(reminder.state));
    const row = document.createElement("div");
    row.tabIndex = 0;
    row.setAttribute("role", "button");
    row.dataset.agentId = peer;
    row.className = `agent-row${peer === state.selectedPeer ? " active" : ""}${active ? " working" : ""}${compacting ? " compacting" : ""}${subagentCount > 0 ? " has-subagents" : ""}${arrival ? " has-arrival" : ""}${expanded ? " expanded" : ""}`;
    const badge = compacting ? compactingBadgeMarkup() : subagentBadgeMarkup(peer, subagentCount, subagents, expanded, "row");
    row.innerHTML = `<span class="avatar${compacting ? " compacting" : ""}">${escapeHtml(monogram(peer))}</span><span class="agent-row-text"><span class="agent-name-line">${reminderIndicatorMarkup(reminders, humanName(peer))}<strong>${escapeHtml(humanName(peer))}</strong></span>${badge}${arrivalIndicatorMarkup(arrival)}</span>${active ? '<span class="working-indicator" aria-label="Agent is working"><span></span></span>' : ""}`;
    row.addEventListener("click", () => selectPeer(peer));
    row.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        selectPeer(peer);
      }
    });
    attachSubagentToggle(row, peer, canExpandSubagents, renderAgents);
    attachReminderActions(row, reminders);
    attachArrivalActions(row, arrival);
    return row;
  }));
  requestAnimationFrame(updateSubagentTooltips);
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
  state.followMessages = true;
  saveSelectedPeer(peer);
  render();
  restoreDraftForSelectedPeer({ focus: true });
  requestHistory(peer);
}

function updateAgentActivity(peer, activityState, subagentCount = 0, subagents = []) {
  if (!peer || peer === state.agentId) return;
  const current = state.agents.find((agent) => agent.agent_id === peer) ?? { agent_id: peer, activity_state: "idle" };
  const incomingSummaries = normalizeSubagents(subagents);
  const incomingCount = Number.isSafeInteger(subagentCount) ? subagentCount : 0;
  const currentSummaries = normalizeSubagents(current?.subagents);
  const summaries = incomingSummaries.length > 0
    ? incomingSummaries
    : (incomingCount > 0 && currentSummaries.length === incomingCount ? currentSummaries : []);
  const normalizedCount = Math.max(summaries.length, incomingCount);
  const hasDurableLifecycle = Number.isSafeInteger(current.subagent_version);
  if (!hasDurableLifecycle && normalizedCount === 0) state.expandedSubagents.delete(peer);
  if (current.activity_state === activityState && hasDurableLifecycle) return;
  if (current.activity_state === activityState && (current.subagent_count ?? 0) === normalizedCount && sameSubagents(current.subagents, summaries)) return;
  const next = hasDurableLifecycle
    ? { ...current, activity_state: activityState }
    : { ...current, activity_state: activityState, subagent_count: normalizedCount, subagents: summaries };
  state.agents = state.agents.some((agent) => agent.agent_id === peer)
    ? state.agents.map((agent) => agent.agent_id === peer ? next : agent)
    : [...state.agents, next];
  renderAgents();
  renderConversation();
}

function requestHistory(peer) {
  if (!peer) return;
  const requestId = crypto.randomUUID();
  state.historyRequests.set(peer, requestId);
  sendCommand({ type: "get_history", peer, requestId });
}

function backfillSelectedConversation() {
  const peer = state.selectedPeer;
  if (!peer) return;
  const routerUrl = state.routerUrl;
  const agentId = state.agentId;
  requestHistory(peer);
  setTimeout(() => {
    if (state.selectedPeer === peer && state.routerUrl === routerUrl && state.agentId === agentId) requestHistory(peer);
  }, 500);
}

function sendDraft() {
  const body = normalizeEscapedNewlines(elements.draft.value);
  if (body !== elements.draft.value) elements.draft.value = body;
  if (!canSendDraft(state.selectedPeer, body, state.images.length)) return;
  const localId = crypto.randomUUID();
  const images = state.images.map(({ dataUrl, name, sizeBytes, mimeType }) => ({ dataUrl, name, sizeBytes, mimeType }));
  const message = { id: localId, from: state.agentId, to: state.selectedPeer, body: pendingMessageBody(body, images), attachments: images.map((image) => ({ url: image.dataUrl, mime_type: image.mimeType, size_bytes: image.sizeBytes, name: image.name })), created_at: new Date().toISOString(), direction: "outgoing", pending: true };
  state.followMessages = true;
  void appendMessage(state.selectedPeer, message);
  elements.draft.value = "";
  state.images = [];
  renderImagePreview();
  clearCurrentDraft();
  updateSendState();
  sendCommand({ type: "send_message", to: state.selectedPeer, body, images, localId });
}

function normalizeEscapedNewlines(value) {
  return value.replace(/\\r\\n|\\n|\\r/g, "\n");
}

function normalizeDraftEscapedNewlines() {
  const value = elements.draft.value;
  const normalized = normalizeEscapedNewlines(value);
  if (normalized === value) return;
  const start = normalizeEscapedNewlines(value.slice(0, elements.draft.selectionStart ?? value.length)).length;
  const end = normalizeEscapedNewlines(value.slice(0, elements.draft.selectionEnd ?? value.length)).length;
  elements.draft.value = normalized;
  elements.draft.setSelectionRange(start, end);
}

function renderConversation() {
  const peer = state.selectedPeer;
  elements.peerAvatar.textContent = peer ? monogram(peer) : "?";
  elements.peerAvatar.className = `avatar${peer ? "" : " muted"}`;
  const selectedAgent = state.agents.find((agent) => agent.agent_id === peer);
  const active = selectedAgent?.activity_state === "active";
  const compacting = selectedAgent?.activity_state === "compacting";
  elements.peerName.innerHTML = peer ? `${escapeHtml(humanName(peer))}${active ? '<span class="working-indicator inline" aria-label="Agent is working"><span></span></span>' : ""}${compacting ? compactingBadgeMarkup("inline") : ""}` : "Pick an agent";
  const subagents = normalizeSubagents(selectedAgent?.subagents);
  const subagentCount = Math.max(subagents.length, Number.isSafeInteger(selectedAgent?.subagent_count) ? selectedAgent.subagent_count : 0);
  const expanded = subagents.length > 0 && state.expandedSubagents.has(peer);
  elements.peerDetail.className = `peer-activity${active ? " working" : ""}${compacting ? " compacting" : ""}${subagentCount > 0 ? " has-subagents" : ""}${expanded ? " expanded" : ""}`;
  const arrival = state.arrivalsByAgent.get(peer);
  elements.peerDetail.innerHTML = peer ? [
    compacting ? "Mailbox is compacting; delivery is queued." : subagentBadgeMarkup(peer, subagentCount, subagents, expanded, "header"),
    arrivalIndicatorMarkup(arrival, "header"),
  ].filter(Boolean).join(" ") : "Connected agents appear on the left.";
  attachSubagentToggle(elements.peerDetail, peer, !compacting && subagents.length > 0, renderConversation);
  attachArrivalActions(elements.peerDetail, arrival);
  requestAnimationFrame(updateSubagentTooltips);
  elements.peerAvatar.classList.toggle("working", active);
  elements.peerAvatar.classList.toggle("compacting", compacting);
  elements.draft.disabled = !peer;
  elements.attachImage.disabled = !peer;
  updateSendState();
  if (!peer) {
    elements.draft.value = "";
    state.images = [];
    renderImagePreview();
    elements.messages.innerHTML = `<div class="empty-state"><h3>No conversation selected</h3><p>Choose a connected agent on ${escapeHtml(displayRouter(state.routerUrl))}, or open Routers to switch.</p></div>`;
    state.renderedPeer = "";
    return;
  }
  const messages = state.messages[peer] || [];
  if (messages.length === 0) {
    elements.messages.innerHTML = `<div class="empty-state"><h3>Start a conversation with ${escapeHtml(humanName(peer))}</h3><p>Messages use ${escapeHtml(displayRouter(state.routerUrl))}.</p></div>`;
    state.renderedMessageCounts[peer] = 0;
    state.renderedMessageSignatures[peer] = "";
    state.renderedLastMessageKeys[peer] = "";
    state.renderedPeer = peer;
    return;
  }
  const signature = messagesSignature(messages);
  if (state.renderedPeer === peer && state.renderedMessageSignatures[peer] === signature) return;
  const previousCount = state.renderedMessageCounts[peer] || 0;
  const previousLastKey = state.renderedLastMessageKeys[peer] || "";
  const nextLastKey = messageKey(messages[messages.length - 1]);
  const appendedMessage = previousLastKey && nextLastKey !== previousLastKey && messages.length >= previousCount;
  const wasFollowing = state.followMessages || isMessagesNearBottom();
  const shouldFollow = wasFollowing && (!previousLastKey || appendedMessage || messages.length >= previousCount);
  const scrollTop = elements.messages.scrollTop;
  elements.messages.replaceChildren(...messages.map(renderMessage));
  state.renderedMessageCounts[peer] = messages.length;
  state.renderedMessageSignatures[peer] = signature;
  state.renderedLastMessageKeys[peer] = nextLastKey;
  state.renderedPeer = peer;
  if (shouldFollow) scrollMessagesToBottom();
  else elements.messages.scrollTop = scrollTop;
}

function messagesSignature(messages) {
  return messages.map((message) => `${messageKey(message)}\u001e${message.pending ? "1" : "0"}\u001e${message.direction || ""}\u001e${message.body || ""}\u001e${message.recorded_at || message.created_at || message.createdAt || ""}\u001e${JSON.stringify(message.attachments || [])}`).join("\u001f");
}

function isMessagesNearBottom() {
  return elements.messages.scrollHeight - elements.messages.scrollTop - elements.messages.clientHeight <= MESSAGE_BOTTOM_THRESHOLD_PX;
}

function scrollMessagesToBottom() {
  state.followMessages = true;
  elements.messages.scrollTop = elements.messages.scrollHeight;
  requestAnimationFrame(() => {
    if (state.followMessages) elements.messages.scrollTop = elements.messages.scrollHeight;
  });
  setTimeout(() => {
    if (state.followMessages) elements.messages.scrollTop = elements.messages.scrollHeight;
  }, 100);
}

function scrollMessagesToBottomIfFollowing() {
  if (state.followMessages) scrollMessagesToBottom();
}

function renderMessage(message) {
  const outgoing = message.direction === "outgoing" || message.from === state.agentId;
  const wrapper = document.createElement("div");
  wrapper.className = `bubble-wrap ${outgoing ? "outgoing" : "incoming"}`;
  const label = document.createElement("div");
  label.className = "bubble-label";
  const sender = document.createElement("span");
  sender.textContent = outgoing ? (message.pending ? "You · sending" : "You") : humanName(message.from);
  const timestamp = document.createElement("time");
  timestamp.dateTime = message.created_at || message.createdAt || message.recorded_at || "";
  timestamp.textContent = formatMessageTimestamp(message);
  label.append(sender, timestamp);
  const bubble = document.createElement("div");
  bubble.className = "bubble";
  bubble.append(renderMarkdownBody(document, message.body || ""));
  const attachments = message.attachments || imageAttachmentsFromBody(textWithoutMarkdownImages(message.body));
  if (attachments.length > 0) bubble.append(renderMessageImages(attachments));
  wrapper.append(label, bubble);
  return wrapper;
}

function updateSendState() {
  elements.send.disabled = !canSendDraft(state.selectedPeer, elements.draft.value, state.images.length);
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
  const context = canvas.getContext("2d");
  context.fillStyle = "#fff";
  context.fillRect(0, 0, width, height);
  context.drawImage(bitmap, 0, 0, width, height);
  bitmap.close?.();
  const blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/jpeg", IMAGE_JPEG_QUALITY));
  if (!blob) throw new Error("Image could not be converted");
  if (blob.size > MAX_IMAGE_BYTES) throw new Error("Image is too large after compression");
  return { dataUrl: await blobToDataUrl(blob), name: jpgName(file.name), sizeBytes: blob.size, mimeType: "image/jpeg", width, height };
}

function renderImagePreview() {
  elements.imagePreview.replaceChildren(...state.images.map((image, index) => {
    const card = document.createElement("div");
    card.className = "image-card";
    const img = document.createElement("img");
    img.src = image.dataUrl;
    img.alt = image.name || `Image ${index + 1}`;
    const label = document.createElement("small");
    label.textContent = `${image.name || `image-${index + 1}.jpg`} · ${formatBytes(image.sizeBytes)}`;
    const remove = document.createElement("button");
    remove.type = "button";
    remove.append(icon("fa-xmark"));
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
    image.addEventListener("load", scrollMessagesToBottomIfFollowing, { once: true });
    if (attachment.cacheKey) {
      void cachedImageUrl(attachment.cacheKey).then((url) => { if (url) image.src = url; });
    }
    image.addEventListener("click", () => window.open(image.src || attachment.url, "_blank", "noopener"));
    return image;
  }));
  return gallery;
}

function imageAttachmentsFromBody(body) {
  const matches = [...String(body || "").matchAll(/\/v1\/attachments\/([A-Za-z0-9._-]+\.(?:png|jpe?g|webp|gif))/g)];
  return matches.map((match) => ({ url: `${state.routerUrl}${match[0]}`, name: match[1] }));
}

function pendingMessageBody(body, images) {
  const text = body.trim();
  if (images.length === 0) return body;
  return `${text}${text ? "\n\n" : ""}Attached image${images.length === 1 ? "" : "s"}:\n${images.map((image, index) => `${index + 1}. ${image.name || "image.jpg"}`).join("\n")}`;
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

function jpgName(name) {
  const base = String(name || "image").replace(/\.[^.]*$/, "") || "image";
  return `${base}.jpg`;
}

function icon(name) {
  const element = document.createElement("i");
  element.className = `fa-solid ${name}`;
  element.setAttribute("aria-hidden", "true");
  return element;
}

function formatBytes(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatMessageTimestamp(message) {
  return new Intl.DateTimeFormat([], { hour: "numeric", minute: "2-digit" }).format(new Date(messageTime(message)));
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
  transaction.objectStore(IMAGE_STORE_NAME).put({ key, blob, mime_type: "image/jpeg", name: attachment.name, savedAtMs: Date.now() });
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
  const context = canvas.getContext("2d");
  context.fillStyle = "#fff";
  context.fillRect(0, 0, width, height);
  context.drawImage(bitmap, 0, 0, width, height);
  bitmap.close?.();
  const blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/jpeg", HISTORY_IMAGE_QUALITY));
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
  const scope = { routerUrl: state.routerUrl, agentId: state.agentId };
  state.messages[peer] = pruneHistoryMessages(mergeMessages(state.messages[peer] || [], [message]));
  const messages = state.messages[peer];
  render();
  await savePeerMessages(peer, scope, messages);
}

async function settleOutgoing(event) {
  const scope = { routerUrl: state.routerUrl, agentId: state.agentId };
  const touchedPeers = new Set();
  for (const peer of Object.keys(state.messages)) {
    const updated = settlePendingMessages(state.messages[peer], event.localId, event.ok);
    if (updated.some((message, index) => message !== state.messages[peer][index])) touchedPeers.add(peer);
    state.messages[peer] = updated;
  }
  if (event.ok && event.message?.to) {
    const cachedMessage = await messageWithCachedImages(
      event.message.to,
      event.message,
      state.messages[event.message.to]?.find((message) => message.id === event.localId),
    );
    if (scope.routerUrl !== state.routerUrl || scope.agentId !== state.agentId) return;
    state.messages[event.message.to] = pruneHistoryMessages(replacePendingMessage(
      state.messages[event.message.to] || [],
      event.localId,
      cachedMessage,
    ));
    touchedPeers.add(event.message.to);
  }
  await Promise.all([...touchedPeers].map((peer) => savePeerMessages(peer, scope, state.messages[peer])));
  if (!event.ok) setStatus("error", event.error || "Message not sent");
  renderConversation();
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

async function savePeerMessages(
  peer,
  { routerUrl, agentId } = { routerUrl: state.routerUrl, agentId: state.agentId },
  sourceMessages = state.messages[peer] || [],
) {
  const messages = pruneHistoryMessages(sourceMessages);
  if (routerUrl === state.routerUrl && agentId === state.agentId) state.messages[peer] = messages;
  const db = await openHistoryDb();
  await saveRecordsForPeer(db, routerUrl, agentId, peer, messages);
}

async function saveRecordsForPeer(db, routerUrl, agentId, peer, messages) {
  const peerScopeValue = peerScope(routerUrl, agentId, peer);
  const existing = await getAllKeysFromIndex(db, "peerScope", peerScopeValue);
  const transaction = db.transaction(HISTORY_STORE_NAME, "readwrite");
  const store = transaction.objectStore(HISTORY_STORE_NAME);
  for (const key of existing) store.delete(key);
  for (const message of messages) {
    const createdAtMs = messageTime(message);
    const key = `${peerScopeValue}${SCOPE_SEPARATOR}${messageKey(message) || crypto.randomUUID()}`;
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
function loadDrafts() {
  try { return pruneDrafts(JSON.parse(localStorage.getItem(DRAFT_STORE_KEY) || "{}")); } catch { return {}; }
}
function saveDrafts(drafts) { localStorage.setItem(DRAFT_STORE_KEY, JSON.stringify(pruneDrafts(drafts))); }
function loadDraftPeers(routerUrl, agentId) {
  const prefix = `${ownerScope(routerUrl, agentId)}${SCOPE_SEPARATOR}`;
  return new Set(Object.entries(loadDrafts()).filter(([key, draft]) => key.startsWith(prefix) && draft.body).map(([key]) => key.slice(prefix.length)));
}
function saveCurrentDraft() {
  const key = draftScope(state.routerUrl, state.agentId, state.selectedPeer);
  if (!key) return;
  const drafts = loadDrafts();
  const value = elements.draft.value;
  if (value) drafts[key] = { body: value, updatedAtMs: Date.now() };
  else delete drafts[key];
  saveDrafts(drafts);
  state.draftPeers = loadDraftPeers(state.routerUrl, state.agentId);
}
function clearCurrentDraft() {
  const key = draftScope(state.routerUrl, state.agentId, state.selectedPeer);
  if (!key) return;
  const drafts = loadDrafts();
  delete drafts[key];
  saveDrafts(drafts);
  state.draftPeers = loadDraftPeers(state.routerUrl, state.agentId);
}
function restoreDraftForSelectedPeer({ focus = false } = {}) {
  const key = draftScope(state.routerUrl, state.agentId, state.selectedPeer);
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
function displayRouter(routerUrl) { return state.routerAliases[routerUrl] || new URL(routerUrl).host; }
function loadRouters() {
  try { return JSON.parse(localStorage.getItem("mecha.web.routers") || "[]"); } catch { return []; }
}
function saveRouters() { localStorage.setItem("mecha.web.routers", JSON.stringify(state.routers)); }
function loadRouterAliases() {
  try { return JSON.parse(localStorage.getItem("mecha.web.routerAliases") || "{}"); } catch { return {}; }
}
function saveRouterAliases() { localStorage.setItem("mecha.web.routerAliases", JSON.stringify(state.routerAliases)); }
function setRouterAlias(routerUrl, alias) {
  const name = alias.trim();
  if (name) state.routerAliases[routerUrl] = name;
  else delete state.routerAliases[routerUrl];
  saveRouterAliases();
  render();
}
function loadAgentIds() {
  try { return JSON.parse(localStorage.getItem("mecha.web.agentIds") || "{}"); } catch { return {}; }
}
function agentIdForRouter(routerUrl, defaultAgentId = WEB_DEFAULT_AGENT_ID) { return loadAgentIds()[routerUrl] || localStorage.getItem("mecha.web.agentId") || defaultAgentId; }
function saveAgentIdForRouter(routerUrl, agentId) {
  const agentIds = loadAgentIds();
  agentIds[routerUrl] = agentId;
  localStorage.setItem("mecha.web.agentIds", JSON.stringify(agentIds));
  localStorage.setItem("mecha.web.agentId", agentId);
}
function deleteAgentIdForRouter(routerUrl) {
  const agentIds = loadAgentIds();
  delete agentIds[routerUrl];
  localStorage.setItem("mecha.web.agentIds", JSON.stringify(agentIds));
}
function humanName(id) { return id.split(/[._-]+/).filter(Boolean).map((part) => part[0].toUpperCase() + part.slice(1)).join(" ") || id; }
function truncateText(value, maxLength) {
  const text = String(value || "").replace(/\s+/g, " ").trim();
  return text.length > maxLength ? `${text.slice(0, Math.max(0, maxLength - 1))}…` : text;
}
function monogram(id) { return (id.match(/[A-Za-z0-9]/)?.[0] || "?").toUpperCase(); }
function cssSafeId(value) { return String(value).replace(/[^A-Za-z0-9_-]/g, "-").slice(0, 96); }
function updateSubagentTooltips() {
  for (const badge of document.querySelectorAll(".subagent-indicator.expanded")) {
    const descriptions = [...badge.querySelectorAll(".subagent-description")];
    const truncated = descriptions.some((description) => description.scrollWidth > description.clientWidth);
    if (truncated) {
      badge.title = descriptions.map((description) => description.textContent.trim()).filter(Boolean).join("\n\n");
    } else {
      badge.removeAttribute("title");
    }
    for (const description of descriptions) description.removeAttribute("title");
  }
}
function applyLifecycleSnapshotEvent(event) {
  const result = applyLifecycleSnapshot(state.agents, event.agents, event.routerVersion, state.lastLifecycleSnapshotVersion);
  state.agents = result.agents;
  state.lastLifecycleSnapshotVersion = result.lastSnapshotVersion;
  for (const agent of state.agents) if ((agent.subagent_count ?? 0) === 0) state.expandedSubagents.delete(agent.agent_id);
  renderAgents();
  renderConversation();
}

function applyLifecycleUpdateEvent(agent, routerVersion, { render = true } = {}) {
  if (!agent?.agent_id || agent.agent_id === state.agentId || !Number.isSafeInteger(routerVersion)) return;
  state.agents = upsertLifecycleAgent(state.agents, { ...agent, subagent_version: agent.subagent_version ?? routerVersion });
  state.lastLifecycleSnapshotVersion = Math.max(state.lastLifecycleSnapshotVersion, routerVersion);
  const next = state.agents.find((candidate) => candidate.agent_id === agent.agent_id);
  if ((next?.subagent_count ?? 0) === 0) state.expandedSubagents.delete(agent.agent_id);
  if (render) { renderAgents(); renderConversation(); }
}

function mergeAgentSnapshot(agent) {
  const current = state.agents.find((candidate) => candidate.agent_id === agent.agent_id);
  if (Number.isSafeInteger(current?.subagent_version)
    && (!Number.isSafeInteger(agent.subagent_version)
      || agent.subagent_version < current.subagent_version
      || (agent.subagent_version === current.subagent_version
        && ((agent.subagent_count ?? 0) !== (current.subagent_count ?? 0)
          || !sameSubagents(agent.subagents, current.subagents))))) {
    return { ...agent, subagent_version: current.subagent_version, subagent_count: current.subagent_count, subagents: current.subagents };
  }
  const incomingSummaries = normalizeSubagents(agent.subagents);
  const incomingCount = Number.isSafeInteger(agent.subagent_count) ? agent.subagent_count : 0;
  if (incomingSummaries.length > 0 || incomingCount === 0) return { ...agent, subagents: incomingSummaries };
  const currentSummaries = normalizeSubagents(current?.subagents);
  return currentSummaries.length === incomingCount ? { ...agent, subagents: currentSummaries } : { ...agent, subagents: [] };
}
function compactingBadgeMarkup(extraClass = "") {
  return `<span class="compacting-indicator${extraClass ? ` ${extraClass}` : ""}" aria-label="Agent is compacting"><span class="compacting-dot"></span><span>Compacting</span></span>`;
}

function reminderIndicatorMarkup(reminders, peerName) {
  if (reminders.length === 0) return "";
  const reminderLabel = `${reminders.length} active reminder${reminders.length === 1 ? "" : "s"} for ${peerName}`;
  return `<button class="reminder-indicator" type="button" aria-haspopup="dialog" aria-controls="reminders-dialog" aria-label="Show ${escapeHtml(reminderLabel)}" title="Show reminders"><i class="fa-solid fa-clock" aria-hidden="true"></i></button>`;
}

function attachReminderActions(root, reminders) {
  if (reminders.length === 0) return;
  const indicator = root.querySelector(".reminder-indicator");
  if (!indicator) return;
  indicator.addEventListener("click", (event) => {
    event.stopPropagation();
    state.reminderDialogPeer = root.querySelector("strong")?.textContent ? root.dataset.agentId : "";
    renderReminderDialog(state.reminderDialogPeer, reminders);
    elements.remindersDialog.showModal();
  });
}

function renderReminderDialog(peer, fallback = []) {
  const reminders = state.reminders.filter((reminder) => reminder.target === peer && ["scheduled", "due", "message_queued"].includes(reminder.state));
  const visible = reminders.length > 0 ? reminders : fallback;
  elements.remindersDialogBody.replaceChildren(...visible.map((reminder) => {
    const item = document.createElement("article");
    item.className = "reminder-list-item";
    item.innerHTML = `<strong>${escapeHtml(reminder.subject || "Reminder")}</strong><p>${escapeHtml(reminder.body)}</p><small>${escapeHtml(reminder.next_due_at || "")}</small>`;
    return item;
  }));
}

function arrivalIndicatorMarkup(arrival, extraClass = "") {
  if (!arrival) return "";
  const sender = humanName(arrival.from || "Unknown sender");
  const label = arrival.subject || arrival.preview || "New message";
  return `<span class="arrival-indicator${extraClass ? ` ${extraClass}` : ""}" role="button" tabindex="0" data-arrival-message-id="${escapeHtml(arrival.messageId || "")}" aria-label="Open new message from ${escapeHtml(sender)}"><i class="fa-solid fa-envelope arrival-icon" aria-hidden="true"></i><span class="arrival-copy"><strong>${escapeHtml(sender)}</strong><small>${escapeHtml(truncateText(label, 80))}</small></span><button class="arrival-dismiss" type="button" aria-label="Dismiss message from ${escapeHtml(sender)}"><i class="fa-solid fa-xmark" aria-hidden="true"></i></button></span>`;
}

function attachArrivalActions(root, arrival) {
  if (!arrival) return;
  const indicator = root.querySelector(".arrival-indicator");
  const dismiss = root.querySelector(".arrival-dismiss");
  if (!indicator) return;
  const openArrival = (event) => {
    event.stopPropagation();
    showArrivedMessageDialog(arrival);
  };
  indicator.addEventListener("click", openArrival);
  indicator.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    openArrival(event);
  });
  dismiss?.addEventListener("click", (event) => {
    event.stopPropagation();
    hideMessageArrival(arrival.agent);
  });
}

function subagentBadgeMarkup(peer, subagentCount, subagents, expanded, surface) {
  if (subagentCount <= 0) return "";
  const canExpandSubagents = subagents.length > 0;
  const listId = `subagents-${surface}-${cssSafeId(peer)}`;
  const countLabel = `${subagentCount} subagent${subagentCount === 1 ? "" : "s"}`;
  const badgeContent = expanded ? expandedSubagentMarkup(countLabel, subagents) : escapeHtml(countLabel);
  return `<span class="subagent-indicator${canExpandSubagents ? " expandable" : ""}${expanded ? " expanded" : ""}"${canExpandSubagents ? ` role="button" tabindex="0" aria-expanded="${expanded ? "true" : "false"}" aria-controls="${listId}" aria-label="${expanded ? "Hide" : "Show"} running subagents for ${escapeHtml(humanName(peer))}"` : ` aria-label="${subagentCount} running subagent${subagentCount === 1 ? "" : "s"}"`}><span class="subagent-dot"></span><span id="${listId}" class="subagent-badge-content">${badgeContent}</span></span>`;
}
function attachSubagentToggle(root, peer, canExpandSubagents, renderAfterToggle) {
  if (!canExpandSubagents) return;
  const toggle = root.querySelector(".subagent-indicator");
  if (!toggle) return;
  const toggleSubagents = (event) => {
    event.stopPropagation();
    event.preventDefault();
    if (state.expandedSubagents.has(peer)) state.expandedSubagents.delete(peer);
    else state.expandedSubagents.add(peer);
    renderAfterToggle();
    requestAnimationFrame(updateSubagentTooltips);
  };
  toggle.addEventListener("click", toggleSubagents);
  toggle.addEventListener("keydown", (event) => {
    if (event.key === "Enter" || event.key === " ") toggleSubagents(event);
  });
}
function expandedSubagentMarkup(countLabel, subagents) {
  const descriptions = subagents.map((subagent) => `<span class="subagent-list-item"><span class="subagent-dot"></span><span class="subagent-description">${escapeHtml(subagent.description)}</span></span>`).join("");
  return `<span class="subagent-expanded"><span class="subagent-count-label">${escapeHtml(countLabel)}</span><span class="subagent-divider"></span><span class="subagent-list">${descriptions}</span></span>`;
}
function normalizeSubagents(subagents) {
  if (!Array.isArray(subagents)) return [];
  return subagents.map((subagent) => {
    if (subagent === null || typeof subagent !== "object") return undefined;
    const id = typeof subagent.id === "string" ? subagent.id.trim().slice(0, 128) : "";
    const description = typeof subagent.description === "string" ? subagent.description.trim().slice(0, 512) : "";
    if (!id || !description) return undefined;
    const type = typeof subagent.type === "string" ? subagent.type.trim().slice(0, 64) : "";
    return type ? { id, type, description } : { id, description };
  }).filter(Boolean).slice(0, 999);
}
function sameSubagents(left = [], right = []) {
  const normalizedLeft = normalizeSubagents(left);
  const normalizedRight = normalizeSubagents(right);
  return normalizedLeft.length === normalizedRight.length && normalizedLeft.every((subagent, index) => {
    const other = normalizedRight[index];
    return other && subagent.id === other.id && subagent.type === other.type && subagent.description === other.description;
  });
}
function escapeHtml(value) {
  return String(value).replace(/[&<>'"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" }[char]));
}
