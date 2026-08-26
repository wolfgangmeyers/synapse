import { HISTORY_RETENTION_MS, mergeMessages, messageKey, messageTime, pruneHistoryMessages, reconcileHistoryMessages } from "./history.js";
import {
  canSendDraft,
  consumePendingModelControl,
  draftScope,
  historyRequestMatches,
  matchesActiveScope,
  mergeAgentModelProjection,
  mergeAgentModelSnapshot,
  modelControlStatusTitle,
  normalizeModelSummaries,
  normalizeModelSummary,
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
import { formatReminderCountdown, reminderRecurrenceLabel } from "./reminders.js";

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
  pendingModelControls: new Map(),
  pendingGoalControls: new Map(),
  pendingReminderLockControls: new Map(),
  pendingReminderCrud: new Map(),
  reminderLockStates: new Map(),
  deferredModelCatalogs: new Map(),
  modelControlUpdatedAtMs: new Map(),
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
  reminderDialogMode: "list",
  reminderDialogStatus: null,
  reminderDialogTimer: null,
  goalDialogPeer: "",
  goalDialogReturnFocus: null,
  goalDraft: null,
};
let modelPickerPeer = "";
let modelPickerSearchText = "";
let modelPickerOpen = false;
let modelPickerSelection;

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
  goalDialog: document.querySelector("#goal-dialog"),
  goalDialogTitle: document.querySelector("#goal-dialog-title"),
  goalDialogBody: document.querySelector("#goal-dialog-body"),
  goalDialogFooter: document.querySelector("#goal-dialog-footer"),
  goalHeaderButton: document.querySelector("#goal-header-button"),
  modelControls: document.querySelector("#model-controls"),
  modelPicker: document.querySelector("#model-picker"),
  modelCurrent: document.querySelector("#model-current"),
  modelCurrentLabel: document.querySelector("#model-current-label"),
  modelMenu: document.querySelector("#model-menu"),
  modelSearch: document.querySelector("#model-search"),
  modelCatalogNote: document.querySelector("#model-catalog-note"),
  modelOptions: document.querySelector("#model-options"),
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
elements.goalHeaderButton.addEventListener("click", () => {
  if (state.selectedPeer) openGoalEditor(state.selectedPeer, elements.goalHeaderButton);
});
elements.refresh.addEventListener("click", () => sendCommand({ type: "refresh_agents" }));
elements.modelCurrent.addEventListener("click", () => {
  setModelPickerOpen(!modelPickerOpen, { focus: !modelPickerOpen });
});
elements.modelSearch.addEventListener("input", () => {
  modelPickerSearchText = elements.modelSearch.value;
  renderModelOptions(normalizeModelSummaries(state.agents.find((agent) => agent.agent_id === state.selectedPeer)?.available_models));
});
document.addEventListener("click", (event) => {
  if (!event.target.closest("#model-picker")) setModelPickerOpen(false);
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && modelPickerOpen) setModelPickerOpen(false);
});
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
elements.remindersDialog.addEventListener("close", () => {
  clearReminderDialogTimer();
  state.reminderDialogPeer = "";
  clearReminderDialogStatus();
});
elements.goalDialog.addEventListener("close", () => {
  const peer = state.goalDialogPeer;
  const returnFocus = state.goalDialogReturnFocus;
  state.goalDialogPeer = "";
  state.goalDialogReturnFocus = null;
  state.goalDraft = null;
  if (returnFocus?.isConnected) returnFocus.focus();
  else [...document.querySelectorAll(".agent-row")].find((row) => row.dataset.agentId === peer)?.querySelector(".goal-indicator")?.focus();
});
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
  const socket = new WebSocket(`${protocol}//${location.host}/ui/events`);
  state.socket = socket;
  socket.addEventListener("open", () => sendCommand({ type: "connect", routerUrl: state.routerUrl, agentId: state.agentId }));
  socket.addEventListener("message", (event) => { void handleEvent(JSON.parse(event.data)); });
  socket.addEventListener("close", () => {
    if (state.socket !== socket) return;
    clearPendingModelControls();
    clearPendingGoalControls();
    clearPendingReminderLockControls();
    state.pendingReminderCrud.clear();
    state.reminderLockStates.clear();
    setStatus("reconnecting", "UI server connection closed; retrying…");
    setTimeout(connectUiSocket, 1000);
  });
}

