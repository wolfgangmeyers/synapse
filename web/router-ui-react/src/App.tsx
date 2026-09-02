import { useCallback, useEffect, useMemo, useRef, useState, type ComponentProps, type FormEvent, type ReactElement } from "react";
import "./app.css";
import { createInitialState, type AppState } from "./state/app-state";
import { selectAgent, selectArrival, selectMessages, selectModels } from "./state/selectors";
import { activeRemindersForPeer, lockAfterCreateStatus, lockResultState, reminderLockErrorMessage, reminderLockKey, type Reminder, type ReminderCreateInput, type ReminderLockAction } from "./domain/reminders";
import { normalizeModelSummary } from "./domain/models";
import { mergeMessages, pendingMessage, reconcileHistoryMessages, settlePendingMessages, type HistoryMessage } from "./domain/history";
import { normalizeRouterUrl, persistedActiveRouter, createSettingsRepository, setActiveRouter, setAgentId, setRouterAlias, LEGACY_AGENT_ID_KEY, setSelectedPeer as persistSelectedPeer, selectedPeerFor, removeRouter as removeSavedRouter, type SettingsState, type StorageAdapter } from "./persistence/settings-repository";
import { type RouterTransport, type RouterTransportOptions } from "./transport/router-transport";
import { isRecord } from "./protocol/guards";
import { useRouterSession } from "./hooks/use-router-session";
import { useAgentReconciliation } from "./hooks/use-agent-reconciliation";
import { useConversation } from "./hooks/use-conversation";
import { useDrafts } from "./hooks/use-drafts";
import { HistoryRepository, initializePersistence } from "./persistence/history-repository";
import { AppShell } from "./components/AppShell";
import { Sidebar } from "./components/Sidebar";
import { Conversation } from "./components/Conversation";
import { RouterManagerDialog } from "./components/RouterManagerDialog";
import { GoalEditorDialog } from "./components/GoalEditorDialog";
import { InlineToolSettingsDialog } from "./components/InlineToolSettingsDialog";
import { ReminderDialog } from "./components/ReminderDialog";
import { PromptControls } from "./components/PromptControls";
import { PromptTemplateDialog } from "./components/PromptTemplateDialog";
import { ArrivalDialog } from "./components/ArrivalDialog";
import { ModelPicker } from "./components/ModelPicker";
import type { ImageInput } from "./domain/images";
import type { SavedRouter } from "./domain/router-manager";
import { promptTemplateUpdatePayload, type PromptTemplate } from "./domain/prompt-templates";
import { runPromptTemplateImport, type PromptTemplateImportProgress, type PromptTemplateImportResult } from "./domain/prompt-template-import";
import { goalErrorMessage, type GoalControlSettings } from "./domain/goals";
import { withAbsoluteAttachmentUrls } from "./transport/attachment-urls";
import { createHistoryThumbnail } from "./domain/images";
import { cacheKeyForAttachment, saveCachedImage } from "./persistence/history-repository";

export interface AppProps {
  routerUrl?: string | undefined;
  agentId?: string | undefined;
  connect?: boolean | undefined;
  state?: AppState | undefined;
  storage?: StorageAdapter | undefined;
  transportOptions?: Omit<RouterTransportOptions, "routerUrl" | "agentId" | "defaultAgentId"> | undefined;
  onRefresh?: (() => void) | undefined;
  onSelectAgent?: ((agentId: string) => void) | undefined;
  onFilterChange?: ((filter: string) => void) | undefined;
  onRouterClick?: (() => void) | undefined;
  onSend?: ((peer: string, body: string, images: ImageInput[]) => void | Promise<void>) | undefined;
  onGoal?: ((peer: string) => void) | undefined;
  onReminders?: ((peer: string) => void) | undefined;
  onInlineToolSettings?: ((peer: string) => void) | undefined;
  onOpenArrival?: ((agentId: string) => void) | undefined;
  onDismissArrival?: ((agentId: string) => void) | undefined;
  onToggleSubagents?: ((agentId: string) => void) | undefined;
}

type Dialog = "routers" | "goal" | "reminders" | "inline-tools" | "prompts" | "arrival" | undefined;

function configuredRouterUrl(props: AppProps): string | undefined {
  const explicit = props.routerUrl ?? props.state?.routerUrl ?? import.meta.env.VITE_ROUTER_URL;
  if (typeof explicit === "string" && explicit.trim() !== "") return explicit;
  return persistedActiveRouter(props.storage);
}

function responseMessage(value: unknown, routerUrl: string): HistoryMessage | undefined {
  if (!isRecord(value) || typeof value.id !== "string" || value.id === "") return undefined;
  const message: HistoryMessage = withAbsoluteAttachmentUrls({ ...value, id: value.id }, routerUrl);
  message.direction = "outgoing";
  return message;
}

