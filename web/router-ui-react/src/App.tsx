import { useCallback, useEffect, useMemo, useRef, useState, type ComponentProps, type FormEvent, type ReactElement } from "react";
import "./app.css";
import { createInitialState, type AppState } from "./state/app-state";
import { selectArrival, selectMessages, selectModels } from "./state/selectors";
import { mergeAgentSources, sourceAgentKey } from "./state/agent-projection";
import { activeRemindersForPeer, lockAfterCreateStatus, lockResultState, reminderLockErrorMessage, reminderLockKey, type Reminder, type ReminderCreateInput, type ReminderLockAction } from "./domain/reminders";
import { normalizeModelSummary, type ModelSummary } from "./domain/models";
import { mergeMessages, pendingMessage, reconcileHistoryMessages, settlePendingMessages, type HistoryMessage } from "./domain/history";
import { normalizeRouterUrl, persistedActiveRouter, createSettingsRepository, migrateBootstrapRouter, setActiveRouter, setAgentId, setRouterAlias, LEGACY_AGENT_ID_KEY, setSelectedPeer as persistSelectedPeer, selectedPeerFor, removeRouter as removeSavedRouter, type SettingsState, type StorageAdapter } from "./persistence/settings-repository";
import { loadManagedRecentFolders, normalizeManagedRecentFolders, recordManagedRecentFolder, saveManagedRecentFolders } from "./persistence/managed-folder-history";
import { type RouterTransport, type RouterTransportOptions } from "./transport/router-transport";
import { managedRequestErrorMessage } from "./transport/http-client";
import { loadManagedGoalProjection } from "./transport/managed-goal-projection";
import { uiLiveErrorMessage } from "./transport/ui-live-client";
import { isRecord } from "./protocol/guards";
import { secureUuid } from "./platform/secure-uuid";
import { useRouterSession } from "./hooks/use-router-session";
import { useAgentReconciliation } from "./hooks/use-agent-reconciliation";
import { useConversation } from "./hooks/use-conversation";
import { useDrafts } from "./hooks/use-drafts";
import { HistoryRepository, initializePersistence } from "./persistence/history-repository";
import { AppShell } from "./components/AppShell";
import { Sidebar } from "./components/Sidebar";
import { Conversation } from "./components/Conversation";
import { RouterManagerDialog } from "./components/RouterManagerDialog";
import { ProviderAuthDialog } from "./components/ProviderAuthDialog";
import { ManagedAgentDialog } from "./components/ManagedAgentDialog";
import { ManagedControlDialog } from "./components/ManagedControlDialog";
import { GoalEditorDialog } from "./components/GoalEditorDialog";
import { InlineToolSettingsDialog } from "./components/InlineToolSettingsDialog";
import { ReminderDialog, type ReminderDialogState } from "./components/ReminderDialog";
import { PromptControls } from "./components/PromptControls";
import { PromptTemplateDialog } from "./components/PromptTemplateDialog";
import { ArrivalDialog } from "./components/ArrivalDialog";
import { ModelPicker } from "./components/ModelPicker";
import type { ImageInput } from "./domain/images";
import { routerDisplayName, type SavedRouter } from "./domain/router-manager";
import { promptTemplateUpdatePayload, type PromptTemplate } from "./domain/prompt-templates";
import { runPromptTemplateImport, type PromptTemplateImportProgress, type PromptTemplateImportResult } from "./domain/prompt-template-import";
import { DEFAULT_GOAL_STATE, goalErrorMessage, type GoalControlSettings, type GoalState } from "./domain/goals";
import { withAbsoluteAttachmentUrls } from "./transport/attachment-urls";
import { createHistoryThumbnail } from "./domain/images";
import { cacheKeyForAttachment, saveCachedImage } from "./persistence/history-repository";
import { RouterLogViewer } from "./components/RouterLogViewer";

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

type Dialog = "routers" | "provider-settings" | "goal" | "reminders" | "inline-tools" | "prompts" | "arrival" | "managed" | undefined;

interface RouterConfiguration {
  routerUrl: string;
  bootstrapRouterUrl?: string;
}

function shouldUseBootstrapProxy(persistedRouterUrl: string, bootstrapRouterUrl: string): boolean {
  try {
    const persisted = new URL(normalizeRouterUrl(persistedRouterUrl));
    const bootstrap = new URL(normalizeRouterUrl(bootstrapRouterUrl));
    return persisted.hostname === bootstrap.hostname && persisted.origin !== bootstrap.origin;
  } catch {
    return false;
  }
}