function sendCommand(command) {
  if (state.socket?.readyState !== WebSocket.OPEN) return false;
  state.socket.send(JSON.stringify(command));
  return true;
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
    const incomingAgents = event.agents.filter((agent) => agent.agent_id !== state.agentId);
    const incomingAgentIds = new Set(incomingAgents.map((agent) => agent.agent_id));
    for (const current of state.agents) {
      if (current.agent_id !== state.agentId && !incomingAgentIds.has(current.agent_id)) {
        clearReminderLockState(current.agent_id);
      }
    }
    for (const agent of incomingAgents) {
      const current = state.agents.find((candidate) => candidate.agent_id === agent.agent_id);
      if (current?.instance_id !== undefined && agent.instance_id !== undefined && current.instance_id !== agent.instance_id) {
        clearReminderLockState(agent.agent_id);
      }
    }
    state.agents = incomingAgents.map((agent) => mergeAgentSnapshot(agent, event.generatedAt));
    if (Number.isSafeInteger(event.activitySnapshotVersion)) state.lastLifecycleSnapshotVersion = Math.max(state.lastLifecycleSnapshotVersion, event.activitySnapshotVersion);
    renderAgents();
    renderConversation();
    if (elements.goalDialog?.open && state.goalDialogPeer && state.goalDraft === null) renderGoalDialog(state.goalDialogPeer);
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
  if (event.type === "agent_goal_state") {
    updateAgentGoalState(event.agent, event.instanceId, event.goalState);
    return;
  }
  if (event.type === "goal_control_result") {
    handleGoalControlResult(event);
    return;
  }
  if (event.type === "reminder_lock_control_result") {
    handleReminderLockControlResult(event);
    return;
  }
  if (event.type === "reminder_crud_result") {
    handleReminderCrudResult(event);
    return;
  }
  if (event.type === "agent_activity") {
    updateAgentActivity(
      event.agent,
      event.activityState,
      event.subagentCount,
      event.subagents,
      event.goal,
      event.instanceId,
      Object.prototype.hasOwnProperty.call(event, "goal"),
      event.currentModel,
    );
    return;
  }
  if (event.type === "agent_model_catalog") {
    const projection = {
      ...(Object.hasOwn(event, "currentModel") ? { currentModel: event.currentModel } : {}),
      availableModels: event.availableModels,
      totalCount: event.totalCount,
      truncated: event.truncated,
    };
    if (normalizeModelSummaries(projection.availableModels).length === 0
      && deferEmptyModelCatalog(event.agent, event.instanceId, projection)) return;
    clearDeferredModelCatalog(event.agent, event.instanceId);
    updateAgentModelCatalog(event.agent, event.instanceId, projection);
    return;
  }
  if (event.type === "model_control_result") {
    const settled = consumePendingModelControl(state.pendingModelControls, event);
    if (settled === undefined) return;
    const targetAgentId = settled.pending.peer;
    const projection = settled.projection;
    if (normalizeModelSummary(event.currentModel) !== undefined) {
      state.modelControlUpdatedAtMs.set(targetAgentId, Date.now());
    }
    if (Object.keys(projection).length > 0) {
      updateAgentModelCatalog(targetAgentId, event.instanceId, projection, { render: false, replaceInstance: false });
    }
    const deferredCatalogKey = modelCatalogKey(targetAgentId, settled.pending.instanceId);
    if (!hasPendingModelControl(targetAgentId, settled.pending.instanceId)) {
      const deferredCatalog = state.deferredModelCatalogs.get(deferredCatalogKey);
      if (deferredCatalog !== undefined) {
        state.deferredModelCatalogs.delete(deferredCatalogKey);
        updateAgentModelCatalog(targetAgentId, settled.pending.instanceId, deferredCatalog, { render: false, replaceInstance: false });
      }
    }
    if (modelPickerSelection?.controlId === event.controlId) modelPickerSelection = undefined;
    if (targetAgentId === state.selectedPeer) renderModelControls(state.agents.find((agent) => agent.agent_id === state.selectedPeer));
    showModelControlStatus(event.accepted, targetAgentId, normalizeModelSummary(event.currentModel), event.errorCode);
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
  closeReminderDialog();
  closeGoalDialog();
  clearPendingModelControls({ render: false });
  clearPendingGoalControls();
  clearPendingReminderLockControls();
  state.pendingReminderCrud.clear();
  state.reminderLockStates.clear();
  saveCurrentDraft();
  state.routerUrl = routerUrl;
  state.agentId = agentIdForRouter(routerUrl, state.defaultAgentId);
  state.agents = [];
  state.modelControlUpdatedAtMs.clear();
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

function showModelControlStatus(accepted, targetAgentId, currentModel, errorCode) {
  const target = targetAgentId || "agent";
  const confirmed = accepted === true && currentModel !== undefined;
  elements.statusDot.className = `status-dot ${confirmed ? "connected" : "offline"}`;
  elements.statusTitle.textContent = modelControlStatusTitle(confirmed, errorCode);
  elements.statusDetail.textContent = confirmed
    ? `${target} · ${currentModel === undefined ? "unknown model" : modelKey(currentModel)}`
    : `${target} · ${errorCode || "unavailable_model"}`;
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
    const goal = normalizeGoalState(agent) ?? normalizeGoalProjection(agent.goal);
    const row = document.createElement("div");
    row.tabIndex = 0;
    row.setAttribute("role", "button");
    row.dataset.agentId = peer;
    row.className = `agent-row${peer === state.selectedPeer ? " active" : ""}${active ? " working" : ""}${compacting ? " compacting" : ""}${subagentCount > 0 ? " has-subagents" : ""}${arrival ? " has-arrival" : ""}${expanded ? " expanded" : ""}`;
    const badge = compacting ? compactingBadgeMarkup() : subagentBadgeMarkup(peer, subagentCount, subagents, expanded, "row");
    row.innerHTML = `<span class="avatar${compacting ? " compacting" : ""}">${escapeHtml(monogram(peer))}</span><span class="agent-row-text"><span class="agent-name-line">${reminderIndicatorMarkup(reminders, humanName(peer))}${goalIndicatorMarkup(goal, humanName(peer))}<strong>${escapeHtml(humanName(peer))}</strong></span>${badge}${arrivalIndicatorMarkup(arrival)}</span>${active ? '<span class="working-indicator" aria-label="Agent is working"><span></span></span>' : ""}`;
    row.addEventListener("click", () => selectPeer(peer));
    row.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        selectPeer(peer);
      }
    });
    attachSubagentToggle(row, peer, canExpandSubagents, renderAgents);
    attachReminderActions(row, reminders);
    attachGoalActions(row, goal);
    attachArrivalActions(row, arrival);
    return row;
  }));
  requestAnimationFrame(updateSubagentTooltips);
}