async function cacheAcceptedImages(peer: string, scope: { routerUrl: string; agentId: string }, accepted: HistoryMessage, pending: HistoryMessage | undefined): Promise<HistoryMessage> {
  const pendingAttachments = pending?.attachments ?? [];
  if (!Array.isArray(accepted.attachments)) return accepted;
  const attachments = await Promise.all(accepted.attachments.map(async (attachment, index) => {
    const source = pendingAttachments[index]?.url;
    if (typeof source !== "string" || !source.startsWith("data:image/")) return attachment;
    const cacheKey = cacheKeyForAttachment(scope.routerUrl, scope.agentId, peer, accepted.id, attachment.id ?? index);
    try {
      const blob = await createHistoryThumbnail(source);
      await saveCachedImage({ key: cacheKey, blob, mime_type: "image/jpeg", name: attachment.name, createdAtMs: Date.now(), byteSize: blob.size });
      return { ...attachment, cacheKey };
    } catch {
      return attachment;
    }
  }));
  return { ...accepted, attachments };
}

function ConfigurationScreen({ initialValue = "", initialError = "Router URL is required", onConfigure }: { initialValue?: string; initialError?: string; onConfigure: (value: string) => void }): ReactElement {
  const [value, setValue] = useState(initialValue);
  const [error, setError] = useState(initialError);
  const submit = (event: FormEvent<HTMLFormElement>): void => {
    event.preventDefault();
    try { onConfigure(normalizeRouterUrl(value)); setError(""); } catch (cause) { setError(cause instanceof Error ? cause.message : "Enter a valid router URL."); }
  };
  return <main className="configuration-screen"><section className="empty-state" aria-labelledby="configuration-title"><h1 id="configuration-title">Connect Mecha</h1><p>Enter the isolated Go router URL to continue.</p><form onSubmit={submit}><label>Router URL<input autoFocus value={value} onChange={(event) => setValue(event.currentTarget.value)} placeholder="http://127.0.0.1:4000" /></label>{error ? <p className="error" role="alert">{error}</p> : null}<button type="submit">Connect</button></form></section></main>;
}

export function App(props: AppProps): ReactElement {
  const [configured, setConfigured] = useState<string | undefined>(() => configuredRouterUrl(props));
  const candidate = configured ?? configuredRouterUrl(props);
  if (candidate === undefined) return <ConfigurationScreen onConfigure={setConfigured} />;
  try {
    const normalized = normalizeRouterUrl(candidate);
    return <ConfiguredApp {...props} routerUrl={normalized} />;
  } catch {
    return <ConfigurationScreen initialValue={candidate} initialError="Router URL is invalid" onConfigure={setConfigured} />;
  }
}

function PersistenceStartupScreen({ error }: { error?: string }): ReactElement {
  return <main className="configuration-screen"><section className="empty-state" aria-labelledby="persistence-title"><h1 id="persistence-title">Loading Mecha</h1>{error ? <p className="error" role="alert">{error}</p> : <p>Loading local history…</p>}</section></main>;
}

function ConfiguredApp(props: AppProps & { routerUrl: string }): ReactElement {
  const [initializationError, setInitializationError] = useState<string>();
  const [ready, setReady] = useState(false);
  useEffect(() => {
    let active = true;
    void initializePersistence(undefined, props.storage).then(() => {
      if (active) setReady(true);
    }).catch((cause: unknown) => {
      if (active) setInitializationError(cause instanceof Error ? cause.message : "Local persistence could not be initialized.");
    });
    return () => { active = false; };
  }, [props.storage]);
  if (initializationError !== undefined) return <PersistenceStartupScreen error={initializationError} />;
  if (!ready) return <PersistenceStartupScreen />;
  return <ConfiguredAppContent {...props} />;
}