function configuredRouterUrl(props: AppProps): RouterConfiguration | undefined {
  const explicit = props.routerUrl ?? props.state?.routerUrl;
  if (typeof explicit === "string" && explicit.trim() !== "") return { routerUrl: explicit };

  const configuredBootstrapRouterUrl: unknown = import.meta.env.VITE_ROUTER_URL;
  const bootstrapRouterUrl = typeof configuredBootstrapRouterUrl === "string" && configuredBootstrapRouterUrl.trim() !== "" ? configuredBootstrapRouterUrl : undefined;
  const persisted = persistedActiveRouter(props.storage, bootstrapRouterUrl);
  if (persisted !== undefined) {
    // A prior direct router setting on this host bypasses the Web UI proxy and
    // therefore revives the cross-origin launch path. Keep other saved routers
    // untouched, but make the public Web UI origin active on startup.
    const routerUrl = bootstrapRouterUrl !== undefined && shouldUseBootstrapProxy(persisted, bootstrapRouterUrl)
      ? bootstrapRouterUrl
      : persisted;
    return { routerUrl, ...(bootstrapRouterUrl === undefined ? {} : { bootstrapRouterUrl }) };
  }
  if (bootstrapRouterUrl !== undefined) return { routerUrl: bootstrapRouterUrl, bootstrapRouterUrl };
  return undefined;
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
  const [configured, setConfigured] = useState<RouterConfiguration | undefined>(() => configuredRouterUrl(props));
  const candidate = configured ?? configuredRouterUrl(props);
  if (candidate === undefined) return <ConfigurationScreen onConfigure={(routerUrl) => setConfigured({ routerUrl })} />;
  try {
    const normalized = normalizeRouterUrl(candidate.routerUrl);
    return <ConfiguredApp {...props} routerUrl={normalized} {...(candidate.bootstrapRouterUrl === undefined ? {} : { bootstrapRouterUrl: candidate.bootstrapRouterUrl })} />;
  } catch {
    return <ConfigurationScreen initialValue={candidate.routerUrl} initialError="Router URL is invalid" onConfigure={(routerUrl) => setConfigured({ routerUrl })} />;
  }
}

function PersistenceStartupScreen({ error }: { error?: string }): ReactElement {
  return <main className="configuration-screen"><section className="empty-state" aria-labelledby="persistence-title"><h1 id="persistence-title">Loading Mecha</h1>{error ? <p className="error" role="alert">{error}</p> : <p>Loading local history…</p>}</section></main>;
}