function selectPeer(peer) {
  closeReminderDialog();
  closeGoalDialog();
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

function updateAgentActivity(peer, activityState, subagentCount = 0, subagents = [], incomingGoal, instanceId, goalProvided = incomingGoal !== undefined, incomingModel) {
  if (!peer || peer === state.agentId) return;
  const current = state.agents.find((agent) => agent.agent_id === peer) ?? { agent_id: peer, activity_state: "idle" };
  const sameInstance = instanceId === undefined || current.instance_id === instanceId;
  const base = sameInstance ? current : { agent_id: peer, activity_state: "idle", instance_id: instanceId };
  const incomingSummaries = normalizeSubagents(subagents);
  const incomingCount = Number.isSafeInteger(subagentCount) ? subagentCount : 0;
  const currentSummaries = normalizeSubagents(base?.subagents);
  const summaries = incomingSummaries.length > 0
    ? incomingSummaries
    : (incomingCount > 0 && currentSummaries.length === incomingCount ? currentSummaries : []);
  const nextGoal = goalProvided ? normalizeGoalProjection(incomingGoal) : (sameInstance ? normalizeGoalProjection(base.goal) : undefined);
  const nextModel = normalizeModelSummary(incomingModel) ?? (sameInstance ? normalizeModelSummary(base.current_model) : undefined);
  const nextModels = normalizeModelSummaries(sameInstance ? base.available_models : []);
  const normalizedCount = Math.max(summaries.length, incomingCount);
  const hasDurableLifecycle = Number.isSafeInteger(base.subagent_version);
  if (!hasDurableLifecycle && normalizedCount === 0) state.expandedSubagents.delete(peer);
  if (base.activity_state === activityState && hasDurableLifecycle && JSON.stringify(base.goal) === JSON.stringify(nextGoal)
    && JSON.stringify(base.current_model) === JSON.stringify(nextModel) && JSON.stringify(base.available_models) === JSON.stringify(nextModels)) return;
  // sameSubagents(current.subagents, summaries)) return; is equivalent for the same-incarnation base below.
  if (base.activity_state === activityState && (base.subagent_count ?? 0) === normalizedCount && sameSubagents(base.subagents, summaries) && JSON.stringify(base.goal) === JSON.stringify(nextGoal)
    && JSON.stringify(base.current_model) === JSON.stringify(nextModel) && JSON.stringify(base.available_models) === JSON.stringify(nextModels) && base.instance_id === instanceId) return;
  const next = hasDurableLifecycle
    ? { ...base, activity_state: activityState, ...(instanceId === undefined ? {} : { instance_id: instanceId }), ...(nextGoal === undefined ? {} : { goal: nextGoal }), ...(nextGoal === undefined ? {} : { goal_state: mergeGoalProjection(base.goal_state, nextGoal) }), ...(nextModel === undefined ? {} : { current_model: nextModel }), available_models: nextModels }
    : { ...base, activity_state: activityState, subagent_count: normalizedCount, subagents: summaries, ...(instanceId === undefined ? {} : { instance_id: instanceId }), ...(nextGoal === undefined ? {} : { goal: nextGoal }), ...(nextGoal === undefined ? {} : { goal_state: mergeGoalProjection(base.goal_state, nextGoal) }), ...(nextModel === undefined ? {} : { current_model: nextModel }), available_models: nextModels };
  state.agents = state.agents.some((agent) => agent.agent_id === peer)
    ? state.agents.map((agent) => agent.agent_id === peer ? next : agent)
    : [...state.agents, next];
  renderAgents();
  renderConversation();
  if (elements.goalDialog?.open && state.goalDialogPeer && state.goalDraft === null) renderGoalDialog(state.goalDialogPeer);
}

function updateAgentGoalState(peer, instanceId, incoming) {
  if (!peer || peer === state.agentId) return;
  const normalized = normalizeGoalStateValue(incoming);
  if (!normalized) return;
  const current = state.agents.find((agent) => agent.agent_id === peer);
  if (current?.instance_id !== undefined && instanceId !== undefined && current.instance_id !== instanceId) {
    state.agents = state.agents.map((agent) => agent.agent_id === peer
      ? { ...agent, instance_id: instanceId, goal_state: normalized, goal: normalizeGoalProjection(normalized) }
      : agent);
  } else {
    const next = { ...(current ?? { agent_id: peer, activity_state: "idle" }), ...(instanceId === undefined ? {} : { instance_id: instanceId }), goal_state: normalized, goal: normalizeGoalProjection(normalized) };
    state.agents = current ? state.agents.map((agent) => agent.agent_id === peer ? next : agent) : [...state.agents, next];
  }
  renderAgents();
  renderConversation();
  if (elements.goalDialog?.open && state.goalDialogPeer === peer && state.goalDraft === null) renderGoalDialog(peer);
}

function handleGoalControlResult(event) {
  const pending = state.pendingGoalControls.get(event.controlId);
  if (!pending || pending.peer !== event.targetAgentId || pending.instanceId !== event.targetInstanceId) return;
  state.pendingGoalControls.delete(event.controlId);
  if (event.accepted === true && event.goalState) updateAgentGoalState(event.targetAgentId, event.targetInstanceId, event.goalState);
  const message = event.accepted === true ? "Goal saved." : goalControlErrorMessage(event.errorCode, event.message);
  if (event.accepted === true) closeGoalDialog();
  else setGoalDialogStatus(message, true);
}

function goalControlErrorMessage(code, message) {
  if (message) return String(message);
  const labels = {
    target_instance_changed: "That agent restarted. Close this editor and try again.",
    target_not_connected: "That agent is no longer connected.",
    goal_control_unavailable: "Goal controls are not available on this router.",
    goal_control_blocking_unavailable: "This router does not support disabling goal blocking.",
    invalid_settings: "Check the goal settings and try again.",
    invalid_goal: "Enter a goal before saving.",
    control_timeout: "The goal change timed out.",
    router_disconnected: "The router connection closed before the change was confirmed.",
    router_reconnecting: "The router is reconnecting.",
  };
  return labels[code] || "The goal change was not saved.";
}

function updateAgentModelCatalog(peer, instanceId, projection, { render = true, replaceInstance = true } = {}) {
  if (!peer || peer === state.agentId) return;
  const merged = mergeAgentModelProjection(state.agents, peer, instanceId, projection, { replaceInstance });
  if (!merged.changed) return;
  state.agents = merged.agents;
  if (render) {
    renderAgents();
    renderConversation();
    if (elements.goalDialog?.open && state.goalDialogPeer) renderGoalDialog(state.goalDialogPeer);
  }
}

function modelCatalogKey(peer, instanceId) {
  return `${peer}\u001f${instanceId ?? ""}`;
}

function pendingModelControlMatchesAgent(pending, peer, instanceId) {
  return pending?.peer === peer
    && (pending.instanceId === undefined || instanceId === undefined || pending.instanceId === instanceId);
}

function pendingModelControlsForAgent(peer, instanceId) {
  return [...state.pendingModelControls.values()].filter((pending) => pendingModelControlMatchesAgent(pending, peer, instanceId));
}

function hasPendingModelControl(peer, instanceId) {
  return pendingModelControlsForAgent(peer, instanceId).length > 0;
}

function deferEmptyModelCatalog(peer, instanceId, projection) {
  const pendingControls = pendingModelControlsForAgent(peer, instanceId);
  if (pendingControls.length === 0) return false;
  for (const pending of pendingControls) {
    state.deferredModelCatalogs.set(modelCatalogKey(peer, pending.instanceId), {
      availableModels: [],
      totalCount: projection.totalCount,
      truncated: projection.truncated,
    });
  }
  return true;
}

function clearDeferredModelCatalog(peer, instanceId) {
  state.deferredModelCatalogs.delete(modelCatalogKey(peer, instanceId));
  for (const pending of pendingModelControlsForAgent(peer, instanceId)) {
    state.deferredModelCatalogs.delete(modelCatalogKey(peer, pending.instanceId));
  }
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
  const hasGoal = (normalizeGoalState(selectedAgent) ?? normalizeGoalProjection(selectedAgent?.goal))?.enabled === true;
  elements.goalHeaderButton.disabled = !peer;
  elements.goalHeaderButton.classList.toggle("has-goal", hasGoal);
  elements.goalHeaderButton.innerHTML = '<i class="fa-solid fa-flag" aria-hidden="true"></i>';
  const goalLabel = hasGoal ? `Edit goal for ${humanName(peer)}` : `Set goal for ${humanName(peer)}`;
  elements.goalHeaderButton.setAttribute("aria-label", peer ? goalLabel : "Select an agent to set a goal");
  elements.goalHeaderButton.title = peer ? goalLabel : "Select an agent to set a goal";
  renderModelControls(selectedAgent);
  const active = selectedAgent?.activity_state === "active";
  const compacting = selectedAgent?.activity_state === "compacting";
  elements.peerName.innerHTML = peer ? `${escapeHtml(humanName(peer))}${active ? '<span class="working-indicator inline" aria-label="Agent is working"><span></span></span>' : ""}${compacting ? compactingBadgeMarkup("inline") : ""}` : "Pick an agent";
  elements.peerName.append(elements.goalHeaderButton);
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

function mergeAgentSnapshot(agent, snapshotGeneratedAt) {
  const current = state.agents.find((candidate) => candidate.agent_id === agent.agent_id);
  const snapshotGeneratedAtMs = Date.parse(snapshotGeneratedAt);
  const modelControlUpdatedAtMs = state.modelControlUpdatedAtMs.get(agent.agent_id);
  const pendingEmptyCatalog = Object.hasOwn(agent, "available_models")
    && normalizeModelSummaries(agent.available_models).length === 0
    && deferEmptyModelCatalog(agent.agent_id, agent.instance_id, {
      availableModels: agent.available_models,
      totalCount: agent.model_catalog_total_count,
      truncated: agent.model_catalog_truncated,
    });
  const modelSnapshot = pendingEmptyCatalog && current?.available_models !== undefined
    ? {
      ...agent,
      available_models: current.available_models,
      model_catalog_total_count: current.model_catalog_total_count,
      model_catalog_truncated: current.model_catalog_truncated,
    }
    : agent;
  const modelFields = mergeAgentModelSnapshot(current, modelSnapshot, {
    snapshotGeneratedAtMs,
    modelControlUpdatedAtMs,
  });
  if (Number.isFinite(snapshotGeneratedAtMs)
    && Number.isFinite(modelControlUpdatedAtMs)
    && snapshotGeneratedAtMs >= modelControlUpdatedAtMs) {
    state.modelControlUpdatedAtMs.delete(agent.agent_id);
  }
  const projectedAgent = { ...agent, ...modelFields };
  const incomingGoalState = normalizeGoalStateValue(agent.goal_state);
  const currentGoalState = normalizeGoalStateValue(current?.goal_state);
  const sameGoalInstance = current === undefined || agent.instance_id === undefined || current.instance_id === undefined || agent.instance_id === current.instance_id;
  const goalState = incomingGoalState ?? (sameGoalInstance ? currentGoalState : undefined);
  const normalizedGoal = goalState ? normalizeGoalProjection(goalState) : normalizeGoalProjection(agent.goal);
  const goalFields = {
    ...(normalizedGoal ? { goal: normalizedGoal } : {}),
    ...(goalState ? { goal_state: goalState } : {}),
  };
  if (Number.isSafeInteger(current?.subagent_version)
    && (!Number.isSafeInteger(agent.subagent_version)
      || agent.subagent_version < current.subagent_version
      || (agent.subagent_version === current.subagent_version
        && ((agent.subagent_count ?? 0) !== (current.subagent_count ?? 0)
          || !sameSubagents(agent.subagents, current.subagents))))) {
    return { ...projectedAgent, ...goalFields, subagent_version: current.subagent_version, subagent_count: current.subagent_count, subagents: current.subagents };
  }
  const incomingSummaries = normalizeSubagents(agent.subagents);
  const incomingCount = Number.isSafeInteger(agent.subagent_count) ? agent.subagent_count : 0;
  if (incomingSummaries.length > 0 || incomingCount === 0) return { ...projectedAgent, ...goalFields, subagents: incomingSummaries };
  const currentSummaries = normalizeSubagents(current?.subagents);
  return currentSummaries.length === incomingCount
    ? { ...projectedAgent, ...goalFields, subagents: currentSummaries }
    : { ...projectedAgent, ...goalFields, subagents: [] };
}
function compactingBadgeMarkup(extraClass = "") {
  return `<span class="compacting-indicator${extraClass ? ` ${extraClass}` : ""}" aria-label="Agent is compacting"><span class="compacting-dot"></span><span>Compacting</span></span>`;
}

const DEFAULT_GOAL_STATE = {
  enabled: false,
  goal: "",
  blocking: true,
  goal_lock_enabled: false,
  snooze_enabled: true,
  snooze_duration_ms: 60_000,
  snooze_turn_limit: null,
  snooze_budget_cap_ms: null,
  snooze_refill_yield_ms: 30_000,
  snooze_refill_elapsed_ms: 60_000,
  outcome_counts: { BLOCKED: 0, COMPLETED: 0, NOT_DONE: 0, WAITING_FOR_USER_AUTHORIZATION: 0 },
};

function normalizeGoalProjection(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)
    || typeof value.enabled !== "boolean" || typeof value.goal !== "string"
    || value.goal.length > 4000) return undefined;
  if (value.enabled && value.goal.length === 0) return undefined;
  return { enabled: value.enabled, goal: value.enabled ? value.goal : "" };
}