function ConfiguredAppContent({ routerUrl, agentId: requestedAgentId, connect = true, state: suppliedState, storage, transportOptions, onRefresh, onSelectAgent, onFilterChange, onRouterClick, onSend: externalSend, onGoal: externalGoal, onReminders: externalReminders, onInlineToolSettings: externalInlineToolSettings, onOpenArrival, onDismissArrival, onToggleSubagents }: AppProps & { routerUrl: string }): ReactElement {
  const settingsRepository = useMemo(() => createSettingsRepository(storage, routerUrl), [storage, routerUrl]);
  const [settings, setSettings] = useState<SettingsState>(() => {
    const loaded = setActiveRouter(settingsRepository.load(), routerUrl);
    const configured = settingsRepository.agentId(loaded, loaded.activeRouter, "wolfgang");
    const persisted = loaded.agentIds[loaded.activeRouter] !== undefined
      || (loaded.routers[0] === loaded.activeRouter && storage?.getItem(LEGACY_AGENT_ID_KEY)?.trim() !== "");
    return requestedAgentId !== undefined && !persisted
      ? setAgentId(loaded, loaded.activeRouter, requestedAgentId)
      : loaded.agentIds[loaded.activeRouter] === undefined ? setAgentId(loaded, loaded.activeRouter, configured) : loaded;
  });
  const activeUrl = settings.activeRouter;
  const resolvedAgentId = settingsRepository.agentId(settings, activeUrl, requestedAgentId ?? "wolfgang");
  const initialState = useMemo(() => suppliedState ?? createInitialState({ routerUrl: activeUrl, agentId: resolvedAgentId }), [activeUrl, resolvedAgentId, suppliedState]);
  const scope = useMemo(() => ({ routerUrl: activeUrl, agentId: resolvedAgentId }), [activeUrl, resolvedAgentId]);
  const historyRepository = useMemo(() => new HistoryRepository(undefined, storage), [storage]);
  const persistDelivery = useCallback(async (messages: readonly HistoryMessage[]): Promise<void> => {
    const grouped = new Map<string, HistoryMessage[]>();
    for (const message of messages) {
      const peer = message.from === resolvedAgentId ? message.to : message.from;
      if (typeof peer !== "string" || peer === "" || peer === resolvedAgentId) continue;
      grouped.set(peer, [...(grouped.get(peer) ?? []), message]);
    }
    await Promise.all([...grouped].map(([peer, entries]) => historyRepository.savePeer(peer, scope, entries)));
  }, [historyRepository, resolvedAgentId, scope]);
  const session = useRouterSession({ routerUrl: activeUrl, agentId: resolvedAgentId, defaultAgentId: resolvedAgentId, initialState, connect, ...transportOptions, persistDelivery });
  const sessionStateRef = useRef(session.state);
  sessionStateRef.current = session.state;
  const reconciliation = useAgentReconciliation(session.transport, scope);
  const selectedFromState = session.state.selectedPeer;
  const [selectedPeer, setSelectedPeer] = useState<string | undefined>(() => selectedFromState ?? (selectedPeerFor(settings, activeUrl, resolvedAgentId) || undefined));
  const previousScope = useRef(scope);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [dialog, setDialog] = useState<Dialog>();
  const [arrivalAgent, setArrivalAgent] = useState<string>();
  const [goalError, setGoalError] = useState<string>();
  const [reminderStatus, setReminderStatus] = useState<string>();
  const [goalControlAvailable, setGoalControlAvailable] = useState(false);
  const [inlineToolControlAvailable, setInlineToolControlAvailable] = useState(false);
  const [inlineToolError, setInlineToolError] = useState<string>();
  const [templatesLoaded, setTemplatesLoaded] = useState(false);
  const [promptImportProgress, setPromptImportProgress] = useState<PromptTemplateImportProgress>();
  const [templateToUse, setTemplateToUse] = useState<PromptTemplate>();
  const drafts = useDrafts(activeUrl, resolvedAgentId, selectedPeer, undefined);
  const composerDraft = drafts.draft;
  const setComposerDraft = drafts.setDraft;
  const [composerSelection, setComposerSelection] = useState<{ start: number; end: number }>();
  const pendingReminderControls = useRef(new Map<string, { action: ReminderLockAction; afterCreate: boolean }>());
  const templateListGeneration = useRef(0);
  const templateListDirty = useRef(false);
  const latestTemplateRequest = useRef(0);
  const persistConversation = useCallback((peer: string, messages: HistoryMessage[]): void => { void historyRepository.savePeer(peer, scope, messages).catch(() => undefined); }, [historyRepository, scope]);
  const replacePersistedConversation = useCallback((peer: string, messages: HistoryMessage[]): void => { void historyRepository.replacePeer(peer, scope, messages).catch(() => undefined); }, [historyRepository, scope]);
  const reconcileConversationState = useCallback((peer: string, messages: HistoryMessage[], highWaterSequence: number): void => {
    session.dispatch({ type: "history/reconcile", scope, peer, snapshot: messages, highWaterSequence });
    const existing = sessionStateRef.current.messagesByPeer[peer] ?? [];
    replacePersistedConversation(peer, reconcileHistoryMessages(existing, messages, highWaterSequence));
  }, [replacePersistedConversation, scope, session.dispatch]);
  const conversation = useConversation(session.transport, scope, selectedPeer, persistConversation, reconcileConversationState);
  const refreshTemplates = useCallback(async (): Promise<void> => {
    const requestId = ++latestTemplateRequest.current;
    const generation = templateListGeneration.current;
    templateListDirty.current = false;
    try {
      const result = await session.transport.http.promptTemplates();
      if (requestId !== latestTemplateRequest.current || generation !== templateListGeneration.current || templateListDirty.current) return;
      session.dispatch({ type: "templates/set", scope, templates: result.prompt_templates });
      setTemplatesLoaded(true);
    } catch (cause) {
      if (requestId === latestTemplateRequest.current && generation === templateListGeneration.current && !templateListDirty.current) setTemplatesLoaded(true);
      throw cause;
    }
  }, [scope, session.dispatch, session.transport]);
  const invalidateTemplateList = (): void => {
    templateListGeneration.current += 1;
    templateListDirty.current = true;
  };

  useEffect(() => { settingsRepository.save(settings); }, [settings, settingsRepository]);
  useEffect(() => {
    const prior = previousScope.current;
    const persistedPeer = selectedPeerFor(settings, activeUrl, resolvedAgentId) || undefined;
    setSelectedPeer((current) => prior.routerUrl === activeUrl ? current ?? persistedPeer : persistedPeer);
    previousScope.current = scope;
    setExpanded(new Set()); setDialog(undefined); setArrivalAgent(undefined); setGoalError(undefined); setReminderStatus(undefined);
    setGoalControlAvailable(false); setInlineToolControlAvailable(false); setInlineToolError(undefined); setTemplatesLoaded(false); setPromptImportProgress(undefined); setTemplateToUse(undefined); setComposerSelection(undefined);
    templateListGeneration.current += 1;
    templateListDirty.current = true;
    latestTemplateRequest.current += 1;
    pendingReminderControls.current.clear();
  }, [activeUrl, resolvedAgentId]);
  useEffect(() => {
    if (session.state.connection === "connected") return;
    pendingReminderControls.current.clear(); setGoalControlAvailable(false); setInlineToolControlAvailable(false); setInlineToolError(undefined); setGoalError(undefined); setReminderStatus(undefined);
  }, [session.state.connection, session.state.connectionGeneration, session.state.instanceId]);
  useEffect(() => { if (reconciliation.ready) session.dispatch({ type: "agents/set", scope, agents: reconciliation.agents }); }, [reconciliation.agents, reconciliation.ready, scope, session.dispatch]);
  useEffect(() => {
    if (selectedPeer === undefined && session.state.selectedPeer !== undefined) setSelectedPeer(session.state.selectedPeer);
    if (selectedPeer !== undefined) { session.dispatch({ type: "selection/set", scope, peer: selectedPeer }); setSettings((current) => persistSelectedPeer(current, activeUrl, resolvedAgentId, selectedPeer)); }
  }, [activeUrl, resolvedAgentId, selectedPeer, scope, session.dispatch, session.state.selectedPeer]);
  useEffect(() => {
    let active = true;
    void historyRepository.load(scope).then((messages) => { if (!active) return; for (const [peer, records] of Object.entries(messages)) { const snapshot = records.filter((message): message is import("./domain/history").HistoryMessage => typeof message.id === "string"); session.dispatch({ type: "history/reconcile", scope, peer, snapshot, highWaterSequence: 0 }); } }).catch(() => undefined);
    return () => { active = false; };
  }, [historyRepository, scope, session.dispatch]);
  useEffect(() => {
    if (session.state.connection !== "connected") return;
    void session.transport.refreshReminders().catch((cause: unknown) => setReminderStatus(cause instanceof Error ? cause.message : "Reminders could not be loaded."));
    void refreshTemplates().catch(() => undefined);
  }, [scope, session.state.connection, session.transport, session.dispatch]);
  useEffect(() => {
    const unsubscribe = session.transport.subscribe((event) => {
      if (event.type === "status") { if (event.status !== "connected") { setGoalControlAvailable(false); setInlineToolControlAvailable(false); setInlineToolError(undefined); } return; }
      if (event.type === "reminders") { session.dispatch({ type: "reminders/set", scope, reminders: event.reminders }); return; }
      if (event.type === "capabilities") { setGoalControlAvailable(event.goalControlAvailable); setInlineToolControlAvailable(event.inlineToolControlAvailable); return; }
      if (event.type === "control-error") {
        if (event.pending.kind === "inline-tool") setInlineToolError(`Inline tool setting could not be saved: ${event.frame.code}`);
        if (event.pending.kind === "goal") {
          setGoalError(goalErrorMessage(event.frame.code));
        }
        if (event.pending.kind === "reminder-lock") {
          const pending = pendingReminderControls.current.get(event.pending.controlId);
          pendingReminderControls.current.delete(event.pending.controlId);
          if (event.pending.reminderId !== undefined) session.dispatch({ type: "reminder-lock/set", scope, key: reminderLockKey(event.pending.targetAgentId, event.pending.reminderId), locked: false });
          const message = reminderLockErrorMessage(event.frame.code);
          setReminderStatus(pending?.afterCreate ? `Reminder created, but lock setup failed: ${message}` : message);
        }
      }
      if (event.type === "control" && event.kind === "inline-tool") {
        const frame = event.frame;
        if (frame.type !== "inline_tool_control_result") return;
        const target = session.state.agents.find((agent) => agent.agent_id === event.pending.targetAgentId);
        if (target?.instance_id === event.pending.targetInstanceId) session.dispatch({ type: "agents/set", scope, agents: session.state.agents.map((agent) => agent.agent_id === target.agent_id ? { ...agent, inline_tool_enabled: frame.enabled } : agent) });
        if (frame.accepted) { setInlineToolError(undefined); setDialog(undefined); }
        else setInlineToolError(`Inline tool setting could not be saved: ${frame.error_code}`);
      }
      if (event.type === "control" && event.kind === "goal") {
        const frame = event.frame;
        if (frame.type !== "goal_control_result") return;
        if (frame.accepted) {
          const target = session.state.agents.find((agent) => agent.agent_id === event.pending.targetAgentId);
          if (target?.instance_id === event.pending.targetInstanceId) session.dispatch({ type: "agents/set", scope, agents: session.state.agents.map((agent) => agent.agent_id === target.agent_id ? { ...agent, goal_state: frame.goal_state } : agent) });
          setGoalError(undefined);
          setDialog(undefined);
        } else setGoalError(goalErrorMessage(frame.error_code));
      }
      if (event.type === "controls-invalidated") {
        const goalPending = [...session.state.pendingControls.values()].some((pending) => pending.action === "goal");
        const reminderAfterCreate = [...pendingReminderControls.current.values()].some((pending) => pending.afterCreate);
        const reminderPending = pendingReminderControls.current.size > 0;
        pendingReminderControls.current.clear();
        const reason = event.reason ?? "router_reconnecting";
        if (goalPending) setGoalError(goalErrorMessage(reason));
        const inlinePending = [...session.state.pendingControls.values()].some((pending) => pending.action === "inline-tool");
        if (inlinePending) setInlineToolError("Inline tool setting could not be saved while the worker is disconnected.");
        if (reminderAfterCreate) setReminderStatus(`Reminder created, but lock setup failed: ${reminderLockErrorMessage(reason)}`);
        else if (reminderPending) setReminderStatus(reminderLockErrorMessage(reason));
      }
      if (event.type === "control" && event.kind === "reminder-lock") {
        const pending = pendingReminderControls.current.get(event.pending.controlId);
        pendingReminderControls.current.delete(event.pending.controlId);
        if (event.pending.reminderId !== undefined) {
          const result = lockResultState(pending?.action ?? "set", event.frame.accepted, event.frame.accepted ? undefined : event.frame.error_code);
          session.dispatch({ type: "reminder-lock/set", scope, key: reminderLockKey(event.pending.targetAgentId, event.pending.reminderId), locked: result.locked === true });
          setReminderStatus(pending?.afterCreate ? lockAfterCreateStatus(event.frame.accepted, result.error) : (result.message ?? result.error ?? "Lock change could not be confirmed."));
        }
      }
    });
    return unsubscribe;
  }, [scope, session.state.agents, session.state.pendingControls, session.transport]);

  const selectedAgent = useMemo(() => selectAgent(session.state, selectedPeer), [session.state, selectedPeer]);
  const arrival = selectedPeer ? selectArrival(session.state, selectedPeer) : undefined;
  const setSelection = (peer: string): void => { setSelectedPeer(peer); onSelectAgent?.(peer); };
  const openGoalForAgent = (peer: string): void => { setSelection(peer); externalGoal?.(peer); setDialog("goal"); };
  const openRemindersForAgent = (peer: string): void => { setSelection(peer); externalReminders?.(peer); setDialog("reminders"); };
  const openInlineToolSettingsForAgent = (peer: string): void => { setSelection(peer); externalInlineToolSettings?.(peer); setInlineToolError(undefined); setDialog("inline-tools"); };
  const send = async (peer: string, body: string, images: ImageInput[]): Promise<void> => {
    // Use one client message ID for the optimistic row and the HTTP envelope.
    // The router preserves it, so reconnect history can settle this row safely.
    const localId = crypto.randomUUID();
    const payload = { id: localId, from: resolvedAgentId, to: peer, body, created_at: new Date().toISOString(), source: { kind: "web_ui" }, ...(images.length > 0 ? { attachments: images.map((image) => ({ type: "image", data_url: image.dataUrl, ...(image.name === undefined ? {} : { name: image.name }) })) } : {}) };
    const pending = pendingMessage(body, peer, resolvedAgentId, images.map((image) => ({ url: image.dataUrl, mime_type: image.mimeType, size_bytes: image.sizeBytes, ...(image.name === undefined ? {} : { name: image.name }) })), localId);
    session.dispatch({ type: "message/append", scope, peer, message: pending });
    persistConversation(peer, [pending]);
    try {
      if (externalSend) await externalSend(peer, body, images);
      const result = await conversation.send(payload);
      const accepted = responseMessage(result.conversation_message ?? result.message, activeUrl);
      if (accepted === undefined) throw new Error("Router returned an invalid message response");
      const cachedAccepted = await cacheAcceptedImages(peer, scope, accepted, pending);
      session.dispatch({ type: "send/replace", scope, peer, localId, message: cachedAccepted });
      persistConversation(peer, [cachedAccepted]);
    } catch (cause) {
      session.dispatch({ type: "send/settle", scope, peer, localId, ok: false });
      // Keep the visible failure state after a reload; the optimistic pending row
      // must not remain persisted once the send has settled unsuccessfully.
      persistConversation(peer, settlePendingMessages([pending], localId, false));
      throw cause;
    }
  };
  const chooseModel = (model: Parameters<NonNullable<ComponentProps<typeof ModelPicker>["onChange"]>>[0]): void => {
    if (selectedAgent?.instance_id === undefined) return;
    session.transport.sendModelControl(selectedAgent.agent_id, selectedAgent.instance_id, model);
  };
  const saveInlineToolSettings = (enabled: boolean): void => {
    if (!selectedAgent?.instance_id || !selectedPeer) return;
    if (!inlineToolControlAvailable || selectedAgent.inline_tool_control_available !== true) { setInlineToolError("Inline tool control is not available for this worker."); return; }
    setInlineToolError(undefined);
    if (!session.transport.sendInlineToolControl(selectedPeer, selectedAgent.instance_id, enabled)) setInlineToolError("Inline tool setting could not be saved while the worker is disconnected.");
  };
  const saveGoal = (payload: { action: "set" | "off"; goal: string; settings: GoalControlSettings }): void => {
    if (!selectedAgent?.instance_id || !selectedPeer) return;
    if (!goalControlAvailable || !session.transport.goalControlAvailable) { setGoalError("Goal controls are not available on this router."); return; }
    const controlId = crypto.randomUUID();
    setGoalError(undefined);
    if (!session.transport.sendGoalControl(selectedPeer, selectedAgent.instance_id, { controlId, action: payload.action, goal: payload.goal, settings: payload.settings })) {
      setGoalError(goalErrorMessage("router_reconnecting"));
    }
  };
  const createReminder = async (payload: ReminderCreateInput, lockAfterCreate: boolean): Promise<void> => {
    const result = await session.transport.http.createReminder(payload);
    session.dispatch({ type: "reminders/set", scope, reminders: [...sessionStateRef.current.reminders, result.reminder] });
    let refreshed = false;
    try {
      await refreshReminders(session.transport);
      refreshed = true;
    } catch {
      setReminderStatus("Reminder created, but the list could not be refreshed.");
    }
    if (lockAfterCreate && selectedAgent?.instance_id) {
      const reminderId = result.reminder.id;
      const controlId = crypto.randomUUID();
      pendingReminderControls.current.set(controlId, { action: "set", afterCreate: true });
      setReminderStatus("Reminder created. Locking…");
      const accepted = session.transport.sendReminderLockControl(payload.target, selectedAgent.instance_id, reminderId, "set", controlId);
      if (!accepted) {
        pendingReminderControls.current.delete(controlId);
        setReminderStatus(`Reminder created, but lock setup failed: ${reminderLockErrorMessage("router_reconnecting")}`);
      }
    } else if (refreshed) setReminderStatus("Reminder created.");
  };
  const updateReminder = async (id: string, payload: { subject: string | null; body: string }): Promise<void> => {
    try {
      const result = await session.transport.http.updateReminder(id, payload);
      session.dispatch({ type: "reminders/set", scope, reminders: sessionStateRef.current.reminders.map((reminder) => reminder.id === id ? result.reminder : reminder) });
      try { await refreshReminders(session.transport); } catch { setReminderStatus("Reminder updated, but the list could not be refreshed."); }
    } catch (cause) { setReminderStatus(cause instanceof Error ? cause.message : "Reminder could not be updated."); throw cause; }
  };
  const deleteReminder = async (id: string): Promise<void> => {
    try {
      await session.transport.http.deleteReminder(id);
      session.dispatch({ type: "reminders/set", scope, reminders: sessionStateRef.current.reminders.filter((reminder) => reminder.id !== id) });
      try { await refreshReminders(session.transport); } catch { setReminderStatus("Reminder deleted, but the list could not be refreshed."); }
    } catch (cause) { setReminderStatus(cause instanceof Error ? cause.message : "Reminder could not be deleted."); }
  };
  const lockReminder = (reminder: Reminder, action: ReminderLockAction): void => {
    if (!selectedAgent?.instance_id) return;
    const controlId = crypto.randomUUID();
    pendingReminderControls.current.set(controlId, { action, afterCreate: false });
    if (!session.transport.sendReminderLockControl(reminder.target, selectedAgent.instance_id, reminder.id, action, controlId)) {
      pendingReminderControls.current.delete(controlId);
      setReminderStatus(reminderLockErrorMessage("router_reconnecting"));
    }
  };
  const saveTemplate = async (template: PromptTemplate): Promise<void> => { if (template.id) await session.transport.http.updatePromptTemplate(template.id, promptTemplateUpdatePayload(template)); else await session.transport.http.createPromptTemplate(template); invalidateTemplateList(); await refreshTemplates(); };
  const deleteTemplate = async (id: string): Promise<void> => { await session.transport.http.deletePromptTemplate(id); invalidateTemplateList(); await refreshTemplates(); };
  const importTemplates = async (items: PromptTemplate[], overwrite: boolean): Promise<PromptTemplateImportResult> => {
    setPromptImportProgress({ completed: 0, total: items.length, interrupted: false });
    const result = await runPromptTemplateImport(items, overwrite, {
      create: async (template) => { await session.transport.http.createPromptTemplate(template); },
      update: async (id, template) => { await session.transport.http.updatePromptTemplate(id, promptTemplateUpdatePayload(template)); invalidateTemplateList(); },
      refresh: async () => { await refreshTemplates(); },
    }, setPromptImportProgress, { existingTemplates: sessionStateRef.current.templates });
    setPromptImportProgress(undefined);
    return result;
  };
  const insertTemplate = (value: string, selection?: { start: number; end: number }): void => {
    const start = Math.max(0, Math.min(selection?.start ?? composerDraft.length, composerDraft.length));
    const end = Math.max(start, Math.min(selection?.end ?? start, composerDraft.length));
    const next = `${composerDraft.slice(0, start)}${value}${composerDraft.slice(end)}`;
    setComposerDraft(next);
    setComposerSelection({ start: start + value.length, end: start + value.length });
  };
  const saveRouter = (router: SavedRouter, previousUrl?: string): void => { setSettings((current) => { let next = previousUrl === undefined || previousUrl !== router.url ? setActiveRouter(current, router.url) : current; next = setRouterAlias(next, router.url, router.alias ?? ""); next = setAgentId(next, router.url, router.agentId); settingsRepository.save(next); return next; }); };
  const selectRouter = (router: SavedRouter): void => { setSettings((current) => setActiveRouter(current, router.url)); setDialog(undefined); };
  const removeRouter = (url: string): void => { setSettings((current) => removeSavedRouter(current, url)); };
  const routerLabel = new URL(activeUrl).host;
  const modelOptions = selectModels(selectedAgent);
  const selectedModel = normalizeModelSummary(selectedAgent?.current_model);
  const pendingModelControl = [...session.state.pendingControls.values()].some((pending) => pending.action === "model" && pending.peer === selectedAgent?.agent_id && pending.instanceId === selectedAgent?.instance_id);
  const pendingGoalControl = [...session.state.pendingControls.values()].some((pending) => pending.action === "goal" && pending.peer === selectedAgent?.agent_id && pending.instanceId === selectedAgent?.instance_id);
  const pendingInlineToolControl = [...session.state.pendingControls.values()].some((pending) => pending.action === "inline-tool" && pending.peer === selectedAgent?.agent_id && pending.instanceId === selectedAgent?.instance_id);
  const selectedAgentActiveReminderCount = selectedPeer === undefined ? 0 : activeRemindersForPeer(session.state.reminders, selectedPeer).length;
  const pendingReminderIds = new Set([...session.state.pendingControls.values()].flatMap((pending) => pending.action === "reminder-lock" && pending.peer === selectedPeer && pending.reminderId !== undefined ? [pending.reminderId] : []));
  const arrivalForDialog = arrivalAgent ? session.state.arrivals.get(arrivalAgent) : undefined;
  const toggleSubagents = (id: string): void => {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  return <>
    <AppShell
      sidebar={<Sidebar routerLabel={routerLabel} connection={session.state.connection} connectionIdentity={resolvedAgentId} connectionDetail={reconciliation.error?.message} agents={session.state.agents} {...(selectedPeer === undefined ? {} : { selectedAgentId: selectedPeer })} filter={session.state.filter} arrivals={session.state.arrivals} reminderCounts={Object.fromEntries(session.state.agents.map((agent) => [agent.agent_id, activeRemindersForPeer(session.state.reminders, agent.agent_id).length]))} expandedAgentIds={expanded} onRefresh={() => { void reconciliation.refresh(); onRefresh?.(); }} onRouterClick={() => { setDialog("routers"); onRouterClick?.(); }} onFilterChange={(filter) => { session.dispatch({ type: "filter/set", scope, filter }); onFilterChange?.(filter); }} onSelectAgent={setSelection} onGoal={openGoalForAgent} onReminders={openRemindersForAgent} onToggleSubagents={(id) => { toggleSubagents(id); onToggleSubagents?.(id); }} onOpenArrival={(item) => { setArrivalAgent(item.agent); setDialog("arrival"); onOpenArrival?.(item.agent); }} onDismissArrival={(id) => { session.dispatch({ type: "arrival/remove", scope, agent: id }); onDismissArrival?.(id); }} />}
      conversation={<div className="conversation-runtime"><Conversation {...(selectedAgent ? { peer: selectedAgent } : {})} activeReminderCount={selectedAgentActiveReminderCount} ownerId={resolvedAgentId} routerUrl={activeUrl} messages={mergeMessages(selectMessages(session.state, selectedPeer), conversation.messages)} value={composerDraft} onChange={setComposerDraft} onSelectionChange={setComposerSelection} {...(modelOptions.length > 0 ? { modelControls: <ModelPicker models={modelOptions} {...(selectedModel ? { currentModel: selectedModel } : {})} pending={pendingModelControl} disabled={session.state.connection !== "connected" || pendingModelControl} onChange={chooseModel} /> } : {})} {...(selectedAgent ? { promptControls: <PromptControls templates={session.state.templates} loaded={templatesLoaded} disabled={session.state.connection !== "connected"} onManage={() => setDialog("prompts")} onUse={(template) => { setTemplateToUse(template); setDialog("prompts"); }} /> } : {})} loading={conversation.loading} {...(conversation.error ? { error: conversation.error.message } : {})} {...(arrival ? { arrival } : {})} onSend={(body, images) => { if (selectedPeer) void send(selectedPeer, body, images).catch(() => undefined); }} onGoal={() => { if (selectedPeer) { setDialog("goal"); externalGoal?.(selectedPeer); } }} onReminders={() => { if (selectedPeer) { setDialog("reminders"); externalReminders?.(selectedPeer); } }} onInlineToolSettings={() => { if (selectedPeer) openInlineToolSettingsForAgent(selectedPeer); }} onOpenArrival={(item) => { setArrivalAgent(item.agent); setDialog("arrival"); onOpenArrival?.(item.agent); }} onDismissArrival={(id) => session.dispatch({ type: "arrival/remove", scope, agent: id })} onToggleSubagents={() => { if (selectedPeer) { toggleSubagents(selectedPeer); onToggleSubagents?.(selectedPeer); } }} expanded={selectedPeer !== undefined && expanded.has(selectedPeer)} /></div>}
    />
    <RouterManagerDialog open={dialog === "routers"} routers={settings.routers.map((url) => ({ url, ...(settings.aliases[url] ? { alias: settings.aliases[url] } : {}), agentId: settings.agentIds[url] ?? (url === activeUrl ? resolvedAgentId : requestedAgentId ?? "wolfgang") }))} activeUrl={activeUrl} defaultAgentId={requestedAgentId ?? "wolfgang"} onClose={() => setDialog(undefined)} onSave={saveRouter} onSelect={selectRouter} onRemove={removeRouter} />
    {selectedPeer && selectedAgent ? <InlineToolSettingsDialog open={dialog === "inline-tools"} peer={selectedPeer} enabled={selectedAgent.inline_tool_enabled === true} capabilityAvailable={inlineToolControlAvailable && selectedAgent.inline_tool_control_available === true} pending={pendingInlineToolControl} {...(inlineToolError ? { error: inlineToolError } : {})} onClose={() => { setDialog(undefined); setInlineToolError(undefined); }} onSave={saveInlineToolSettings} /> : null}
    {selectedPeer && selectedAgent ? <GoalEditorDialog open={dialog === "goal"} peer={selectedPeer} {...(selectedAgent.goal_state ? { goalState: selectedAgent.goal_state } : {})} capabilityAvailable={goalControlAvailable && session.transport.goalControlAvailable} pending={pendingGoalControl} {...(goalError ? { error: goalError } : {})} onClose={() => { setDialog(undefined); }} onSave={saveGoal} /> : null}
    {selectedPeer ? <ReminderDialog open={dialog === "reminders"} peer={selectedPeer} creator={resolvedAgentId} reminders={session.state.reminders} locks={Object.fromEntries(session.state.reminderLocks)} pendingIds={pendingReminderIds} {...(reminderStatus ? { status: reminderStatus } : {})} onClose={() => setDialog(undefined)} onCreate={(payload, lock) => createReminder(payload, lock)} onUpdate={(id, payload) => updateReminder(id, payload)} onDelete={(id) => deleteReminder(id)} onLock={lockReminder} /> : null}
    <PromptTemplateDialog open={dialog === "prompts"} templates={session.state.templates} loaded={templatesLoaded} {...(promptImportProgress === undefined ? {} : { importProgress: promptImportProgress })} {...(templateToUse ? { templateToUse } : {})} {...(composerSelection ? { selection: composerSelection } : {})} onClose={() => { setDialog(undefined); setPromptImportProgress(undefined); setTemplateToUse(undefined); }} onSave={(template) => saveTemplate(template)} onDelete={(id) => deleteTemplate(id)} onImport={(items, overwrite) => importTemplates(items, overwrite)} onInsert={insertTemplate} />
    <ArrivalDialog open={dialog === "arrival"} {...(arrivalForDialog ? { arrival: arrivalForDialog } : {})} uiAgentId={resolvedAgentId} onClose={() => setDialog(undefined)} onDismiss={(id) => session.dispatch({ type: "arrival/remove", scope, agent: id })} />
  </>;
}

async function refreshReminders(transport: RouterTransport): Promise<void> { await transport.refreshReminders(); }

export default App;
export { AppShell } from "./components/AppShell";
export { Sidebar } from "./components/Sidebar";
export { BrandHeader } from "./components/BrandHeader";
export { RouterSelector } from "./components/RouterSelector";
export { ConnectionStatus } from "./components/ConnectionStatus";
export { AgentList } from "./components/AgentList";
export { AgentRow } from "./components/AgentRow";
export { SubagentBadge } from "./components/SubagentBadge";
export { ArrivalIndicator } from "./components/ArrivalIndicator";
export { Conversation } from "./components/Conversation";
export { ConversationHeader } from "./components/ConversationHeader";
export { InlineToolSettingsDialog } from "./components/InlineToolSettingsDialog";
export { MessageList } from "./components/MessageList";
export { MessageBubble } from "./components/MessageBubble";
export { AttachmentGallery } from "./components/AttachmentGallery";
export { Composer } from "./components/Composer";
export { ImagePreview } from "./components/ImagePreview";
export { EmptyState } from "./components/EmptyState";
export { StatusMessage } from "./components/StatusMessage";
