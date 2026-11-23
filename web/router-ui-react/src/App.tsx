import { sameModelEngine } from "./domain/models";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ComponentProps, type FormEvent, type ReactElement } from "react";
import "./app.css";
import { createInitialState, type AppState } from "./state/app-state";
import { canBridgeAccepted, canonicalSendId, selectArrival, selectMessages, selectModels, visibleAccepted } from "./state/selectors";
import { DeferredImageReleases, InFlightImageBudget } from "./state/outbox-budget";
import { mergeAgentSources, sourceAgentKey } from "./state/agent-projection";
import { activeRemindersForPeer, lockAfterCreateStatus, lockResultState, reminderLockErrorMessage, reminderLockKey, type Reminder, type ReminderCreateInput, type ReminderLockAction } from "./domain/reminders";
import { normalizeModelSummary, type ModelSummary } from "./domain/models";
import { mergeMessages, pendingMessage, reconcileHistoryMessages, settlePendingMessages, type HistoryMessage } from "./domain/history";
import { normalizeRouterUrl, persistedActiveRouter, createSettingsRepository, migrateBootstrapRouter, setActiveRouter, setAgentId, setRouterAlias, LEGACY_AGENT_ID_KEY, setSelectedPeer as persistSelectedPeer, selectedPeerFor, removeRouter as removeSavedRouter, DEFAULT_AGENT_ID, type SettingsState, type StorageAdapter } from "./persistence/settings-repository";
import { loadManagedRecentFolders, normalizeManagedRecentFolders, recordManagedRecentFolder, saveManagedRecentFolders } from "./persistence/managed-folder-history";
import { type RouterTransport, type RouterTransportOptions } from "./transport/router-transport";
import { managedRequestErrorMessage } from "./transport/http-client";
import { uiLiveErrorMessage } from "./transport/ui-live-client";
import { isRecord } from "./protocol/guards";
import type { ManagedAgent } from "./protocol/types";
import { secureUuid } from "./platform/secure-uuid";
import { useRouterSession } from "./hooks/use-router-session";
import { useAgentReconciliation } from "./hooks/use-agent-reconciliation";
import { useConversation } from "./hooks/use-conversation";
import { useSelectedManagedActivity } from "./hooks/use-selected-managed-activity";
import { useDrafts } from "./hooks/use-drafts";
import { HistoryRepository, initializePersistence } from "./persistence/history-repository";
import { AppShell } from "./components/AppShell";
import { Sidebar } from "./components/Sidebar";
import { Conversation } from "./components/Conversation";
import { RouterManagerDialog } from "./components/RouterManagerDialog";
import { ProviderAuthDialog } from "./components/ProviderAuthDialog";
import { SystemOneConfigDialog } from "./components/SystemOneConfigDialog";
import { ContextThresholdDialog } from "./components/ContextThresholdDialog";
import { MCPServersDialog } from "./components/MCPServersDialog";
import { ToolDefaultsDialog } from "./components/ToolDefaultsDialog";
import { ManagedAgentDialog } from "./components/ManagedAgentDialog";
import { TerminateConfirmationDialog } from "./components/TerminateConfirmationDialog";
import { GoalEditorDialog } from "./components/GoalEditorDialog";
import { InlineToolSettingsDialog } from "./components/InlineToolSettingsDialog";
import { ReminderDialog, type ReminderDialogState } from "./components/ReminderDialog";
import { PromptControls } from "./components/PromptControls";
import { PromptTemplateDialog } from "./components/PromptTemplateDialog";
import { ArrivalDialog } from "./components/ArrivalDialog";
import { ModelPicker } from "./components/ModelPicker";
import type { ImageInput } from "./domain/images";
import { routerDisplayName, type SavedRouter } from "./domain/router-manager";
import { promptTemplateCreatePayload, promptTemplateUpdatePayload, loadPromptTemplateUsage, recordPromptTemplateUsage, sortPromptTemplatesByMRU, type PromptTemplate } from "./domain/prompt-templates";
import { runPromptTemplateImport, type PromptTemplateImportProgress, type PromptTemplateImportResult } from "./domain/prompt-template-import";
import { DEFAULT_GOAL_STATE, goalErrorMessage, type GoalControlSettings, type GoalState } from "./domain/goals";
import { withAbsoluteAttachmentUrls } from "./transport/attachment-urls";
import { createHistoryThumbnail } from "./domain/images";
import { attachmentCacheIdentity, cacheKeyForAttachment, saveCachedImage } from "./persistence/history-repository";
import { RouterLogViewer } from "./components/RouterLogViewer";
import { ScratchpadViewer } from "./components/ScratchpadViewer";
import { KnowledgeBaseDialog } from "./components/KnowledgeBaseDialog";
import { WorkflowSchedulesModal } from "./components/WorkflowSchedulesModal";
import { WorkflowBackgroundRunModal } from "./components/WorkflowBackgroundRunModal";
import { WorkflowsPage } from "./components/WorkflowsPage";
import { WorkflowJournalModal } from "./components/WorkflowJournalModal";
import { ProgressHistoryModal } from "./components/ProgressHistoryModal";
import { BroadcastMessageDialog } from "./components/BroadcastMessageDialog";

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

type Dialog = "routers" | "provider-settings" | "mcp-servers" | "tool-defaults" | "knowledge-base" | "workflow-schedules" | "run-background-workflow" | "systemone-config" | "context-thresholds" | "goal" | "reminders" | "inline-tools" | "prompts" | "arrival" | "managed" | "terminate-confirmation" | "broadcast-message" | undefined;

interface RouterConfiguration {
  routerUrl: string;
  bootstrapRouterUrl?: string;
}