function normalizeGoalStateValue(value) {
  const projection = normalizeGoalProjection(value);
  if (!projection || (Object.hasOwn(value, "blocking") && typeof value.blocking !== "boolean")
    || typeof value.goal_lock_enabled !== "boolean" || typeof value.snooze_enabled !== "boolean"
    || !positiveGoalInteger(value.snooze_duration_ms) || !positiveGoalInteger(value.snooze_refill_yield_ms)
    || !positiveGoalInteger(value.snooze_refill_elapsed_ms)
    || !(value.snooze_turn_limit === null || (Number.isSafeInteger(value.snooze_turn_limit) && value.snooze_turn_limit >= 1 && value.snooze_turn_limit <= 20))
    || !(value.snooze_budget_cap_ms === null || positiveGoalInteger(value.snooze_budget_cap_ms))) return undefined;
  const counts = value.outcome_counts;
  if (!counts || typeof counts !== "object" || !["BLOCKED", "COMPLETED", "NOT_DONE", "WAITING_FOR_USER_AUTHORIZATION"].every((key) => Number.isSafeInteger(counts[key]) && counts[key] >= 0)) return undefined;
  return {
    ...projection,
    blocking: value.blocking !== false,
    goal_lock_enabled: value.goal_lock_enabled,
    snooze_enabled: value.snooze_enabled,
    snooze_duration_ms: value.snooze_duration_ms,
    snooze_turn_limit: value.snooze_turn_limit,
    snooze_budget_cap_ms: value.snooze_budget_cap_ms,
    snooze_refill_yield_ms: value.snooze_refill_yield_ms,
    snooze_refill_elapsed_ms: value.snooze_refill_elapsed_ms,
    outcome_counts: { ...DEFAULT_GOAL_STATE.outcome_counts, ...counts },
  };
}

function normalizeGoalState(agent) {
  return normalizeGoalStateValue(agent?.goal_state);
}

function mergeGoalProjection(current, projection) {
  if (!projection) return current;
  const full = normalizeGoalStateValue(current);
  return full ? { ...full, ...projection } : { ...DEFAULT_GOAL_STATE, ...projection, outcome_counts: { ...DEFAULT_GOAL_STATE.outcome_counts } };
}

function positiveGoalInteger(value) {
  return Number.isSafeInteger(value) && value > 0;
}

function modelKey(model) {
  return `${model.provider}/${model.id}`;
}

function modelOptionLabel(model) {
  return model.name ? `${model.name} (${model.provider})` : modelKey(model);
}

function setModelPickerOpen(open, { focus = false } = {}) {
  modelPickerOpen = open;
  elements.modelMenu.hidden = !open;
  elements.modelCurrent.setAttribute("aria-expanded", String(open));
  if (open) {
    renderModelOptions(normalizeModelSummaries(state.agents.find((agent) => agent.agent_id === state.selectedPeer)?.available_models));
    if (focus) requestAnimationFrame(() => elements.modelSearch.focus());
  }
}

function selectModel(model) {
  const peer = state.selectedPeer;
  if (!peer) return;
  const targetInstanceId = state.agents.find((agent) => agent.agent_id === peer)?.instance_id;
  if (!targetInstanceId) return;
  const controlId = `model-control-${crypto.randomUUID()}`;
  state.pendingModelControls.set(controlId, { peer, model, instanceId: targetInstanceId });
  modelPickerSelection = { peer, model, controlId };
  elements.modelCurrentLabel.textContent = modelKey(model);
  modelPickerSearchText = "";
  elements.modelSearch.value = "";
  setModelPickerOpen(false);
  if (!sendCommand({
    type: "set_model",
    controlId,
    targetAgentId: peer,
    targetInstanceId,
    model: { provider: model.provider, id: model.id },
  })) {
    state.pendingModelControls.delete(controlId);
    state.deferredModelCatalogs.delete(modelCatalogKey(peer, targetInstanceId));
    modelPickerSelection = undefined;
    renderModelControls(state.agents.find((agent) => agent.agent_id === peer));
    showModelControlStatus(false, peer, undefined, "ui_not_connected");
  }
}