function ConfiguredApp(props: AppProps & { routerUrl: string; bootstrapRouterUrl?: string }): ReactElement {
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

function ConfiguredAppContent({ routerUrl, bootstrapRouterUrl, agentId: requestedAgentId, connect = true, state: suppliedState, storage, transportOptions, onRefresh, onSelectAgent, onFilterChange, onRouterClick, onSend: externalSend, onGoal: externalGoal, onReminders: externalReminders, onInlineToolSettings: externalInlineToolSettings, onOpenArrival, onDismissArrival, onToggleSubagents }: AppProps & { routerUrl: string; bootstrapRouterUrl?: string }): ReactElement {
  const settingsRepository = useMemo(() => createSettingsRepository(storage, routerUrl), [storage, routerUrl]);
  const [settings, setSettings] = useState<SettingsState>(() => {
    const loaded = setActiveRouter(migrateBootstrapRouter(settingsRepository.load(), bootstrapRouterUrl), routerUrl);
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
  const [providerPrompt, setProviderPrompt] = useState<import("./protocol/types").ProviderAuthPromptFrame>();
  const [providerRefresh, setProviderRefresh] = useState(0);
  const persistDelivery = useCallback(async (messages: readonly HistoryMessage[]): Promise<void> => {
    const grouped = new Map<string, HistoryMessage[]>();
    for (const message of messages) {
      const peer = message.from === resolvedAgentId ? message.to : message.from;
      if (typeof peer !== "string" || peer === "" || peer === resolvedAgentId) continue;
      grouped.set(peer, [...(grouped.get(peer) ?? []), message]);
    }
    await Promise.all([...grouped].map(([peer, entries]) => historyRepository.savePeer(peer, scope, entries)));
  }, [historyRepository, resolvedAgentId, scope]);
  const session = useRouterSession({ routerUrl: activeUrl, agentId: resolvedAgentId, defaultAgentId: resolvedAgentId, initialState, connect, ...transportOptions, persistDelivery, onProviderAuthFrame: (frame) => { if (frame.type === "provider_auth_prompt") setProviderPrompt(frame); else setProviderRefresh((current) => current + 1); }, onUILiveError: (error) => setManagedLiveError(uiLiveErrorMessage(error)) });
  const sessionStateRef = useRef(session.state);
  sessionStateRef.current = session.state;
  const reconciliation = useAgentReconciliation(session.transport, scope);
  const selectedFromState = session.state.selectedPeer;
  const [selectedPeer, setSelectedPeer] = useState<string | undefined>(() => selectedFromState ?? (selectedPeerFor(settings, activeUrl, resolvedAgentId) || undefined));
  const selectedConversationPeer = selectedPeer === undefined ? undefined : selectedPeer.startsWith("tmux:") ? selectedPeer.slice("tmux:".length) : selectedPeer.startsWith("router-managed:") ? selectedPeer.slice("router-managed:".length) : selectedPeer;
  const previousScope = useRef(scope);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [dialog, setDialog] = useState<Dialog>();
  const [logsAgentId, setLogsAgentId] = useState<string>();
  const [logsOpen, setLogsOpen] = useState(false);
  const [conversationView, setConversationView] = useState<"quiet" | "complete">("quiet");
  const [completeHistoryReadyAgent, setCompleteHistoryReadyAgent] = useState<string>();
  const [arrivalAgent, setArrivalAgent] = useState<string>();
  const [managedAgentID, setManagedAgentID] = useState<string>();
  const [managedError, setManagedError] = useState<string>();
  const [managedLiveError, setManagedLiveError] = useState<string>();
  const [managedPending, setManagedPending] = useState(false);
  const [managedHeaderModels, setManagedHeaderModels] = useState<ModelSummary[]>([]);
  const [managedHeaderModelsLoading, setManagedHeaderModelsLoading] = useState(false);
  const [managedHeaderModelError, setManagedHeaderModelError] = useState<string>();
  const [managedRecentFolders, setManagedRecentFolders] = useState(() => loadManagedRecentFolders(storage));
  const distinctRecentFolders = useMemo(() => {
    const knownManagedFolders = (session.state.managedAgents ?? []).map((agent) => agent.cwd);
    const peerManagedFolders = session.state.agents.map((agent) => agent.managed_agent?.cwd).filter((cwd): cwd is string => Boolean(cwd));
    return normalizeManagedRecentFolders([...managedRecentFolders, ...knownManagedFolders, ...peerManagedFolders]);
  }, [managedRecentFolders, session.state.managedAgents, session.state.agents]);

  useEffect(() => {
    const knownManagedFolders = (session.state.managedAgents ?? []).map((agent) => agent.cwd);
    const peerManagedFolders = session.state.agents.map((agent) => agent.managed_agent?.cwd).filter((cwd): cwd is string => Boolean(cwd));
    const knownFolders = [...knownManagedFolders, ...peerManagedFolders];
    if (knownFolders.length === 0) return;
    setManagedRecentFolders((current) => {
      const merged = normalizeManagedRecentFolders([...current, ...knownFolders]);
      if (merged.length !== current.length || merged.some((folder, index) => folder !== current[index])) {
        saveManagedRecentFolders(storage, merged);
        return merged;
      }
      return current;
    });
  }, [session.state.managedAgents, session.state.agents, storage]);
  const [managedTerminationConfirmation, setManagedTerminationConfirmation] = useState(false);
  const [managedGoalState, setManagedGoalState] = useState<GoalState>();
  const [managedInlineToolsEnabled, setManagedInlineToolsEnabled] = useState<boolean>();
  const [goalError, setGoalError] = useState<string>();
  const [reminderStatus, setReminderStatus] = useState<string>();
  const [goalControlAvailable, setGoalControlAvailable] = useState(false);
  const [inlineToolControlAvailable, setInlineToolControlAvailable] = useState(false);
  const [inlineToolError, setInlineToolError] = useState<string>();
  const [templatesLoaded, setTemplatesLoaded] = useState(false);
  const [promptImportProgress, setPromptImportProgress] = useState<PromptTemplateImportProgress>();
  const [templateToUse, setTemplateToUse] = useState<PromptTemplate>();
  const [promptReturnDialog, setPromptReturnDialog] = useState<"reminders">();
  const [dialogAfterClose, setDialogAfterClose] = useState<"reminders">();
  const [reminderTemplateToUse, setReminderTemplateToUse] = useState<PromptTemplate>();
  const [reminderSelection, setReminderSelection] = useState<{ start: number; end: number }>();
  const [reminderDialogState, setReminderDialogState] = useState<ReminderDialogState>({ mode: "list", subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false });
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
  const conversation = useConversation(session.transport, scope, selectedConversationPeer, persistConversation, reconcileConversationState);
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
    setExpanded(new Set()); setDialog(undefined); setLogsOpen(false); setLogsAgentId(undefined); setProviderPrompt(undefined); setArrivalAgent(undefined); setManagedAgentID(undefined); setManagedError(undefined); setManagedLiveError(undefined); setManagedPending(false); setManagedTerminationConfirmation(false); setManagedGoalState(undefined); setManagedInlineToolsEnabled(undefined); setGoalError(undefined); setReminderStatus(undefined);
    setGoalControlAvailable(false); setInlineToolControlAvailable(false); setInlineToolError(undefined); setTemplatesLoaded(false); setPromptImportProgress(undefined); setTemplateToUse(undefined); setPromptReturnDialog(undefined); setDialogAfterClose(undefined); setReminderTemplateToUse(undefined); setReminderSelection(undefined); setReminderDialogState({ mode: "list", subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false }); setComposerSelection(undefined);
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

  const visibleAgents = useMemo(() => mergeAgentSources(session.state.agents, session.state.managedAgents ?? [], session.state.managedChildren), [session.state.agents, session.state.managedAgents, session.state.managedChildren]);
  // Saved selections from before source-qualified rows are legacy tmux IDs.
  const selectedSourceKey = selectedPeer === undefined ? undefined : selectedPeer.includes(":") ? selectedPeer : sourceAgentKey("tmux", selectedPeer);
  const selectedAgent = useMemo(() => visibleAgents.find((agent) => sourceAgentKey(agent.source, agent.agent_id) === selectedSourceKey), [visibleAgents, selectedSourceKey]);
  const isManagedSelected = selectedSourceKey?.startsWith("router-managed:") || selectedAgent?.source === "router-managed" || selectedAgent?.managed_agent?.runtime === "embedded" || (selectedAgent as { runtime?: string })?.runtime === "embedded";
  const selectedLegacyPeer = isManagedSelected ? undefined : selectedAgent?.agent_id;
  const selectedManagedAgent = useMemo(() => managedAgentID === undefined ? undefined : (session.state.managedAgents ?? []).find((agent) => agent.agent_id === managedAgentID), [managedAgentID, session.state.managedAgents]);
  useEffect(() => { setConversationView(isManagedSelected ? "complete" : "quiet"); }, [selectedPeer, isManagedSelected]);
  useEffect(() => {
    if (selectedAgent?.source !== "router-managed") {
      setManagedHeaderModels([]); setManagedHeaderModelsLoading(false); setManagedHeaderModelError(undefined);
      return;
    }
    let active = true;
    setManagedHeaderModelsLoading(true); setManagedHeaderModelError(undefined);
    void session.transport.http.models().then((catalog) => {
      if (!active) return;
      setManagedHeaderModels(catalog.models.filter((model) => model.available).map(({ provider, id, name }) => ({ provider, id, name })));
    }).catch((cause: unknown) => {
      if (active) setManagedHeaderModelError(cause instanceof Error ? cause.message : "Models could not be loaded.");
    }).finally(() => { if (active) setManagedHeaderModelsLoading(false); });
    return () => { active = false; };
  }, [selectedAgent?.agent_id, selectedAgent?.source, session.transport.http]);
  useEffect(() => { setCompleteHistoryReadyAgent(undefined); }, [activeUrl, selectedAgent?.agent_id, selectedAgent?.source]);
  useEffect(() => {
    if (!session.state.managedAvailable || selectedAgent?.source !== "router-managed") return;
    let active = true;
    void session.transport.http.managedHistory(selectedAgent.agent_id, "complete", { limit: 200 }).then((page) => {
      if (!active) return;
      for (const entry of page.entries) session.dispatch({ type: "managed/history", scope, view: "complete", entry });
    }).catch(() => undefined).finally(() => {
      if (active) setCompleteHistoryReadyAgent(selectedAgent.agent_id);
    });
    return () => { active = false; };
  }, [scope, selectedAgent?.agent_id, selectedAgent?.source, session.dispatch, session.state.managedAvailable, session.state.managedProjectionVersion, session.state.managedChildrenProjectionVersion, session.transport]);
  const arrival = selectedLegacyPeer ? selectArrival(session.state, selectedLegacyPeer) : undefined;
  const setSelection = (peer: string): void => { setLogsOpen(false); setLogsAgentId(undefined); setSelectedPeer(peer); onSelectAgent?.(peer); };
  const openLogsForSelectedAgent = (): void => {
    if (selectedAgent === undefined) return;
    setLogsAgentId(selectedAgent.agent_id);
    setLogsOpen(true);
  };
  const loadManagedGoal = (agentId: string): void => {
    setManagedPending(true); setManagedError(undefined); setGoalError(undefined);
    void session.transport.http.managedGoal(agentId).then((result) => {
      session.dispatch({ type: "managed/goal-state", scope, agentId, goalState: result.goal_state });
      setManagedGoalState(result.goal_state); setDialog("goal");
    }).catch((cause: unknown) => {
      setManagedError(managedRequestErrorMessage(cause, "Managed goal could not be loaded."));
    }).finally(() => setManagedPending(false));
  };
  const openGoalForAgent = (peer: string): void => {
    const target = visibleAgents.find((agent) => sourceAgentKey(agent.source, agent.agent_id) === peer) ?? visibleAgents.find((agent) => agent.source === "tmux" && agent.agent_id === peer);
    const selected = target === undefined ? peer : sourceAgentKey(target.source, target.agent_id);
    setSelectedPeer(selected); onSelectAgent?.(target?.agent_id ?? peer);
    if (target?.source === "router-managed") { setManagedAgentID(target.agent_id); loadManagedGoal(target.agent_id); return; }
    externalGoal?.(target?.agent_id ?? peer); setDialog("goal");
  };
  const openRemindersForAgent = (peer: string): void => { setSelection(peer); externalReminders?.(peer); setReminderDialogState({ mode: "list", subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false }); setDialog("reminders"); };
  const openInlineToolSettingsForAgent = (peer: string): void => { setSelection(peer); externalInlineToolSettings?.(peer); setInlineToolError(undefined); setDialog("inline-tools"); };
  const send = async (peer: string, body: string, images: ImageInput[]): Promise<void> => {
    // Use one client message ID for the optimistic row and the HTTP envelope.
    // The router preserves it, so reconnect history can settle this row safely.
    const localId = secureUuid();
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
    if (!selectedAgent?.instance_id || !selectedLegacyPeer) return;
    if (!inlineToolControlAvailable || selectedAgent.inline_tool_control_available !== true) { setInlineToolError("Inline tool control is not available for this worker."); return; }
    setInlineToolError(undefined);
    if (!session.transport.sendInlineToolControl(selectedLegacyPeer, selectedAgent.instance_id, enabled)) setInlineToolError("Inline tool setting could not be saved while the worker is disconnected.");
  };
  const saveGoal = (payload: { action: "set" | "off"; goal: string; settings: GoalControlSettings }): void => {
    if (!selectedAgent?.instance_id || !selectedLegacyPeer) return;
    if (!goalControlAvailable || !session.transport.goalControlAvailable) { setGoalError("Goal controls are not available on this router."); return; }
    const controlId = secureUuid();
    setGoalError(undefined);
    if (!session.transport.sendGoalControl(selectedLegacyPeer, selectedAgent.instance_id, { controlId, action: payload.action, goal: payload.goal, settings: payload.settings })) {
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
      const controlId = secureUuid();
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
    const controlId = secureUuid();
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
  const useReminderTemplate = (template: PromptTemplate, selection: { start: number; end: number }): void => { setReminderTemplateToUse(template); setTemplateToUse(undefined); setReminderSelection(selection); setPromptReturnDialog("reminders"); setDialog("prompts"); };
  const closePromptTemplateDialog = (): void => {
    setDialog(undefined);
    setDialogAfterClose(promptReturnDialog);
    setPromptReturnDialog(undefined);
    setPromptImportProgress(undefined);
    setTemplateToUse(undefined);
    setReminderTemplateToUse(undefined);
    setReminderSelection(undefined);
  };
  const reopenDialogAfterPromptClose = (): void => {
    if (dialogAfterClose !== undefined) setDialog(dialogAfterClose);
    setDialogAfterClose(undefined);
  };
  const insertReminderTemplate = (value: string, selection?: { start: number; end: number }): void => {
    setReminderDialogState((current) => {
      const start = Math.max(0, Math.min(selection?.start ?? current.body.length, current.body.length));
      const end = Math.max(start, Math.min(selection?.end ?? start, current.body.length));
      return { ...current, body: `${current.body.slice(0, start)}${value}${current.body.slice(end)}` };
    });
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
  const routerLabel = routerDisplayName(activeUrl, settings.aliases[activeUrl]);
  const modelOptions = selectedAgent?.source === "router-managed" ? managedHeaderModels : selectModels(selectedAgent);
  const selectedModel = normalizeModelSummary(selectedAgent?.current_model);
  const pendingModelControl = [...session.state.pendingControls.values()].some((pending) => pending.action === "model" && pending.peer === selectedAgent?.agent_id && pending.instanceId === selectedAgent?.instance_id);
  const pendingManagedModelChange = selectedAgent?.source === "router-managed" && managedPending;
  const pendingGoalControl = [...session.state.pendingControls.values()].some((pending) => pending.action === "goal" && pending.peer === selectedAgent?.agent_id && pending.instanceId === selectedAgent?.instance_id);
  const pendingInlineToolControl = [...session.state.pendingControls.values()].some((pending) => pending.action === "inline-tool" && pending.peer === selectedAgent?.agent_id && pending.instanceId === selectedAgent?.instance_id);
  const selectedAgentActiveReminderCount = selectedLegacyPeer === undefined ? 0 : activeRemindersForPeer(session.state.reminders, selectedLegacyPeer).length;
  const pendingReminderIds = new Set([...session.state.pendingControls.values()].flatMap((pending) => pending.action === "reminder-lock" && pending.peer === selectedLegacyPeer && pending.reminderId !== undefined ? [pending.reminderId] : []));
  const arrivalForDialog = arrivalAgent ? session.state.arrivals.get(arrivalAgent) : undefined;
  const refreshManagedGoalStates = (agents: Parameters<typeof loadManagedGoalProjection>[1]): void => {
    void loadManagedGoalProjection(session.transport.http, agents).then((goals) => {
      for (const { agentId, goalState } of goals) session.dispatch({ type: "managed/goal-state", scope, agentId, goalState });
    });
  };
  const refreshManaged = async (): Promise<void> => {
    // A router restart can temporarily clear the capability frame while stale
    // managed rows remain visible. Refresh the direct endpoint anyway so the
    // user can recover their retained child records without another reconnect.
    const snapshot = await session.transport.http.managedAgents();
    const children = await Promise.all(snapshot.agents.map((agent) => session.transport.http.managedChildren(agent.agent_id)));
    session.dispatch({ type: "managed/snapshot", scope, version: snapshot.projection_version, agents: snapshot.agents });
    refreshManagedGoalStates(snapshot.agents);
    session.dispatch({ type: "managed/children", scope, children });
  };
  const changeManagedModel = async (model: ModelSummary): Promise<void> => {
    if (selectedAgent?.source !== "router-managed" || selectedAgent.managed_agent?.accepting_prompts !== true) return;
    setManagedPending(true); setManagedError(undefined);
    try {
      await session.transport.http.restartManaged(selectedAgent.agent_id, { provider: model.provider, id: model.id });
      await refreshManaged();
    } catch (cause) {
      setManagedError(managedRequestErrorMessage(cause, "Managed model could not be changed."));
    } finally { setManagedPending(false); }
  };
  const launchManaged = async (input: { name: string; cwd: string; runtime?: "rpc" | "embedded"; model?: { provider: string; id: string }; initial_instruction?: string }): Promise<void> => {
    setManagedPending(true); setManagedError(undefined);
    try {
      const launched = await session.transport.http.launchManaged(input);
      const folder = launched.agent?.cwd ?? input.cwd;
      setManagedRecentFolders((current) => {
        const next = recordManagedRecentFolder(current, folder);
        saveManagedRecentFolders(storage, next);
        return next;
      });
      await refreshManaged();
      setDialog(undefined);
    }
    catch (cause) { setManagedError(managedRequestErrorMessage(cause, "Managed agent could not be launched.")); }
    finally { setManagedPending(false); }
  };
  const manageAction = async (agentId: string, action: "restart" | "reset" | "terminate"): Promise<void> => {
    setManagedPending(true); setManagedError(undefined);
    try { if (action === "restart") await session.transport.http.restartManaged(agentId); else if (action === "reset") await session.transport.http.resetManaged(agentId); else await session.transport.http.terminateManaged(agentId); await refreshManaged(); if (action === "terminate") { setDialog(undefined); setManagedTerminationConfirmation(false); } }
    catch (cause) { setManagedError(managedRequestErrorMessage(cause, "Managed action could not be completed.")); }
    finally { setManagedPending(false); }
  };
  const openManagedGoal = (): void => {
    if (selectedManagedAgent !== undefined) loadManagedGoal(selectedManagedAgent.agent_id);
  };
  const saveManagedGoal = (payload: { action: "set" | "off"; goal: string; settings: GoalControlSettings }): void => {
    if (selectedManagedAgent === undefined) return;
    setManagedPending(true); setGoalError(undefined);
    void session.transport.http.saveManagedGoal(selectedManagedAgent.agent_id, payload).then((result) => {
      session.dispatch({ type: "managed/goal-state", scope, agentId: selectedManagedAgent.agent_id, goalState: result.goal_state });
      setManagedGoalState(result.goal_state); setDialog(undefined);
    }).catch((cause: unknown) => {
      setGoalError(managedRequestErrorMessage(cause, "Managed goal could not be saved."));
    }).finally(() => setManagedPending(false));
  };
  const openManagedInlineTools = (): void => {
    if (selectedManagedAgent === undefined) return;
    setManagedPending(true); setManagedError(undefined); setInlineToolError(undefined);
    void session.transport.http.managedInlineTools(selectedManagedAgent.agent_id).then((result) => {
      setManagedInlineToolsEnabled(result.enabled); setDialog("inline-tools");
    }).catch((cause: unknown) => {
      setManagedError(managedRequestErrorMessage(cause, "Managed inline-tool settings could not be loaded."));
    }).finally(() => setManagedPending(false));
  };
  const saveManagedInlineTools = (enabled: boolean): void => {
    if (selectedManagedAgent === undefined) return;
    setManagedPending(true); setInlineToolError(undefined);
    void session.transport.http.saveManagedInlineTools(selectedManagedAgent.agent_id, enabled).then((result) => {
      setManagedInlineToolsEnabled(result.enabled); setDialog(undefined);
    }).catch((cause: unknown) => {
      setInlineToolError(managedRequestErrorMessage(cause, "Managed inline-tool settings could not be saved."));
    }).finally(() => setManagedPending(false));
  };
  const openManagedReminders = (): void => {
    if (selectedManagedAgent === undefined) return;
    setReminderDialogState({ mode: "list", subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false });
    setReminderStatus(undefined); setDialog("reminders");
  };
  const restartAllManaged = (): void => { setManagedPending(true); setManagedError(undefined); void session.transport.http.restartAllManaged().then(refreshManaged).catch((cause: unknown) => setManagedError(managedRequestErrorMessage(cause, "Managed agents could not be restarted."))).finally(() => setManagedPending(false)); };
  const toggleSubagents = (id: string): void => {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  return <>
    <AppShell
      sidebar={<Sidebar routerLabel={routerLabel} connection={session.state.connection} connectionIdentity={resolvedAgentId} connectionDetail={reconciliation.error?.message} {...(managedLiveError ? { managedStatus: managedLiveError } : {})} agents={visibleAgents} {...(selectedSourceKey === undefined ? {} : { selectedAgentId: selectedSourceKey })} filter={session.state.filter} arrivals={session.state.arrivals} reminderCounts={Object.fromEntries(session.state.agents.map((agent) => [agent.agent_id, activeRemindersForPeer(session.state.reminders, agent.agent_id).length]))} expandedAgentIds={expanded} onRefresh={() => { void reconciliation.refresh(); void refreshManaged().catch(() => undefined); onRefresh?.(); }} onRouterClick={() => { setDialog("routers"); onRouterClick?.(); }} onFilterChange={(filter) => { session.dispatch({ type: "filter/set", scope, filter }); onFilterChange?.(filter); }} onSelectAgent={setSelection} onGoal={openGoalForAgent} onReminders={openRemindersForAgent} onToggleSubagents={(id) => { toggleSubagents(id); onToggleSubagents?.(id); }} onOpenArrival={(item) => { setArrivalAgent(item.agent); setDialog("arrival"); onOpenArrival?.(item.agent); }} onDismissArrival={(id) => { session.dispatch({ type: "arrival/remove", scope, agent: id }); onDismissArrival?.(id); }} {...(session.state.managedAvailable ? { onLaunchManaged: () => { setManagedAgentID(undefined); setManagedError(undefined); setManagedTerminationConfirmation(false); setDialog("managed"); }, onRestartAllManaged: restartAllManaged, onProviderSettings: () => { setManagedError(undefined); setDialog("provider-settings"); } } : {})} />}
      conversation={<div className="conversation-runtime"><Conversation {...(selectedAgent ? { peer: selectedAgent } : {})} activeReminderCount={selectedAgentActiveReminderCount} ownerId={resolvedAgentId} routerUrl={activeUrl} messages={mergeMessages(selectMessages(session.state, selectedConversationPeer), conversation.messages)} {...(isManagedSelected && selectedAgent ? { conversationView: "complete" as const, activityEntries: session.state.managedHistory?.[selectedAgent.agent_id]?.complete ?? [], activityHistoryReady: completeHistoryReadyAgent === selectedAgent.agent_id } : {})} composeEnabled={selectedConversationPeer !== undefined && (selectedAgent?.source !== "router-managed" || selectedAgent.managed_agent?.accepting_prompts === true)} showAgentControls={selectedLegacyPeer !== undefined} showGoalControl={selectedLegacyPeer !== undefined || selectedAgent?.source === "router-managed"} value={composerDraft} onChange={setComposerDraft} onSelectionChange={setComposerSelection} {...(selectedAgent && (selectedLegacyPeer !== undefined || selectedAgent.source === "router-managed") ? { modelControls: <ModelPicker models={modelOptions} {...(selectedModel ? { currentModel: selectedModel } : {})} isEmbedded={Boolean(selectedAgent && ('runtime' in selectedAgent ? (selectedAgent as any).runtime === 'embedded' : (selectedAgent as any).managed_agent?.runtime === 'embedded'))} pending={selectedAgent.source === "router-managed" ? pendingManagedModelChange : pendingModelControl} loading={selectedAgent.source === "router-managed" && managedHeaderModelsLoading} {...(selectedAgent.source === "router-managed" && managedHeaderModelError ? { emptyMessage: managedHeaderModelError } : {})} {...(selectedAgent.source === "router-managed" && managedError ? { status: managedError } : selectedAgent.source === "router-managed" && managedHeaderModelError ? { status: managedHeaderModelError } : {})} disabled={selectedAgent.source === "router-managed" ? selectedAgent.managed_agent?.accepting_prompts !== true || pendingManagedModelChange : session.state.connection !== "connected" || pendingModelControl} onChange={selectedAgent.source === "router-managed" ? changeManagedModel : chooseModel} /> } : {})} {...(selectedAgent ? { promptControls: <PromptControls templates={session.state.templates} loaded={templatesLoaded} disabled={selectedAgent.source === "router-managed" ? selectedAgent.managed_agent?.accepting_prompts !== true : session.state.connection !== "connected"} onManage={() => setDialog("prompts")} onUse={(template) => { setTemplateToUse(template); setDialog("prompts"); }} /> } : {})} loading={conversation.loading} {...(conversation.error ? { error: conversation.error.message } : {})} {...(arrival ? { arrival } : {})} onSend={(body, images) => { if (selectedConversationPeer) void send(selectedConversationPeer, body, images).catch(() => undefined); }} onManageManaged={() => { if (selectedAgent?.source === "router-managed") { setManagedAgentID(selectedAgent.agent_id); setManagedError(undefined); setManagedTerminationConfirmation(false); setDialog("managed"); } }} onLogs={openLogsForSelectedAgent} onGoal={() => { if (selectedLegacyPeer) { setDialog("goal"); externalGoal?.(selectedLegacyPeer); } else if (selectedAgent?.source === "router-managed") { setManagedAgentID(selectedAgent.agent_id); loadManagedGoal(selectedAgent.agent_id); } }} onReminders={() => { if (selectedLegacyPeer) { setReminderDialogState({ mode: "list", subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false }); setDialog("reminders"); externalReminders?.(selectedLegacyPeer); } }} onInlineToolSettings={() => { if (selectedLegacyPeer) openInlineToolSettingsForAgent(selectedLegacyPeer); }} onOpenArrival={(item) => { setArrivalAgent(item.agent); setDialog("arrival"); onOpenArrival?.(item.agent); }} onDismissArrival={(id) => session.dispatch({ type: "arrival/remove", scope, agent: id })} onToggleSubagents={() => { if (selectedSourceKey) { toggleSubagents(selectedSourceKey); onToggleSubagents?.(selectedSourceKey); } }} expanded={selectedSourceKey !== undefined && expanded.has(selectedSourceKey)} /></div>}
    />
    {logsAgentId !== undefined ? <RouterLogViewer open={logsOpen} agentId={logsAgentId} routerUrl={activeUrl} onClose={() => setLogsOpen(false)} onClosed={() => setLogsAgentId(undefined)} /> : null}
    <RouterManagerDialog open={dialog === "routers"} routers={settings.routers.map((url) => ({ url, ...(settings.aliases[url] ? { alias: settings.aliases[url] } : {}), agentId: settings.agentIds[url] ?? (url === activeUrl ? resolvedAgentId : requestedAgentId ?? "wolfgang") }))} activeUrl={activeUrl} defaultAgentId={requestedAgentId ?? "wolfgang"} onClose={() => setDialog(undefined)} onSave={saveRouter} onSelect={selectRouter} onRemove={removeRouter} onProviderSettings={() => setDialog("provider-settings")} />
    <ProviderAuthDialog open={dialog === "provider-settings"} client={session.transport.http} refreshKey={providerRefresh} {...(providerPrompt ? { prompt: providerPrompt } : {})} onClose={() => setDialog(undefined)} onPromptHandled={() => setProviderPrompt(undefined)} />
    {selectedLegacyPeer && selectedAgent ? <InlineToolSettingsDialog open={dialog === "inline-tools"} peer={selectedLegacyPeer} enabled={selectedAgent.inline_tool_enabled === true} capabilityAvailable={inlineToolControlAvailable && selectedAgent.inline_tool_control_available === true} pending={pendingInlineToolControl} {...(inlineToolError ? { error: inlineToolError } : {})} onClose={() => { setDialog(undefined); setInlineToolError(undefined); }} onSave={saveInlineToolSettings} /> : selectedManagedAgent ? <InlineToolSettingsDialog open={dialog === "inline-tools"} peer={selectedManagedAgent.agent_id} enabled={managedInlineToolsEnabled === true} capabilityAvailable={selectedManagedAgent.accepting_prompts} pending={managedPending} {...(inlineToolError ? { error: inlineToolError } : {})} onClose={() => { setDialog(undefined); setInlineToolError(undefined); }} onSave={saveManagedInlineTools} /> : null}
    {selectedLegacyPeer && selectedAgent ? <GoalEditorDialog open={dialog === "goal"} peer={selectedLegacyPeer} {...(selectedAgent.goal_state ? { goalState: selectedAgent.goal_state } : {})} capabilityAvailable={goalControlAvailable && session.transport.goalControlAvailable} pending={pendingGoalControl} {...(goalError ? { error: goalError } : {})} onClose={() => { setDialog(undefined); }} onSave={saveGoal} /> : selectedManagedAgent ? <GoalEditorDialog open={dialog === "goal"} peer={selectedManagedAgent.agent_id} goalState={managedGoalState ?? DEFAULT_GOAL_STATE} capabilityAvailable={selectedManagedAgent.accepting_prompts} pending={managedPending} {...(goalError ? { error: goalError } : {})} onClose={() => { setDialog(undefined); }} onSave={saveManagedGoal} /> : null}
    {selectedLegacyPeer ? <ReminderDialog open={dialog === "reminders"} peer={selectedLegacyPeer} creator={resolvedAgentId} reminders={session.state.reminders} locks={Object.fromEntries(session.state.reminderLocks)} pendingIds={pendingReminderIds} templates={session.state.templates} templatesLoaded={templatesLoaded} dialogState={reminderDialogState} onDialogStateChange={setReminderDialogState} onUseTemplate={useReminderTemplate} onManageTemplates={() => { setReminderTemplateToUse(undefined); setPromptReturnDialog("reminders"); setDialog("prompts"); }} {...(reminderStatus ? { status: reminderStatus } : {})} onClose={() => { setDialog(undefined); setReminderDialogState({ mode: "list", subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false }); }} onCreate={(payload, lock) => createReminder(payload, lock)} onUpdate={(id, payload) => updateReminder(id, payload)} onDelete={(id) => deleteReminder(id)} onLock={lockReminder} /> : selectedManagedAgent ? <ReminderDialog open={dialog === "reminders"} peer={selectedManagedAgent.agent_id} creator={resolvedAgentId} reminders={session.state.reminders} locks={{}} pendingIds={new Set()} templates={session.state.templates} templatesLoaded={templatesLoaded} dialogState={reminderDialogState} onDialogStateChange={setReminderDialogState} onUseTemplate={useReminderTemplate} onManageTemplates={() => { setReminderTemplateToUse(undefined); setPromptReturnDialog("reminders"); setDialog("prompts"); }} {...(reminderStatus ? { status: reminderStatus } : {})} onClose={() => { setDialog(undefined); setReminderDialogState({ mode: "list", subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false }); }} onCreate={(payload, lock) => createReminder(payload, lock)} onUpdate={(id, payload) => updateReminder(id, payload)} onDelete={(id) => deleteReminder(id)} onLock={() => setReminderStatus("Reminder locking is not available for managed agents.")} /> : null}
    <PromptTemplateDialog open={dialog === "prompts"} templates={session.state.templates} loaded={templatesLoaded} {...(promptImportProgress === undefined ? {} : { importProgress: promptImportProgress })} {...(reminderTemplateToUse ? { templateToUse: reminderTemplateToUse } : templateToUse ? { templateToUse } : {})} {...(reminderTemplateToUse && reminderSelection ? { selection: reminderSelection } : !reminderTemplateToUse && composerSelection ? { selection: composerSelection } : {})} onClose={closePromptTemplateDialog} onClosed={reopenDialogAfterPromptClose} onSave={(template) => saveTemplate(template)} onDelete={(id) => deleteTemplate(id)} onImport={(items, overwrite) => importTemplates(items, overwrite)} onInsert={(value, selection) => { if (reminderTemplateToUse) insertReminderTemplate(value, selection); else insertTemplate(value, selection); }} />
    <ArrivalDialog open={dialog === "arrival"} {...(arrivalForDialog ? { arrival: arrivalForDialog } : {})} uiAgentId={resolvedAgentId} onClose={() => setDialog(undefined)} onDismiss={(id) => session.dispatch({ type: "arrival/remove", scope, agent: id })} />
    {selectedManagedAgent ? <ManagedControlDialog open={dialog === "managed"} agent={selectedManagedAgent} pending={managedPending} {...(managedError ? { error: managedError } : {})} confirmTermination={managedTerminationConfirmation} onConfirmTerminationChange={setManagedTerminationConfirmation} onClose={() => { setDialog(undefined); setManagedError(undefined); setManagedTerminationConfirmation(false); }} onAction={manageAction} onGoal={openManagedGoal} onReminders={openManagedReminders} onInlineTools={openManagedInlineTools} /> : <ManagedAgentDialog open={dialog === "managed"} recentFolders={distinctRecentFolders} pending={managedPending} {...(managedError ? { error: managedError } : {})} onClose={() => { setDialog(undefined); setManagedError(undefined); }} onLaunch={launchManaged} onAction={manageAction} onListDirectories={(path) => session.transport.http.managedDirectories(path)} onModels={session.transport.http.models} />}
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
export { RouterLogViewer } from "./components/RouterLogViewer";