function shouldUseBootstrapProxy(persistedRouterUrl: string, bootstrapRouterUrl: string): boolean {
  try {
    if (bootstrapRouterUrl.includes(":8787")) return false;
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
  const rawBootstrap = typeof configuredBootstrapRouterUrl === "string" && configuredBootstrapRouterUrl.trim() !== "" ? configuredBootstrapRouterUrl : undefined;
  const bootstrapRouterUrl = rawBootstrap && !rawBootstrap.includes(":8787") ? rawBootstrap : undefined;
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
    const cacheKey = cacheKeyForAttachment(scope.routerUrl, scope.agentId, peer, canonicalSendId(accepted.id), attachmentCacheIdentity(attachment, index));
    try {
      const blob = await createHistoryThumbnail(source);
      await saveCachedImage({ key: cacheKey, blob, mime_type: "image/jpeg", name: attachment.name, createdAtMs: Date.now(), byteSize: blob.size });
      window.dispatchEvent(new CustomEvent("mecha:image-cached", { detail: cacheKey }));
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
    const configured = settingsRepository.agentId(loaded, loaded.activeRouter, requestedAgentId ?? DEFAULT_AGENT_ID);
    const persisted = loaded.agentIds[loaded.activeRouter] !== undefined
      || (loaded.routers[0] === loaded.activeRouter && storage?.getItem(LEGACY_AGENT_ID_KEY)?.trim() !== "");
    return requestedAgentId !== undefined && !persisted
      ? setAgentId(loaded, loaded.activeRouter, requestedAgentId)
      : loaded.agentIds[loaded.activeRouter] === undefined ? setAgentId(loaded, loaded.activeRouter, configured) : loaded;
  });
  const activeUrl = settings.activeRouter;
  const resolvedAgentId = settingsRepository.agentId(settings, activeUrl, requestedAgentId ?? DEFAULT_AGENT_ID);
  const initialState = useMemo(() => suppliedState ?? createInitialState({ routerUrl: activeUrl, agentId: resolvedAgentId }), [activeUrl, resolvedAgentId, suppliedState]);
  const scope = useMemo(() => ({ routerUrl: activeUrl, agentId: resolvedAgentId }), [activeUrl, resolvedAgentId]);
  const [providerPrompt, setProviderPrompt] = useState<import("./protocol/types").ProviderAuthPromptFrame>();
  const [providerRefresh, setProviderRefresh] = useState(0);
  const session = useRouterSession({ routerUrl: activeUrl, agentId: resolvedAgentId, defaultAgentId: resolvedAgentId, initialState, connect, ...transportOptions, onProviderAuthFrame: (frame) => { if (frame.type === "provider_auth_prompt") setProviderPrompt(frame); else setProviderRefresh((current) => current + 1); }, onUILiveError: (error) => setManagedLiveError(uiLiveErrorMessage(error)) });
  const sessionStateRef = useRef(session.state);
  sessionStateRef.current = session.state;
  const reconciliation = useAgentReconciliation(session.transport, scope);
  const selectedFromState = session.state.selectedPeer;
  const [selectedPeer, setSelectedPeer] = useState<string | undefined>(() => selectedFromState ?? (selectedPeerFor(settings, activeUrl, resolvedAgentId) || undefined));
  const previousScope = useRef(scope);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [dialog, setDialog] = useState<Dialog>();
  const [scratchpadAgentId, setScratchpadAgentId] = useState<string>();
  const [scratchpadOpen, setScratchpadOpen] = useState(false);
  const [conversationView, setConversationView] = useState<"quiet" | "complete">("quiet");
  const [arrivalAgent, setArrivalAgent] = useState<string>();
  const [managedAgentID, setManagedAgentID] = useState<string>();
  const [currentRoute, setCurrentRoute] = useState<string>(() => {
    return window.location.hash === "#/workflows" ? "workflows" : "chat";
  });

  useEffect(() => {
    const handleHashChange = (): void => {
      setCurrentRoute(window.location.hash === "#/workflows" ? "workflows" : "chat");
    };
    window.addEventListener("hashchange", handleHashChange);
    return () => window.removeEventListener("hashchange", handleHashChange);
  }, []);
  const [managedError, setManagedError] = useState<string>();
  const [managedLiveError, setManagedLiveError] = useState<string>();
  const [managedPending, setManagedPending] = useState(false);
  const [managedHeaderModels, setManagedHeaderModels] = useState<ModelSummary[]>([]);
  const [managedHeaderModelsLoading, setManagedHeaderModelsLoading] = useState(false);
  const [managedHeaderModelError, setManagedHeaderModelError] = useState<string>();
  const [managedRecentFolders, setManagedRecentFolders] = useState(() => loadManagedRecentFolders(storage));

  const extractManagedFolder = (agent: { cwd?: string; worktree?: boolean; worktree_base_path?: string } | undefined): string | undefined => {
    if (!agent) return undefined;
    return agent.worktree ? (agent.worktree_base_path?.trim() ? agent.worktree_base_path : undefined) : agent.cwd;
  };

  const distinctRecentFolders = useMemo(() => {
    const knownManagedFolders = (session.state.managedAgents ?? []).map(extractManagedFolder).filter((cwd): cwd is string => Boolean(cwd));
    const peerManagedFolders = session.state.agents.map((agent) => extractManagedFolder(agent.managed_agent)).filter((cwd): cwd is string => Boolean(cwd));
    return normalizeManagedRecentFolders([...managedRecentFolders, ...knownManagedFolders, ...peerManagedFolders]);
  }, [managedRecentFolders, session.state.managedAgents, session.state.agents]);

  useEffect(() => {
    const knownManagedFolders = (session.state.managedAgents ?? []).map(extractManagedFolder).filter((cwd): cwd is string => Boolean(cwd));
    const peerManagedFolders = session.state.agents.map((agent) => extractManagedFolder(agent.managed_agent)).filter((cwd): cwd is string => Boolean(cwd));
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
  const [managedGoalState, setManagedGoalState] = useState<GoalState>();
  const [managedInlineToolsEnabled, setManagedInlineToolsEnabled] = useState<boolean>();
  const [journalAgentId, setJournalAgentId] = useState<string | null>(null);
  const [progressModalAgentId, setProgressModalAgentId] = useState<string | null>(null);
  const [goalError, setGoalError] = useState<string>();
  const [reminderStatus, setReminderStatus] = useState<string>();
  const [goalControlAvailable, setGoalControlAvailable] = useState(false);
  const [inlineToolControlAvailable, setInlineToolControlAvailable] = useState(false);
  const [inlineToolError, setInlineToolError] = useState<string>();
  const [templatesLoaded, setTemplatesLoaded] = useState(false);
  const [promptImportProgress, setPromptImportProgress] = useState<PromptTemplateImportProgress>();
  const [templateUsage, setTemplateUsage] = useState<Record<string, number>>(() => loadPromptTemplateUsage(storage));
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

  const reconcileConversationState = useCallback((peer: string, messages: HistoryMessage[], highWaterSequence: number): void => {
    session.dispatch({ type: "history/reconcile", scope, peer, snapshot: messages, highWaterSequence });
  }, [scope, session.dispatch]);
  // Background lookup map resolving workflow state IDs to human-readable state and workflow names.
  // Periodically synchronizes so agent rows and conversation headers display named workflow states even
  // when an agent projection only carries state IDs.
  const [sidebarTab, setSidebarTab] = useState<"agents" | "background">("agents");
  const [workflowStateMap, setWorkflowStateMap] = useState<Record<string, { state_name?: string | undefined; workflow_name?: string | undefined }>>({});

  useEffect(() => {
    if (sidebarTab !== "background" || !session.transport?.http?.workflowStates) return;
    let mounted = true;
    let generation = 0;
    const fetchWorkflowData = async () => {
      const request = ++generation;
      try {
        const [statesRes, workflowsRes] = await Promise.allSettled([
          session.transport.http.workflowStates(),
          session.transport.http.listWorkflows(),
        ]);
        if (!mounted || request !== generation || statesRes.status !== "fulfilled" || workflowsRes.status !== "fulfilled") return;
        const states = statesRes.value.workflow_states || [];
        const workflows = workflowsRes.value || [];
        const wfMap = new Map(workflows.map((w) => [w.id, w.name]));
        const mapping: Record<string, { state_name?: string | undefined; workflow_name?: string | undefined }> = {};
        for (const st of states) {
          mapping[st.id] = {
            state_name: st.name,
            workflow_name: st.workflow_id ? wfMap.get(st.workflow_id) : undefined,
          };
        }
        setWorkflowStateMap(mapping);
      } catch {
        // ignore errors
      }
    };
    void fetchWorkflowData();
    const interval = setInterval(() => {
      void fetchWorkflowData();
    }, 10000);
    return () => {
      mounted = false;
      clearInterval(interval);
    };
  }, [session.transport, sidebarTab]);

  const visibleAgents = useMemo(
    () => mergeAgentSources(session.state.agents, session.state.managedAgents ?? [], session.state.managedChildren, workflowStateMap)
      .filter((agent) => !(agent as { parent_agent_id?: string }).parent_agent_id && !(agent.managed_agent as { parent_agent_id?: string } | undefined)?.parent_agent_id),
    [session.state.agents, session.state.managedAgents, session.state.managedChildren, workflowStateMap]
  );
  // Saved selections from before source-qualified rows are legacy tmux IDs.
  const selectedSourceKey = selectedPeer === undefined ? undefined : selectedPeer.includes(":") ? selectedPeer : sourceAgentKey("tmux", selectedPeer);
  const selectedAgent = useMemo(() => visibleAgents.find((agent) => sourceAgentKey(agent.source, agent.agent_id) === selectedSourceKey), [visibleAgents, selectedSourceKey]);
  const selectedConversationPeer = selectedAgent === undefined ? undefined : selectedPeer === undefined ? undefined : selectedPeer.startsWith("tmux:") ? selectedPeer.slice("tmux:".length) : selectedPeer.startsWith("router-managed:") ? selectedPeer.slice("router-managed:".length) : selectedPeer;
  const conversation = useConversation(session.transport, scope, selectedConversationPeer, reconcileConversationState, session.uiLive, selectedConversationPeer === undefined ? undefined : selectedSourceKey);
  // Accepted rows are a view-local bridge until fresh history catches up. They
  // are never restored after switching away, even if a send resolves late.
  const viewKey = `${scope.routerUrl}\0${scope.agentId}\0${selectedPeer ?? ""}`;
  const viewGeneration = useRef({ key: viewKey, generation: 0 });
  if (viewGeneration.current.key !== viewKey) viewGeneration.current = { key: viewKey, generation: viewGeneration.current.generation + 1 };
  const [acceptedView, setAcceptedView] = useState<{ generation: number; messages: HistoryMessage[] }>({ generation: -1, messages: [] });
  const imageBudget = useRef(new InFlightImageBudget());
  const committedOutbox = useRef(session.state.messagesByPeer);
  useLayoutEffect(() => { committedOutbox.current = session.state.messagesByPeer; }, [session.state.messagesByPeer]);
  const releasedAfterCommit = useRef(new DeferredImageReleases());
  const [releaseRevision, setReleaseRevision] = useState(0);
  useLayoutEffect(() => {
    releasedAfterCommit.current.commit(imageBudget.current);
  }, [releaseRevision, session.state.messagesByPeer]);
  // Only IDs of active sends are tracked; this is not a completed-history cache.
  const sendsInFlight = useRef(new Map<string, { generation: number; confirmed: boolean }>());
  useLayoutEffect(() => {
    const canonical = new Set(conversation.messages.filter((message) => !message.pending).map((message) => canonicalSendId(message.id)));
    for (const [id, send] of sendsInFlight.current) if (send.generation === viewGeneration.current.generation && canonical.has(canonicalSendId(id))) send.confirmed = true;
    setAcceptedView((current) => {
      if (current.generation !== viewGeneration.current.generation) return current;
      const remaining = visibleAccepted(current.messages, conversation.messages.filter((message) => !message.pending));
      return remaining.length === current.messages.length ? current : { ...current, messages: remaining };
    });
  }, [conversation.messages]);
  useEffect(() => { setAcceptedView({ generation: viewGeneration.current.generation, messages: [] }); }, [viewKey]);
  useEffect(() => {
    const times = Object.values(session.state.messagesByPeer).flat().map((message) => Date.parse(message.created_at ?? message.createdAt ?? "")).filter(Number.isFinite);
    if (times.length === 0) return;
    const delay = Math.max(0, Math.min(...times) + 24 * 60 * 60 * 1000 - Date.now());
    const timer = window.setTimeout(() => session.dispatch({ type: "outbox/prune", scope, now: Date.now() }), delay);
    return () => window.clearTimeout(timer);
  }, [scope, session.dispatch, session.state.messagesByPeer]);

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
    setExpanded(new Set()); setDialog(undefined); setScratchpadOpen(false); setScratchpadAgentId(undefined); setProviderPrompt(undefined); setArrivalAgent(undefined); setManagedAgentID(undefined); setManagedError(undefined); setManagedLiveError(undefined); setManagedPending(false); setManagedGoalState(undefined); setManagedInlineToolsEnabled(undefined); setGoalError(undefined); setReminderStatus(undefined);
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
    if (selectedPeer === undefined) {
      session.dispatch({ type: "selection/set", scope });
      setSettings((current) => persistSelectedPeer(current, activeUrl, resolvedAgentId, ""));
    } else {
      session.dispatch({ type: "selection/set", scope, peer: selectedPeer });
      setSettings((current) => persistSelectedPeer(current, activeUrl, resolvedAgentId, selectedPeer));
    }
  }, [activeUrl, resolvedAgentId, selectedPeer, scope, session.dispatch]);

  useEffect(() => {
    if (selectedPeer === undefined && session.state.selectedPeer !== undefined) setSelectedPeer(session.state.selectedPeer);
  }, [selectedPeer, session.state.selectedPeer]);
  // Conversation messages are ephemeral and loaded on demand from the router; no local IndexedDB history reconcile on startup.
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

  const isManagedSelected = selectedSourceKey?.startsWith("router-managed:") || selectedAgent?.source === "router-managed" || selectedAgent?.managed_agent?.runtime === "embedded" || (selectedAgent as { runtime?: string })?.runtime === "embedded";
  const selectedLegacyPeer = isManagedSelected ? undefined : selectedAgent?.agent_id;
  const selectedActivity = useSelectedManagedActivity(session.transport.http, scope, isManagedSelected ? selectedConversationPeer : undefined, session.uiLive);
  const selectedManagedAgent = useMemo(() => managedAgentID === undefined ? undefined : (session.state.managedAgents ?? []).find((agent) => agent.agent_id === managedAgentID), [managedAgentID, session.state.managedAgents]);
  const terminateTargetAgent = useMemo(() => {
    if (managedAgentID) {
      const found = (session.state.managedAgents ?? []).find((agent) => agent.agent_id === managedAgentID);
      if (found) return found;
    }
    if (selectedAgent?.source === "router-managed") {
      const found = (session.state.managedAgents ?? []).find((agent) => agent.agent_id === selectedAgent.agent_id);
      if (found) return found;
      if (selectedAgent.managed_agent) return selectedAgent.managed_agent as ManagedAgent;
    }
    return selectedManagedAgent;
  }, [managedAgentID, selectedAgent, session.state.managedAgents, selectedManagedAgent]);

  useEffect(() => {
    if (selectedPeer === undefined) return;
    const agentsLoaded = reconciliation.ready || session.state.managedAvailable;
    if (agentsLoaded && selectedAgent === undefined) {
      setSelectedPeer(undefined);
      setSettings((current) => persistSelectedPeer(current, activeUrl, resolvedAgentId, ""));
      session.dispatch({ type: "selection/set", scope });
    }
  }, [selectedPeer, selectedAgent, reconciliation.ready, session.state.managedAvailable, activeUrl, resolvedAgentId, scope, session.dispatch]);

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
      setManagedHeaderModels(catalog.models.filter((model) => model.available).map(({ provider, id, name }) => ({ provider, id, name: name ?? id })));
    }).catch((cause: unknown) => {
      if (active) setManagedHeaderModelError(cause instanceof Error ? cause.message : "Models could not be loaded.");
    }).finally(() => { if (active) setManagedHeaderModelsLoading(false); });
    return () => { active = false; };
  }, [selectedAgent?.agent_id, selectedAgent?.source, session.transport.http]);


  const discardCombinedHistory = useCallback((ids: ReadonlySet<string>) => {
    conversation.discardBefore(ids);
    selectedActivity.discardBefore(ids);
  }, [conversation.discardBefore, selectedActivity.discardBefore]);

  const handleLoadOlder = useCallback(async (beforeCommit?: () => void) => {
    await Promise.all([
      conversation.hasOlder ? conversation.loadOlder(beforeCommit) : Promise.resolve(),
      selectedActivity.hasOlder ? selectedActivity.loadOlder(beforeCommit) : Promise.resolve(),
    ]);
  }, [conversation, selectedActivity]);
  const arrival = selectedLegacyPeer ? selectArrival(session.state, selectedLegacyPeer) : undefined;
  const setSelection = (peer: string): void => {
    setScratchpadOpen(false);
    setScratchpadAgentId(undefined);
    setSelectedPeer(peer);
    setManagedError(undefined);
    setManagedHeaderModelError(undefined);
    onSelectAgent?.(peer);
  };
  useEffect(() => {
    setManagedError(undefined);
    setManagedHeaderModelError(undefined);
  }, [selectedSourceKey]);
  const openScratchpadForSelectedAgent = (): void => {
    if (selectedAgent === undefined) return;
    setScratchpadAgentId(selectedAgent.agent_id);
    setScratchpadOpen(true);
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
    const sendGeneration = viewGeneration.current.generation;
    const localId = secureUuid();
    const payload = { id: localId, from: resolvedAgentId, to: peer, body, created_at: new Date().toISOString(), source: { kind: "web_ui" }, ...(images.length > 0 ? { attachments: images.map((image) => ({ type: "image", data_url: image.dataUrl, ...(image.name === undefined ? {} : { name: image.name }) })) } : {}) };
    const pending = pendingMessage(body, peer, resolvedAgentId, images.map((image) => ({ url: image.dataUrl, mime_type: image.mimeType, size_bytes: image.sizeBytes, ...(image.name === undefined ? {} : { name: image.name }) })), localId);
    if (sendsInFlight.current.size >= 150) {
      session.dispatch({ type: "message/append", scope, peer, message: pending });
      session.dispatch({ type: "send/settle", scope, peer, localId, ok: false });
      throw new Error("Too many sends in flight; this message was not sent.");
    }
    if (!imageBudget.current.reserve(localId, pending, committedOutbox.current)) {
      session.dispatch({ type: "outbox/reject-images", scope, peer, message: pending });
      return;
    }
    sendsInFlight.current.set(localId, { generation: sendGeneration, confirmed: false });
    session.dispatch({ type: "message/append", scope, peer, message: pending });
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      const operation = async () => {
        if (externalSend) await externalSend(peer, body, images);
        if (controller.signal.aborted) throw new Error("Send timed out; check history before resending.");
        const result = await conversation.send(payload, controller.signal);
        const accepted = responseMessage(result.conversation_message ?? result.message, activeUrl);
        if (accepted === undefined) throw new Error("Router returned an invalid message response");
        return accepted;
      };
      const accepted = await Promise.race([operation(), new Promise<never>((_, reject) => { timer = setTimeout(() => { controller.abort(); reject(new Error("Send timed out; check history before resending.")); }, 30_000); })]);
      if (canBridgeAccepted(sendGeneration, viewGeneration.current.generation, sendsInFlight.current.get(localId)?.confirmed === true)) {
        const generation = viewGeneration.current.generation;
        setAcceptedView((current) => ({ generation, messages: mergeMessages(current.generation === generation ? current.messages : [], [accepted]).slice(-150) }));
      }
      session.dispatch({ type: "send/replace", scope, peer, localId, message: accepted });
      void cacheAcceptedImages(peer, scope, accepted, pending);
      if (viewGeneration.current.generation === sendGeneration) { setManagedError(undefined); setManagedHeaderModelError(undefined); }
    } catch (cause) {
      session.dispatch({ type: "send/settle", scope, peer, localId, ok: false });
      throw cause;
    } finally {
      if (timer !== undefined) clearTimeout(timer);
      sendsInFlight.current.delete(localId);
      releasedAfterCommit.current.defer(localId);
      setReleaseRevision((revision) => revision + 1);
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
  const saveTemplate = async (template: PromptTemplate): Promise<void> => {
    const isExisting = Boolean(template.id && session.state.templates.some((t) => t.id === template.id));
    if (isExisting && template.id) {
      await session.transport.http.updatePromptTemplate(
        template.id,
        promptTemplateUpdatePayload(template)
      );
    } else {
      await session.transport.http.createPromptTemplate(
        promptTemplateCreatePayload(template)
      );
    }
    invalidateTemplateList();
    await refreshTemplates();
  };
  const deleteTemplate = async (id: string): Promise<void> => { await session.transport.http.deletePromptTemplate(id); invalidateTemplateList(); await refreshTemplates(); };
  const importTemplates = async (items: PromptTemplate[], overwrite: boolean): Promise<PromptTemplateImportResult> => {
    setPromptImportProgress({ completed: 0, total: items.length, interrupted: false });
    const result = await runPromptTemplateImport(items, overwrite, {
      create: async (template) => { await session.transport.http.createPromptTemplate(promptTemplateCreatePayload(template)); },
      update: async (id, template) => { await session.transport.http.updatePromptTemplate(id, promptTemplateUpdatePayload(template)); invalidateTemplateList(); },
      refresh: async () => { await refreshTemplates(); },
    }, setPromptImportProgress, { existingTemplates: sessionStateRef.current.templates });
    setPromptImportProgress(undefined);
    return result;
  };
  const markTemplateUsed = (template: PromptTemplate): void => {
    setTemplateUsage((current) => recordPromptTemplateUsage(template, current, Date.now(), storage));
  };
  const useReminderTemplate = (template: PromptTemplate, selection: { start: number; end: number }): void => { markTemplateUsed(template); setReminderTemplateToUse(template); setTemplateToUse(undefined); setReminderSelection(selection); setPromptReturnDialog("reminders"); setDialog("prompts"); };
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
  const modelOptions = selectedAgent?.source === "router-managed" ? managedHeaderModels.filter(model => sameModelEngine(selectedAgent.managed_agent?.model ?? selectedAgent.current_model, model)) : selectModels(selectedAgent);
  const selectedModel = normalizeModelSummary(selectedAgent?.managed_agent?.model ?? selectedAgent?.current_model);
  const pendingModelControl = [...session.state.pendingControls.values()].some((pending) => pending.action === "model" && pending.peer === selectedAgent?.agent_id && pending.instanceId === selectedAgent?.instance_id);
  const pendingManagedModelChange = selectedAgent?.source === "router-managed" && managedPending;
  const pendingGoalControl = [...session.state.pendingControls.values()].some((pending) => pending.action === "goal" && pending.peer === selectedAgent?.agent_id && pending.instanceId === selectedAgent?.instance_id);
  const pendingInlineToolControl = [...session.state.pendingControls.values()].some((pending) => pending.action === "inline-tool" && pending.peer === selectedAgent?.agent_id && pending.instanceId === selectedAgent?.instance_id);
  const selectedAgentActiveReminderCount = !selectedAgent ? 0 : activeRemindersForPeer(session.state.reminders, selectedAgent.agent_id).length;
  const pendingReminderIds = new Set([...session.state.pendingControls.values()].flatMap((pending) => pending.action === "reminder-lock" && pending.peer === selectedLegacyPeer && pending.reminderId !== undefined ? [pending.reminderId] : []));
  const arrivalForDialog = arrivalAgent ? session.state.arrivals.get(arrivalAgent) : undefined;
  const sortedPromptTemplates = useMemo(() => sortPromptTemplatesByMRU(session.state.templates, templateUsage), [session.state.templates, templateUsage]);
  const refreshManaged = async (): Promise<void> => {
    const snapshot = await session.transport.http.managedAgents(true);
    session.dispatch({ type: "managed/snapshot", scope, version: snapshot.projection_version, agents: snapshot.agents });
    if (snapshot.children !== undefined) session.dispatch({ type: "managed/children", scope, children: snapshot.children, ...(snapshot.children_version === undefined ? {} : { version: snapshot.children_version }) });
  };
  const changeManagedModel = async (model: ModelSummary): Promise<void> => {
    if (selectedAgent?.source !== "router-managed" || selectedAgent.managed_agent?.accepting_prompts !== true) return;
    if (!sameModelEngine(selectedAgent.managed_agent.model, model)) return;
    setManagedPending(true); setManagedError(undefined);
    try {
      await session.transport.http.restartManaged(selectedAgent.agent_id, { provider: model.provider, id: model.id });
      await refreshManaged();
      setManagedError(undefined);
    } catch (cause) {
      setManagedError(managedRequestErrorMessage(cause, "Managed model could not be changed."));
    } finally { setManagedPending(false); }
  };
  const launchManaged = async (input: { name: string; cwd: string; runtime?: "rpc" | "embedded"; model?: { provider: string; id: string }; initial_instruction?: string }): Promise<void> => {
    setManagedPending(true); setManagedError(undefined);
    try {
      const launched = await session.transport.http.launchManaged(input);
      const agentId = launched.agent?.agent_id ?? input.name;
      const folder = extractManagedFolder(launched.agent) ?? input.cwd;
      setManagedRecentFolders((current) => {
        const next = recordManagedRecentFolder(current, folder);
        saveManagedRecentFolders(storage, next);
        return next;
      });
      await refreshManaged();
      setSelection(sourceAgentKey("router-managed", agentId));
      setDialog(undefined);
    }
    catch (cause) { setManagedError(managedRequestErrorMessage(cause, "Managed agent could not be launched.")); }
    finally { setManagedPending(false); }
  };
  const manageAction = async (agentId: string, action: "restart" | "reset" | "terminate", options?: { cleanupWorktree?: boolean }): Promise<void> => {
    setManagedPending(true); setManagedError(undefined);
    try {
      if (action === "restart") {
        await session.transport.http.restartManaged(agentId);
      } else if (action === "reset") {
        await session.transport.http.resetManaged(agentId);
      } else {
        await session.transport.http.terminateManaged(agentId, options);
        if (selectedAgent?.agent_id === agentId || selectedConversationPeer === agentId) {
          setSelectedPeer(undefined);
          setSettings((current) => persistSelectedPeer(current, activeUrl, resolvedAgentId, ""));
          session.dispatch({ type: "selection/set", scope });
        }
        setDialog(undefined);
      }
      await refreshManaged();
    }
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
      setManagedGoalState(result.goal_state); setDialog(undefined); setManagedError(undefined);
    }).catch((cause: unknown) => {
      setGoalError(managedRequestErrorMessage(cause, "Managed goal could not be saved."));
    }).finally(() => setManagedPending(false));
  };
  const openManagedInlineTools = (): void => {
    const targetAgentId = selectedAgent?.source === "router-managed" ? selectedAgent.agent_id : selectedManagedAgent?.agent_id;
    if (targetAgentId === undefined) return;
    if (selectedManagedAgent === undefined) {
      setManagedAgentID(targetAgentId);
    }
    setManagedPending(true); setManagedError(undefined); setInlineToolError(undefined);
    void session.transport.http.managedInlineTools(targetAgentId).then((result) => {
      setManagedInlineToolsEnabled(result.enabled); setDialog("inline-tools");
    }).catch((cause: unknown) => {
      setManagedError(managedRequestErrorMessage(cause, "Managed inline-tool settings could not be loaded."));
    }).finally(() => setManagedPending(false));
  };
  const saveManagedInlineTools = (enabled: boolean): void => {
    if (selectedManagedAgent === undefined) return;
    setManagedPending(true); setInlineToolError(undefined);
    void session.transport.http.saveManagedInlineTools(selectedManagedAgent.agent_id, enabled).then((result) => {
      setManagedInlineToolsEnabled(result.enabled); setDialog(undefined); setManagedError(undefined);
      void session.resync().catch(() => undefined);
    }).catch((cause: unknown) => {
      setInlineToolError(managedRequestErrorMessage(cause, "Managed inline-tool settings could not be saved."));
    }).finally(() => setManagedPending(false));
  };
  const openManagedReminders = (agentId?: string): void => {
    const targetAgentId = agentId ?? (selectedAgent?.source === "router-managed" ? selectedAgent.agent_id : selectedManagedAgent?.agent_id);
    if (targetAgentId === undefined) return;
    setManagedAgentID(targetAgentId);
    setReminderDialogState({ mode: "list", subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false });
    setReminderStatus(undefined); setDialog("reminders");
  };
  const restartRouter = (options?: { update?: boolean }): void => {
    void session.transport.http.restartRouter(options).catch((cause: unknown) => {
      console.warn("restart router triggered:", cause);
    });
  };
  const handleStopSubagent = async (agentId: string, childId: string): Promise<void> => {
    const existing = session.state.managedChildren[agentId];
    if (existing) {
      const updatedChildren = existing.children.map((child) =>
        child.id === childId ? { ...child, status: "stopped" as const } : child
      );
      session.dispatch({
        type: "managed/children",
        scope,
        children: [{ ...existing, children: updatedChildren }],
      });
    }
    try {
      await session.transport.http.stopManagedSubagent(agentId, childId);
    } catch (cause) {
      console.error("Failed to stop managed subagent", cause);
    } finally {
      await refreshManaged();
    }
  };
  const toggleSubagents = (id: string): void => {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  const handleWorktreeCheck = useCallback((path: string, options?: { includeStatus?: boolean }) => session.transport.http.worktreeCheck(path, options), [session.transport.http]);

  const isConnected = session.state.connection === "connected";

  if (currentRoute === "workflows") {
    return <>
      <div className="workflows-spa-container" style={{ width: "100%", height: "100%", display: "flex", flexDirection: "column" }}>
        <WorkflowsPage
          client={session.transport.http}
          onBack={() => { window.location.hash = ""; }}
        />
      </div>
    </>;
  }

  return <>
    <AppShell
      sidebar={<Sidebar sidebarTab={sidebarTab} onSidebarTabChange={setSidebarTab} scope={scope} client={session.transport.http} storage={storage} routerLabel={routerLabel} connection={session.state.connection} connectionIdentity={resolvedAgentId} connectionDetail={reconciliation.error?.message} agents={isConnected ? visibleAgents : []} {...(selectedSourceKey === undefined ? {} : { selectedAgentId: selectedSourceKey })} filter={session.state.filter} arrivals={session.state.arrivals} reminderCounts={Object.fromEntries(session.state.agents.map((agent) => [agent.agent_id, activeRemindersForPeer(session.state.reminders, agent.agent_id).length]))} expandedAgentIds={expanded} onStopSubagent={handleStopSubagent} onRefresh={() => { void reconciliation.refresh(); void refreshManaged().catch(() => undefined); void session.resync().catch(() => undefined); onRefresh?.(); }} onRouterClick={() => { setDialog("routers"); onRouterClick?.(); }} onFilterChange={(filter) => { session.dispatch({ type: "filter/set", scope, filter }); onFilterChange?.(filter); }} onSelectAgent={setSelection} onGoal={openGoalForAgent} onReminders={openRemindersForAgent} onToggleSubagents={(id) => { toggleSubagents(id); onToggleSubagents?.(id); }} onOpenArrival={(item) => { setArrivalAgent(item.agent); setDialog("arrival"); onOpenArrival?.(item.agent); }} onDismissArrival={(id) => { session.dispatch({ type: "arrival/remove", scope, agent: id }); onDismissArrival?.(id); }} onOpenProgressHistory={(id) => setProgressModalAgentId(id)} onLaunchBackgroundWorkflow={() => setDialog("run-background-workflow")} onWorkflowSchedules={() => setDialog("workflow-schedules")} onBroadcastMessage={() => setDialog("broadcast-message")} {...(session.state.managedAvailable ? { onLaunchManaged: () => { setManagedAgentID(undefined); setManagedError(undefined); setDialog("managed"); }, onRestartRouter: restartRouter, onProviderSettings: () => { setManagedError(undefined); setDialog("provider-settings"); }, onMCPServers: () => { setDialog("mcp-servers"); }, onToolDefaults: () => { setDialog("tool-defaults"); }, onKnowledgeBase: () => { setDialog("knowledge-base"); }, onWorkflows: () => { window.location.hash = "#/workflows"; }, onSystemOneConfig: () => { setDialog("systemone-config"); }, onContextThresholds: () => { setDialog("context-thresholds"); } } : { onRestartRouter: restartRouter, onMCPServers: () => { setDialog("mcp-servers"); }, onToolDefaults: () => { setDialog("tool-defaults"); }, onKnowledgeBase: () => { setDialog("knowledge-base"); }, onWorkflows: () => { window.location.hash = "#/workflows"; }, onSystemOneConfig: () => { setDialog("systemone-config"); }, onContextThresholds: () => { setDialog("context-thresholds"); } })} />}
      conversation={isConnected ? <div className="conversation-runtime">{session.state.outboxNotice && <div role="alert">{session.state.outboxNotice} <button type="button" onClick={() => session.dispatch({ type: "outbox/dismiss", scope })}>Dismiss</button></div>}<Conversation onDiscardHistoryBefore={isManagedSelected ? discardCombinedHistory : undefined} client={session.transport.http} templates={session.state.templates} {...(selectedAgent ? { peer: selectedAgent } : {})} activeReminderCount={selectedAgentActiveReminderCount} ownerId={resolvedAgentId} routerUrl={activeUrl} timelineEvents={conversation.timelineEvents} liveTimelineIds={conversation.liveTimelineIds} historyWindowReady={!conversation.loading && (!isManagedSelected || selectedActivity.ready)} messages={selectedAgent === undefined ? [] : reconcileHistoryMessages(mergeMessages(selectMessages(session.state, selectedConversationPeer), acceptedView.generation === viewGeneration.current.generation ? visibleAccepted(acceptedView.messages, conversation.messages) : []), conversation.messages, 0).map((message) => (message.attachments === undefined ? message : { ...message, attachments: message.attachments.map((attachment, index) => ({ ...attachment, cacheKey: attachment.cacheKey ?? cacheKeyForAttachment(scope.routerUrl, scope.agentId, selectedConversationPeer ?? "", canonicalSendId(message.id), attachmentCacheIdentity(attachment, index)) })) }))} {...(isManagedSelected && selectedAgent ? { conversationView: "complete" as const, activityEntries: selectedActivity.entries, liveActivityIds: selectedActivity.liveIds, activityHistoryReady: selectedActivity.ready && !conversation.loading } : {})} composeEnabled={selectedConversationPeer !== undefined && (selectedAgent?.source !== "router-managed" || selectedAgent.managed_agent?.accepting_prompts === true)} showAgentControls={selectedLegacyPeer !== undefined} showGoalControl={selectedLegacyPeer !== undefined || selectedAgent?.source === "router-managed"} value={composerDraft} onChange={setComposerDraft} onSelectionChange={setComposerSelection} {...(selectedAgent && (selectedLegacyPeer !== undefined || selectedAgent.source === "router-managed") ? { modelControls: <ModelPicker models={modelOptions} {...(selectedModel ? { currentModel: selectedModel } : {})} isEmbedded={selectedAgent.runtime === "embedded" || selectedAgent.managed_agent?.runtime === "embedded"} pending={selectedAgent.source === "router-managed" ? pendingManagedModelChange : pendingModelControl} loading={selectedAgent.source === "router-managed" && managedHeaderModelsLoading} {...(selectedAgent.source === "router-managed" && managedHeaderModelError ? { emptyMessage: managedHeaderModelError } : {})} disabled={selectedAgent.source === "router-managed" ? selectedAgent.managed_agent?.accepting_prompts !== true || pendingManagedModelChange : session.state.connection !== "connected" || pendingModelControl} onChange={selectedAgent.source === "router-managed" ? changeManagedModel : chooseModel} /> } : {})} {...(selectedAgent ? { promptControls: <PromptControls templates={sortedPromptTemplates} usage={templateUsage} loaded={templatesLoaded} disabled={selectedAgent.source === "router-managed" ? selectedAgent.managed_agent?.accepting_prompts !== true : session.state.connection !== "connected"} onManage={() => setDialog("prompts")} onUse={(template) => { markTemplateUsed(template); setTemplateToUse(template); setDialog("prompts"); }} /> } : {})} loading={conversation.loading} {...((selectedActivity.error || conversation.error) ? { onRetryHistory: () => { if (selectedActivity.error) void selectedActivity.loadOlder(); if (conversation.error) void conversation.refresh(); } } : {})} {...((selectedActivity.error ?? conversation.error) ? { error: (selectedActivity.error ?? conversation.error)?.message } : {})} {...(selectedAgent && dialog === undefined && managedError ? { agentError: managedError, onDismissError: () => { setManagedError(undefined); } } : {})} {...(arrival ? { arrival } : {})} onSend={(body, images) => { if (!selectedConversationPeer) return; const retained = new Set(Object.values(committedOutbox.current).flat().map((message) => message.id)); for (const id of sendsInFlight.current.keys()) retained.add(id); if (retained.size >= 150) throw new Error("Outbox full. Copy or resolve existing messages before sending."); void send(selectedConversationPeer, body, images).catch(() => undefined); }} onAction={(action) => {
  if (selectedAgent?.agent_id) void manageAction(selectedAgent.agent_id, action);
}} onConfirmTermination={(confirm) => {
  if (confirm) {
    const targetAgentId = selectedAgent?.source === "router-managed" ? selectedAgent.agent_id : selectedManagedAgent?.agent_id;
    if (targetAgentId !== undefined) {
      setManagedAgentID(targetAgentId);
    }
    setDialog("terminate-confirmation");
  }
}} onScratchpad={openScratchpadForSelectedAgent} onContextThresholds={() => setDialog("context-thresholds")} onGoal={() => { if (selectedLegacyPeer) { setDialog("goal"); externalGoal?.(selectedLegacyPeer); } else if (selectedAgent?.source === "router-managed") { setManagedAgentID(selectedAgent.agent_id); loadManagedGoal(selectedAgent.agent_id); } }} onReminders={() => { if (selectedLegacyPeer) { setReminderDialogState({ mode: "list", subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false }); setDialog("reminders"); externalReminders?.(selectedLegacyPeer); } else if (selectedAgent?.source === "router-managed") { openManagedReminders(); } }} onInlineToolSettings={() => {
  if (selectedLegacyPeer) {
    openInlineToolSettingsForAgent(selectedLegacyPeer);
  } else if (selectedAgent?.source === "router-managed") {
    openManagedInlineTools();
  }
}} onOpenArrival={(item) => { setArrivalAgent(item.agent); setDialog("arrival"); onOpenArrival?.(item.agent); }} onOpenWorkflow={() => { window.location.hash = "#/workflows"; }} onOpenWorkflowJournal={setJournalAgentId} onDismissArrival={(id) => session.dispatch({ type: "arrival/remove", scope, agent: id })} onToggleSubagents={() => { if (selectedAgent) { toggleSubagents(selectedAgent.agent_id); onToggleSubagents?.(selectedAgent.agent_id); } }} expanded={selectedAgent !== undefined && (expanded.has(selectedAgent.agent_id) || (selectedSourceKey !== undefined && expanded.has(selectedSourceKey)))} onStopSubagent={selectedAgent ? (childId: string) => void handleStopSubagent(selectedAgent.agent_id, childId) : undefined} onOpenProgressHistory={(id) => setProgressModalAgentId(id)} onLoadOlder={handleLoadOlder} hasOlder={conversation.hasOlder || selectedActivity.hasOlder} loadingOlder={conversation.loadingOlder || selectedActivity.loadingOlder} /></div> : <div className="conversation-disconnected"><div className="connection-overlay-card"><img src="/mecha-watermark.png" alt="" className={`mecha-watermark${session.state.connection === "reconnecting" ? " reconnecting" : ""}`} aria-hidden="true" /><div className="connection-overlay-indicator">{session.state.connection === "reconnecting" ? <span className="reconnecting-pulse-dot" aria-hidden="true" /> : session.state.connection === "connecting" ? <span className="connection-overlay-spinner" aria-hidden="true" /> : <span className="connection-overlay-dot error" aria-hidden="true" />}<h3>{session.state.connection === "reconnecting" ? "Reconnecting..." : session.state.connection === "connecting" ? `Connecting to ${routerLabel || "router"}...` : `Unable to connect to router at ${activeUrl}`}</h3></div><p className="connection-overlay-subtitle">{session.state.connection === "reconnecting" ? (activeUrl ? `Attempting to reconnect to ${routerLabel || activeUrl}` : undefined) : session.state.connection === "connecting" ? activeUrl : (reconciliation.error?.message || "Check that the router is running, or select another router above.")}</p></div></div>}
    />
    {scratchpadAgentId !== undefined ? <ScratchpadViewer open={scratchpadOpen} agentId={scratchpadAgentId} client={session.transport.http} onClose={() => setScratchpadOpen(false)} onClosed={() => setScratchpadAgentId(undefined)} /> : null}
    <RouterManagerDialog open={dialog === "routers"} routers={settings.routers.map((url) => ({ url, ...(settings.aliases[url] ? { alias: settings.aliases[url] } : {}), agentId: settings.agentIds[url] ?? (url === activeUrl ? resolvedAgentId : requestedAgentId ?? DEFAULT_AGENT_ID) }))} activeUrl={activeUrl} defaultAgentId={requestedAgentId ?? DEFAULT_AGENT_ID} onClose={() => setDialog(undefined)} onSave={saveRouter} onSelect={selectRouter} onRemove={removeRouter} onProviderSettings={() => setDialog("provider-settings")} onMCPServers={() => setDialog("mcp-servers")} onToolDefaults={() => setDialog("tool-defaults")} />
    <ProviderAuthDialog open={dialog === "provider-settings"} client={session.transport.http} refreshKey={providerRefresh} {...(providerPrompt ? { prompt: providerPrompt } : {})} onClose={() => setDialog(undefined)} onPromptHandled={() => setProviderPrompt(undefined)} />
    <MCPServersDialog open={dialog === "mcp-servers"} client={session.transport.http} onClose={() => setDialog(undefined)} />
    <ToolDefaultsDialog open={dialog === "tool-defaults"} client={session.transport.http} onClose={() => setDialog(undefined)} />
    <SystemOneConfigDialog open={dialog === "systemone-config"} client={session.transport.http} onClose={() => setDialog(undefined)} />
    <KnowledgeBaseDialog open={dialog === "knowledge-base"} client={session.transport.http} onClose={() => setDialog(undefined)} />
    <WorkflowSchedulesModal
      open={dialog === "workflow-schedules"}
      client={session.transport.http}
      recentFolders={distinctRecentFolders}
      onListDirectories={(path) => session.transport.http.managedDirectories(path)}
      onClose={() => setDialog(undefined)}
    />
    <WorkflowBackgroundRunModal
      open={dialog === "run-background-workflow"}
      client={session.transport.http}
      recentFolders={distinctRecentFolders}
      onListDirectories={(path) => session.transport.http.managedDirectories(path)}
      onClose={() => setDialog(undefined)}
      onLaunched={(agent) => {
        setDialog(undefined);
        if (agent.cwd) {
          setManagedRecentFolders((current) => {
            const next = recordManagedRecentFolder(current, agent.cwd);
            saveManagedRecentFolders(storage, next);
            return next;
          });
        }
        void reconciliation.refresh();
        void refreshManaged().catch(() => undefined);
        if (agent.agent_id) {
          setSelection(agent.agent_id);
        }
      }}
    />
    <ContextThresholdDialog open={dialog === "context-thresholds"} client={session.transport.http} onClose={() => setDialog(undefined)} />
    {selectedLegacyPeer && selectedAgent ? <InlineToolSettingsDialog open={dialog === "inline-tools"} peer={selectedLegacyPeer} enabled={selectedAgent.inline_tool_enabled === true} capabilityAvailable={inlineToolControlAvailable && selectedAgent.inline_tool_control_available === true} pending={pendingInlineToolControl} {...(inlineToolError ? { error: inlineToolError } : {})} onClose={() => { setDialog(undefined); setInlineToolError(undefined); }} onSave={saveInlineToolSettings} /> : selectedManagedAgent ? <InlineToolSettingsDialog open={dialog === "inline-tools"} peer={selectedManagedAgent.agent_id} enabled={managedInlineToolsEnabled === true} capabilityAvailable={selectedManagedAgent.accepting_prompts} pending={managedPending} {...(inlineToolError ? { error: inlineToolError } : {})} onClose={() => { setDialog(undefined); setInlineToolError(undefined); }} onSave={saveManagedInlineTools} /> : null}
    {selectedLegacyPeer && selectedAgent ? <GoalEditorDialog open={dialog === "goal"} peer={selectedLegacyPeer} {...(selectedAgent.goal_state ? { goalState: selectedAgent.goal_state } : {})} capabilityAvailable={goalControlAvailable && session.transport.goalControlAvailable} pending={pendingGoalControl} {...(goalError ? { error: goalError } : {})} onClose={() => { setDialog(undefined); }} onSave={saveGoal} /> : selectedManagedAgent ? <GoalEditorDialog open={dialog === "goal"} peer={selectedManagedAgent.agent_id} goalState={managedGoalState ?? DEFAULT_GOAL_STATE} capabilityAvailable={selectedManagedAgent.accepting_prompts} pending={managedPending} {...(goalError ? { error: goalError } : {})} onClose={() => { setDialog(undefined); }} onSave={saveManagedGoal} /> : null}
    {selectedLegacyPeer ? <ReminderDialog open={dialog === "reminders"} peer={selectedLegacyPeer} creator={resolvedAgentId} reminders={session.state.reminders} locks={Object.fromEntries(session.state.reminderLocks)} pendingIds={pendingReminderIds} templates={session.state.templates} templatesLoaded={templatesLoaded} dialogState={reminderDialogState} onDialogStateChange={setReminderDialogState} onUseTemplate={useReminderTemplate} onManageTemplates={() => { setReminderTemplateToUse(undefined); setPromptReturnDialog("reminders"); setDialog("prompts"); }} {...(reminderStatus ? { status: reminderStatus } : {})} onClose={() => { setDialog(undefined); setReminderDialogState({ mode: "list", subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false }); }} onCreate={(payload, lock) => createReminder(payload, lock)} onUpdate={(id, payload) => updateReminder(id, payload)} onDelete={(id) => deleteReminder(id)} onLock={lockReminder} /> : selectedManagedAgent ? <ReminderDialog open={dialog === "reminders"} peer={selectedManagedAgent.agent_id} creator={resolvedAgentId} reminders={session.state.reminders} locks={{}} pendingIds={new Set()} templates={session.state.templates} templatesLoaded={templatesLoaded} dialogState={reminderDialogState} onDialogStateChange={setReminderDialogState} onUseTemplate={useReminderTemplate} onManageTemplates={() => { setReminderTemplateToUse(undefined); setPromptReturnDialog("reminders"); setDialog("prompts"); }} {...(reminderStatus ? { status: reminderStatus } : {})} onClose={() => { setDialog(undefined); setReminderDialogState({ mode: "list", subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false }); }} onCreate={(payload, lock) => createReminder(payload, lock)} onUpdate={(id, payload) => updateReminder(id, payload)} onDelete={(id) => deleteReminder(id)} onLock={() => setReminderStatus("Reminder locking is not available for managed agents.")} /> : null}
    <PromptTemplateDialog client={session.transport.http} open={dialog === "prompts"} templates={sortedPromptTemplates} loaded={templatesLoaded} {...(promptImportProgress === undefined ? {} : { importProgress: promptImportProgress })} {...(reminderTemplateToUse ? { templateToUse: reminderTemplateToUse } : templateToUse ? { templateToUse } : {})} {...(reminderTemplateToUse && reminderSelection ? { selection: reminderSelection } : !reminderTemplateToUse && composerSelection ? { selection: composerSelection } : {})} onClose={closePromptTemplateDialog} onClosed={reopenDialogAfterPromptClose} onSave={(template) => saveTemplate(template)} onDelete={(id) => deleteTemplate(id)} onImport={(items, overwrite) => importTemplates(items, overwrite)} onReloadTemplates={refreshTemplates} onInsert={(value, selection) => { if (reminderTemplateToUse) { markTemplateUsed(reminderTemplateToUse); insertReminderTemplate(value, selection); } else if (templateToUse) { markTemplateUsed(templateToUse); insertTemplate(value, selection); } else { insertTemplate(value, selection); } }} />
    <ArrivalDialog open={dialog === "arrival"} {...(arrivalForDialog ? { arrival: arrivalForDialog } : {})} uiAgentId={resolvedAgentId} onClose={() => setDialog(undefined)} onDismiss={(id) => session.dispatch({ type: "arrival/remove", scope, agent: id })} />
    {terminateTargetAgent ? <TerminateConfirmationDialog open={dialog === "terminate-confirmation"} agent={terminateTargetAgent} pending={managedPending} {...(managedError ? { error: managedError } : {})} onClose={() => { setDialog(undefined); setManagedError(undefined); }} onConfirm={(agentId, options) => manageAction(agentId, "terminate", options)} onWorktreeCheck={(path: string) => session.transport.http.worktreeCheck(path)} /> : null}
    <BroadcastMessageDialog open={dialog === "broadcast-message"} client={session.transport.http} currentAgentId={resolvedAgentId} agents={visibleAgents} onClose={() => setDialog(undefined)} />
    <ManagedAgentDialog open={dialog === "managed"} recentFolders={distinctRecentFolders} pending={managedPending} {...(managedError ? { error: managedError } : {})} onClose={() => { setDialog(undefined); setManagedError(undefined); }} onLaunch={launchManaged} onAction={manageAction} onListDirectories={(path) => session.transport.http.managedDirectories(path)} onModels={session.transport.http.models} onWorktreeCheck={handleWorktreeCheck} />
    {journalAgentId && <WorkflowJournalModal agentId={journalAgentId} client={session.transport.http} onClose={() => setJournalAgentId(null)} />}
    {progressModalAgentId && <ProgressHistoryModal agentId={progressModalAgentId} client={session.transport.http} onClose={() => setProgressModalAgentId(null)} />}
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
export { ErrorBanner } from "./components/ErrorBanner";
export { ContextThresholdDialog } from "./components/ContextThresholdDialog";
export { MCPServersDialog } from "./components/MCPServersDialog";
export { ToolDefaultsDialog } from "./components/ToolDefaultsDialog";
export { WorkerToolsDialog } from "./components/WorkerToolsDialog";
export { WorkflowsPage } from "./components/WorkflowsPage";
export { WorkflowSchedulesModal } from "./components/WorkflowSchedulesModal";
export { WorkflowStatesPage } from "./components/WorkflowStatesPage";
export { BroadcastMessageDialog } from "./components/BroadcastMessageDialog";