function clearPendingModelControls({ render = true } = {}) {
  state.pendingModelControls.clear();
  state.deferredModelCatalogs.clear();
  modelPickerSelection = undefined;
  if (render) renderModelControls(state.agents.find((agent) => agent.agent_id === state.selectedPeer));
}

function clearPendingGoalControls() {
  state.pendingGoalControls.clear();
}

function clearPendingReminderLockControls() {
  state.pendingReminderLockControls.clear();
}

function handleReminderCrudResult(event) {
  const pending = state.pendingReminderCrud.get(event.requestId);
  if (!pending) return;
  state.pendingReminderCrud.delete(event.requestId);
  if (event.accepted === true) {
    state.reminderDialogMode = "list";
    clearReminderDialogStatus();
    sendCommand({ type: "refresh_agents" });
    if (elements.remindersDialog?.open) renderReminderDialog(pending.peer);
    return;
  }
  setReminderDialogStatus(event.body?.error?.message || event.body?.error?.code || `Reminder request failed (${event.status || "unknown"})`, true);
  if (elements.remindersDialog?.open) renderReminderDialog(pending.peer);
}

function clearReminderLockState(peer) {
  const prefix = `${peer}:`;
  for (const key of state.reminderLockStates.keys()) {
    if (key.startsWith(prefix)) state.reminderLockStates.delete(key);
  }
  for (const [controlId, pending] of state.pendingReminderLockControls) {
    if (pending.peer === peer) state.pendingReminderLockControls.delete(controlId);
  }
}

function reminderLockKey(peer, reminderId) {
  return `${peer}:${reminderId}`;
}

function handleReminderLockControlResult(event) {
  const pending = state.pendingReminderLockControls.get(event.controlId);
  if (!pending || pending.peer !== event.targetAgentId || pending.instanceId !== event.targetInstanceId || pending.reminderId !== event.reminderId) return;
  state.pendingReminderLockControls.delete(event.controlId);
  const key = reminderLockKey(pending.peer, pending.reminderId);
  state.reminderLockStates.set(key, event.accepted === true
    ? { locked: pending.action === "set", message: pending.action === "set" ? "Native edits blocked." : "Unlocked." }
    : { locked: false, error: reminderLockErrorMessage(event.errorCode) });
  if (elements.remindersDialog?.open && state.reminderDialogPeer) renderReminderDialog(state.reminderDialogPeer);
}

function reminderLockErrorMessage(code) {
  const labels = {
    control_timeout: "The lock change timed out.",
    router_disconnected: "The router connection closed before the lock change was confirmed.",
    router_reconnecting: "The router is reconnecting.",
    target_not_connected: "That agent is no longer connected.",
    reminder_not_found: "That reminder no longer exists.",
  };
  return labels[code] || "The lock change was not accepted.";
}

function renderModelOptions(models) {
  const query = modelPickerSearchText.trim().toLowerCase();
  const visibleModels = models.filter((model) => `${modelKey(model)} ${modelOptionLabel(model)}`.toLowerCase().includes(query));
  if (visibleModels.length === 0) {
    const empty = document.createElement("p");
    empty.className = "model-options-empty";
    empty.textContent = "No matching models";
    elements.modelOptions.replaceChildren(empty);
    return;
  }
  elements.modelOptions.replaceChildren(...visibleModels.map((model) => {
    const option = document.createElement("button");
    option.className = "model-option";
    option.type = "button";
    option.role = "option";
    option.setAttribute("aria-selected", String(modelKey(model) === elements.modelCurrentLabel.textContent));
    const name = document.createElement("strong");
    name.textContent = modelOptionLabel(model);
    const key = document.createElement("small");
    key.textContent = modelKey(model);
    option.append(name, key);
    option.addEventListener("click", () => selectModel(model));
    return option;
  }));
}

function renderModelControls(agent) {
  const models = normalizeModelSummaries(agent?.available_models);
  elements.modelControls.hidden = agent === undefined || models.length === 0;
  const totalCount = Number.isSafeInteger(agent?.model_catalog_total_count)
    ? Math.max(models.length, agent.model_catalog_total_count)
    : models.length;
  elements.modelCatalogNote.hidden = agent?.model_catalog_truncated !== true;
  elements.modelCatalogNote.textContent = agent?.model_catalog_truncated === true
    ? `Showing ${models.length} of ${totalCount} models.`
    : "";
  if (elements.modelControls.hidden) {
    setModelPickerOpen(false);
    modelPickerPeer = "";
    modelPickerSelection = undefined;
    modelPickerSearchText = "";
    elements.modelSearch.value = "";
    elements.modelCurrentLabel.textContent = "Select model";
    elements.modelOptions.replaceChildren();
    return;
  }
  if (modelPickerPeer !== state.selectedPeer) {
    modelPickerPeer = state.selectedPeer;
    modelPickerSelection = undefined;
    modelPickerSearchText = "";
    elements.modelSearch.value = "";
  }
  const selectedModel = modelPickerSelection?.peer === state.selectedPeer
    ? normalizeModelSummary(modelPickerSelection.model)
    : undefined;
  const currentModel = normalizeModelSummary(agent.current_model);
  const current = selectedModel && (!currentModel || modelKey(selectedModel) !== modelKey(currentModel))
    ? selectedModel
    : currentModel;
  elements.modelCurrentLabel.textContent = current === undefined ? "Select model" : modelKey(current);
  if (modelPickerOpen) renderModelOptions(models);
}

function goalIndicatorMarkup(goal, peerName) {
  if (!goal?.enabled) return "";
  return `<button class="goal-indicator" type="button" aria-haspopup="dialog" aria-controls="goal-dialog" aria-label="Show goal for ${escapeHtml(peerName)}" title="Show goal"><i class="fa-solid fa-flag" aria-hidden="true"></i></button>`;
}

function reminderIndicatorMarkup(reminders, peerName) {
  const empty = reminders.length === 0;
  const reminderLabel = empty ? `Add a reminder for ${peerName}` : `Show ${reminders.length} active reminder${reminders.length === 1 ? "" : "s"} for ${peerName}`;
  return `<button class="reminder-indicator${empty ? " empty" : ""}" type="button" aria-haspopup="dialog" aria-controls="reminders-dialog" aria-label="${escapeHtml(reminderLabel)}" title="${escapeHtml(reminderLabel)}"><i class="fa-solid fa-clock" aria-hidden="true"></i></button>`;
}

function attachGoalActions(root, goal) {
  if (!goal) return;
  const indicator = root.querySelector(".goal-indicator");
  if (!indicator) return;
  const openGoal = (event) => {
    event.stopPropagation();
    event.preventDefault();
    openGoalEditor(root.dataset.agentId, indicator);
  };
  indicator.addEventListener("click", openGoal);
  indicator.addEventListener("keydown", (event) => {
    if (event.key === "Enter" || event.key === " ") openGoal(event);
  });
}

function openGoalEditor(peer, returnFocus) {
  const agent = state.agents.find((candidate) => candidate.agent_id === peer);
  const source = normalizeGoalState(agent) ?? {
    ...DEFAULT_GOAL_STATE,
    ...(normalizeGoalProjection(agent?.goal) ?? {}),
  };
  state.goalDialogPeer = peer;
  state.goalDialogReturnFocus = returnFocus;
  state.goalDraft = goalDraftFromState(source);
  renderGoalDialog(peer);
  elements.goalDialog.showModal();
  requestAnimationFrame(() => elements.goalDialogBody.querySelector("button, textarea, input")?.focus());
}

function closeGoalDialog() {
  if (elements.goalDialog?.open) elements.goalDialog.close();
  else {
    state.goalDialogPeer = "";
    state.goalDraft = null;
  }
}

function goalDraftFromState(stateValue) {
  return {
    enabled: stateValue.enabled === true,
    goal: stateValue.goal || "",
    blocking: stateValue.blocking !== false,
    goal_lock_enabled: stateValue.goal_lock_enabled === true,
    snooze_enabled: stateValue.snooze_enabled !== false,
    snooze_duration_ms: stateValue.snooze_duration_ms || DEFAULT_GOAL_STATE.snooze_duration_ms,
    snooze_turn_limit: stateValue.snooze_turn_limit ?? null,
    snooze_budget_cap_ms: stateValue.snooze_budget_cap_ms ?? null,
    snooze_refill_yield_ms: stateValue.snooze_refill_yield_ms || DEFAULT_GOAL_STATE.snooze_refill_yield_ms,
    snooze_refill_elapsed_ms: stateValue.snooze_refill_elapsed_ms || DEFAULT_GOAL_STATE.snooze_refill_elapsed_ms,
    outcome_counts: { ...DEFAULT_GOAL_STATE.outcome_counts, ...(stateValue.outcome_counts || {}) },
  };
}

function renderGoalDialog(peer) {
  const draft = state.goalDraft;
  if (!draft || state.goalDialogPeer !== peer) return;
  elements.goalDialogTitle.textContent = `Goal for ${humanName(peer)}`;
  elements.goalDialogBody.replaceChildren();
  elements.goalDialogFooter.replaceChildren();
  const form = document.createElement("div");
  form.className = "goal-editor-form";
  form.addEventListener("input", (event) => updateGoalDraftFromInput(event.target));
  form.append(goalToggle("goal-enabled", "Goal", draft.enabled));
  const goalLabel = document.createElement("label");
  goalLabel.textContent = "What should this agent work toward?";
  const goalText = document.createElement("textarea");
  goalText.id = "goal-text";
  goalText.maxLength = 4000;
  goalText.rows = 10;
  goalText.placeholder = "Describe the goal in Markdown";
  goalText.value = draft.goal;
  goalText.disabled = !draft.enabled;
  goalLabel.append(goalText);
  form.append(goalLabel);
  form.append(goalToggle("goal-blocking", "Blocking", draft.blocking));
  form.append(goalToggle("goal-lock", "Goal lock", draft.goal_lock_enabled, { lock: true }));
  const snoozeFieldset = document.createElement("fieldset");
  const snoozeLegend = document.createElement("legend");
  snoozeLegend.textContent = "Snooze settings";
  snoozeFieldset.append(snoozeLegend, goalToggle("snooze-enabled", "Allow snoozing", draft.snooze_enabled));
  snoozeFieldset.append(numberField("snooze-duration", "Snooze duration (seconds)", draft.snooze_duration_ms / 1000, 1));
  snoozeFieldset.append(numberField("snooze-turn-limit", "Snooze turn limit (optional)", draft.snooze_turn_limit ?? "", 1, 20));
  snoozeFieldset.append(numberField("snooze-budget", "Snooze budget (minutes, optional)", draft.snooze_budget_cap_ms === null ? "" : draft.snooze_budget_cap_ms / 60_000, 1));
  snoozeFieldset.append(numberField("snooze-yield", "Snooze refill yield (seconds)", draft.snooze_refill_yield_ms / 1000, 1));
  snoozeFieldset.append(numberField("snooze-elapsed", "Snooze refill time (seconds)", draft.snooze_refill_elapsed_ms / 1000, 1));
  form.append(snoozeFieldset);
  const counts = document.createElement("p");
  counts.className = "goal-outcome-counts";
  counts.textContent = goalOutcomeCountsLabel(draft.outcome_counts);
  form.append(counts);
  const status = document.createElement("p");
  status.id = "goal-editor-status";
  status.className = "goal-editor-status";
  const actions = document.createElement("div");
  actions.className = "goal-editor-actions";
  const cancel = document.createElement("button");
  cancel.type = "button";
  cancel.className = "secondary";
  cancel.textContent = "Cancel";
  cancel.addEventListener("click", closeGoalDialog);
  const save = document.createElement("button");
  save.type = "button";
  save.textContent = "Save";
  save.addEventListener("click", () => sendGoalControl());
  actions.append(cancel, save);
  elements.goalDialogBody.append(form);
  elements.goalDialogFooter.append(status, actions);
}

function goalToggle(id, labelText, pressed, { lock = false } = {}) {
  const button = document.createElement("button");
  button.type = "button";
  button.id = id;
  button.className = `goal-toggle${lock ? " goal-lock-toggle" : ""}`;
  const label = document.createElement("span");
  label.className = "goal-toggle-label";
  label.textContent = labelText;
  const track = document.createElement("span");
  track.className = "goal-toggle-track";
  track.setAttribute("aria-hidden", "true");
  const thumb = document.createElement("span");
  thumb.className = "goal-toggle-thumb";
  track.append(thumb);
  const status = document.createElement("span");
  status.className = "goal-toggle-state";
  if (lock) status.classList.add("goal-toggle-state-hidden");
  const icon = lock ? document.createElement("i") : undefined;
  if (icon) icon.setAttribute("aria-hidden", "true");
  if (lock) thumb.append(icon, status);
  else thumb.append(status);
  button.append(label, track);
  button.addEventListener("click", () => {
    setGoalToggleState(button, button.getAttribute("aria-pressed") !== "true");
    updateGoalDraftFromInput(button);
  });
  setGoalToggleState(button, pressed);
  return button;
}

function setGoalToggleState(button, pressed) {
  const lock = button.classList.contains("goal-lock-toggle");
  button.setAttribute("aria-pressed", String(pressed));
  button.setAttribute("aria-label", `${button.querySelector(".goal-toggle-label")?.textContent ?? "Goal setting"}: ${lock ? (pressed ? "LOCKED" : "UNLOCKED") : (pressed ? "ON" : "OFF")}`);
  button.classList.toggle("is-on", pressed);
  const state = button.querySelector(".goal-toggle-state");
  if (state) state.textContent = lock ? (pressed ? "LOCKED" : "UNLOCKED") : (pressed ? "ON" : "OFF");
  const icon = button.querySelector("i");
  if (icon) icon.className = `fa-solid ${pressed ? "fa-lock" : "fa-lock-open"}`;
}

function numberField(id, labelText, value, min, max) {
  const label = document.createElement("label");
  label.className = "goal-number-field";
  label.textContent = labelText;
  const input = document.createElement("input");
  input.type = "number";
  input.id = id;
  input.min = String(min);
  if (max !== undefined) input.max = String(max);
  input.step = "1";
  input.value = value === "" ? "" : String(value);
  label.append(input);
  return label;
}

function updateGoalDraftFromInput(input) {
  if (!state.goalDraft || !input?.id) return;
  const number = (fallback = null) => {
    if (input.value === "") return fallback;
    const value = Number(input.value);
    return Number.isSafeInteger(value) && value > 0 ? value : fallback;
  };
  if (input.classList?.contains("goal-toggle")) {
    const pressed = input.getAttribute("aria-pressed") === "true";
    if (input.id === "goal-enabled") state.goalDraft.enabled = pressed;
    if (input.id === "goal-blocking") state.goalDraft.blocking = pressed;
    if (input.id === "goal-lock") state.goalDraft.goal_lock_enabled = pressed;
    if (input.id === "snooze-enabled") state.goalDraft.snooze_enabled = pressed;
  }
  if (input.id === "goal-text") state.goalDraft.goal = input.value;
  if (input.id === "snooze-duration") state.goalDraft.snooze_duration_ms = (number(60) || 60) * 1000;
  if (input.id === "snooze-turn-limit") state.goalDraft.snooze_turn_limit = number(null);
  if (input.id === "snooze-budget") state.goalDraft.snooze_budget_cap_ms = input.value === "" ? null : (number(null) || 1) * 60_000;
  if (input.id === "snooze-yield") state.goalDraft.snooze_refill_yield_ms = (number(30) || 30) * 1000;
  if (input.id === "snooze-elapsed") state.goalDraft.snooze_refill_elapsed_ms = (number(60) || 60) * 1000;
  if (input.id === "goal-enabled") {
    const text = elements.goalDialogBody.querySelector("#goal-text");
    if (text) text.disabled = input.getAttribute("aria-pressed") !== "true";
  }
}

function goalOutcomeCountsLabel(counts) {
  return `Outcomes: blocked ${counts.BLOCKED || 0}, completed ${counts.COMPLETED || 0}, not done ${counts.NOT_DONE || 0}, waiting ${counts.WAITING_FOR_USER_AUTHORIZATION || 0}`;
}

function setGoalDialogStatus(message, error = false) {
  const status = elements.goalDialogFooter.querySelector("#goal-editor-status");
  if (!status) return;
  status.textContent = message;
  status.classList.toggle("error", error);
}

function sendGoalControl() {
  const peer = state.goalDialogPeer;
  const draft = state.goalDraft;
  const action = draft?.enabled ? "set" : "off";
  const agent = state.agents.find((candidate) => candidate.agent_id === peer);
  const targetInstanceId = agent?.instance_id;
  if (!peer || !draft || !targetInstanceId) {
    setGoalDialogStatus("The agent is not ready for goal changes.", true);
    return;
  }
  if (action === "set" && draft.goal.trim() === "") {
    setGoalDialogStatus("Turn on the goal and enter a description before saving.", true);
    return;
  }
  const controlId = `goal-control-${crypto.randomUUID()}`;
  state.pendingGoalControls.set(controlId, { peer, instanceId: targetInstanceId });
  if (!sendCommand({
    type: "goal_control",
    controlId,
    targetAgentId: peer,
    targetInstanceId,
    action,
    goal: action === "off" ? "" : draft.goal,
    settings: {
      blocking: draft.blocking,
      goal_lock_enabled: draft.goal_lock_enabled,
      snooze_enabled: draft.snooze_enabled,
      snooze_duration_ms: draft.snooze_duration_ms,
      snooze_turn_limit: draft.snooze_turn_limit,
      snooze_budget_cap_ms: draft.snooze_budget_cap_ms,
      snooze_refill_yield_ms: draft.snooze_refill_yield_ms,
      snooze_refill_elapsed_ms: draft.snooze_refill_elapsed_ms,
    },
  })) {
    state.pendingGoalControls.delete(controlId);
    setGoalDialogStatus("The router is not connected.", true);
    return;
  }
  setGoalDialogStatus("Saving…");
}

function attachReminderActions(root, reminders) {
  const indicator = root.querySelector(".reminder-indicator");
  if (!indicator) return;
  const openReminder = (event) => {
    event.stopPropagation();
    event.preventDefault();
    const peer = root.dataset.agentId;
    openReminderDialog(peer, reminders.length === 0 ? "create" : "list", indicator);
  };
  indicator.addEventListener("click", openReminder);
  indicator.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    openReminder(event);
  });
}

function openReminderDialog(peer, mode, returnFocus) {
  clearReminderDialogStatus();
  state.reminderDialogPeer = peer;
  state.reminderDialogMode = mode;
  renderReminderDialog(peer);
  clearReminderDialogTimer();
  elements.remindersDialog.showModal();
  if (mode === "list") {
    state.reminderDialogTimer = setInterval(() => {
      if (!elements.remindersDialog.open || state.reminderDialogPeer !== peer || state.reminderDialogMode !== "list") {
        clearReminderDialogTimer();
        return;
      }
      renderReminderDialog(peer);
    }, 1000);
  }
  requestAnimationFrame(() => elements.remindersDialogBody.querySelector("textarea, input, button")?.focus());
}

function clearReminderDialogTimer() {
  if (state.reminderDialogTimer === null) return;
  clearInterval(state.reminderDialogTimer);
  state.reminderDialogTimer = null;
}

function closeReminderDialog() {
  clearReminderDialogTimer();
  state.reminderDialogPeer = "";
  state.reminderDialogMode = "list";
  clearReminderDialogStatus();
  if (elements.remindersDialog?.open) elements.remindersDialog.close();
}

function reminderRowsForPeer(peer) {
  return state.reminders.filter((reminder) => reminder.target === peer && ["scheduled", "due", "message_queued"].includes(reminder.state));
}

function renderReminderDialog(peer) {
  const reminders = state.reminders.filter((reminder) => reminder.target === peer && ["scheduled", "due", "message_queued"].includes(reminder.state));
  elements.remindersDialogBody.replaceChildren();
  if (state.reminderDialogMode === "create") {
    renderReminderEditor(peer);
    renderReminderDialogStatus();
    return;
  }
  const add = document.createElement("button");
  add.type = "button";
  add.textContent = "Add reminder";
  add.addEventListener("click", () => openReminderDialog(peer, "create", add));
  elements.remindersDialogBody.append(add);
  if (reminders.length === 0) {
    const empty = document.createElement("p");
    empty.className = "empty-state";
    empty.textContent = "No active reminders.";
    elements.remindersDialogBody.append(empty);
    renderReminderDialogStatus();
    return;
  }
  for (const reminder of reminders) elements.remindersDialogBody.append(renderReminderItem(peer, reminder));
  renderReminderDialogStatus();
}

function renderReminderItem(peer, reminder) {
  const item = document.createElement("article");
  item.className = "reminder-list-item";
  const key = reminderLockKey(peer, reminder.id);
  const lock = state.reminderLockStates.get(key);
  item.innerHTML = `<strong>${escapeHtml(reminder.subject || "Reminder")}</strong><p>${escapeHtml(reminder.body)}</p><small class="reminder-countdown">${escapeHtml(formatReminderCountdown(reminder.next_due_at))}</small><small class="reminder-recurrence">${escapeHtml(reminderRecurrenceLabel(reminder.interval_ms))}</small>`;
  const status = document.createElement("small");
  status.className = `reminder-lock-status${lock?.error ? " error" : ""}`;
  status.textContent = lock?.error || lock?.message || (lock?.locked ? "Native edits blocked." : "");
  const actions = document.createElement("div");
  actions.className = "reminder-lock-actions";
  const edit = document.createElement("button");
  edit.type = "button";
  edit.className = "secondary";
  edit.textContent = "Edit";
  edit.addEventListener("click", () => { state.reminderDialogMode = "edit"; state.reminderEditorReminder = reminder; renderReminderDialog(peer); });
  const pendingCrud = [...state.pendingReminderCrud.values()].some((request) => request.peer === peer && request.reminderId === reminder.id);
  edit.disabled = pendingCrud;
  const remove = document.createElement("button");
  remove.type = "button";
  remove.className = "secondary";
  remove.textContent = "Delete";
  remove.disabled = pendingCrud;
  remove.addEventListener("click", () => sendReminderCrud(peer, "delete", reminder.id));
  actions.append(edit, remove);
  const pending = [...state.pendingReminderLockControls.values()].some((control) => reminderLockKey(control.peer, control.reminderId) === key);
  for (const action of ["set", "clear"]) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = action === "clear" ? "secondary" : "";
    button.textContent = action === "set" ? "Set lock" : "Clear lock";
    button.disabled = pending || (action === "clear" && lock?.locked !== true);
    button.addEventListener("click", () => sendReminderLockControl(peer, reminder, action));
    actions.append(button);
  }
  item.append(status, actions);
  return item;
}

function renderReminderEditor(peer) {
  const reminder = state.reminderDialogMode === "edit" ? state.reminderEditorReminder : undefined;
  const form = document.createElement("form");
  form.className = "reminder-editor-form";
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const body = form.elements.namedItem("body").value.trim();
    const subject = form.elements.namedItem("subject").value.trim();
    if (!body) { setReminderDialogStatus("Enter reminder text.", true); return; }
    if (reminder) sendReminderCrud(peer, "update", reminder.id, { body, subject: subject || null });
    else {
      const now = new Date();
      const delayMinutes = Number(form.elements.namedItem("delayMinutes").value);
      const recurring = form.elements.namedItem("recurring").checked;
      if (!Number.isSafeInteger(delayMinutes) || delayMinutes < 1 || delayMinutes > 10080) { setReminderDialogStatus("Enter a delay from 1 to 10080 minutes.", true); return; }
      sendReminderCrud(peer, "create", undefined, { id: `reminder-${crypto.randomUUID()}`, target: peer, creator: state.agentId, body, ...(subject ? { subject } : {}), created_at: now.toISOString(), next_due_at: new Date(now.getTime() + delayMinutes * 60_000).toISOString(), interval_ms: recurring ? delayMinutes * 60_000 : null });
    }
  });
  form.append(reminderField("subject", "Subject", reminder?.subject || "", "Optional"));
  form.append(reminderField("body", "Reminder", reminder?.body || "", "What should be sent?", "textarea"));
  if (!reminder) {
    form.append(reminderField("delayMinutes", "Send after (minutes)", "1", "1"));
    const label = document.createElement("label");
    const recurring = document.createElement("input");
    recurring.type = "checkbox"; recurring.name = "recurring";
    label.append(recurring, document.createTextNode(" Repeat every delay interval"));
    form.append(label);
  }
  const actions = document.createElement("div"); actions.className = "reminder-lock-actions";
  const save = document.createElement("button"); save.type = "submit"; save.textContent = reminder ? "Save changes" : "Create reminder";
  save.disabled = [...state.pendingReminderCrud.values()].some((request) => request.peer === peer && (reminder ? request.reminderId === reminder.id : request.operation === "create"));
  const cancel = document.createElement("button"); cancel.type = "button"; cancel.className = "secondary"; cancel.textContent = "Cancel";
  cancel.addEventListener("click", () => { state.reminderDialogMode = "list"; renderReminderDialog(peer); });
  actions.append(cancel, save); form.append(actions); elements.remindersDialogBody.append(form);
}

function reminderField(name, labelText, value, placeholder, type = "input") {
  const label = document.createElement("label"); label.textContent = labelText;
  const field = document.createElement(type === "textarea" ? "textarea" : "input");
  field.name = name; field.value = value; field.placeholder = placeholder;
  if (type === "textarea") field.rows = 5;
  label.append(field); return label;
}

function clearReminderDialogStatus() {
  state.reminderDialogStatus = null;
}

function renderReminderDialogStatus() {
  const { message, error } = state.reminderDialogStatus || {};
  if (!message) return;
  const status = document.createElement("p");
  status.id = "reminder-dialog-status";
  status.className = `reminder-editor-status${error ? " error" : ""}`;
  status.textContent = message;
  elements.remindersDialogBody.append(status);
}

function setReminderDialogStatus(message, error = false) {
  state.reminderDialogStatus = { message, error };
  const status = elements.remindersDialogBody.querySelector("#reminder-dialog-status");
  if (status) {
    status.textContent = message;
    status.classList.toggle("error", error);
  }
}

function sendReminderCrud(peer, operation, reminderId, payload) {
  const requestId = `reminder-crud-${crypto.randomUUID()}`;
  clearReminderDialogStatus();
  state.pendingReminderCrud.set(requestId, { peer, operation, reminderId });
  if (!sendCommand({ type: "reminder_crud", requestId, operation, ...(reminderId === undefined ? {} : { reminderId }), ...(payload === undefined ? {} : { payload }) })) {
    state.pendingReminderCrud.delete(requestId);
    setReminderDialogStatus("The router is not connected.", true);
    renderReminderDialog(peer);
    return;
  }
  setReminderDialogStatus("Saving…");
  renderReminderDialog(peer);
}

function sendReminderLockControl(peer, reminder, action) {
  const targetInstanceId = state.agents.find((agent) => agent.agent_id === peer)?.instance_id;
  if (!targetInstanceId) {
    state.reminderLockStates.set(reminderLockKey(peer, reminder.id), { error: "That agent is not ready for lock changes." });
    renderReminderDialog(peer);
    return;
  }
  const controlId = `reminder-lock-${crypto.randomUUID()}`;
  state.pendingReminderLockControls.set(controlId, { peer, instanceId: targetInstanceId, reminderId: reminder.id, action });
  if (!sendCommand({ type: "reminder_lock_control", controlId, targetAgentId: peer, targetInstanceId, reminderId: reminder.id, action })) {
    state.pendingReminderLockControls.delete(controlId);
    state.reminderLockStates.set(reminderLockKey(peer, reminder.id), { error: "The router is not connected." });
  }
  renderReminderDialog(peer);
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
