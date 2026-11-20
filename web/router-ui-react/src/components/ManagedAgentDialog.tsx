import { useEffect, useRef, useState, type FormEvent, type ReactElement } from "react";
import type { ManagedAgent, ManagedAgentLaunchPayload, ModelSummary } from "../protocol/types";
import type { ModelsResponse } from "../transport/http-client";
import { DialogShell } from "./DialogShell";
import { ManagedDirectoryPicker } from "./ManagedDirectoryPicker";
import { ModelPicker } from "./ModelPicker";

export function normalizeManagedAgentName(name: string): string { return name.trim().replace(/\s+/g, "-"); }

export interface ManagedAgentDialogProps {
  open: boolean;
  agent?: ManagedAgent;
  pending?: boolean;
  error?: string;
  recentFolders?: readonly string[];
  onClose(): void;
  onLaunch(input: ManagedAgentLaunchPayload): Promise<void>;
  onAction(agentId: string, action: "restart" | "reset" | "terminate", options?: { cleanupWorktree?: boolean }): Promise<void>;
  onListDirectories(path: string): Promise<{ path: string; parent: string; directories: Array<{ name: string; path: string }> }>;
  onModels?(): Promise<ModelsResponse>;
  onWorktreeCheck?(path: string, options?: { includeStatus?: boolean }): Promise<{ is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean }>;
}

export function isGoogleProvider(provider: string | undefined): boolean {
  return provider === "google" || provider === "google-vertex";
}

// Bounded session cache for worktree check results (Scenario WTL-004).
// Adheres to repository bounded retention policy: capped at 50 entries with LRU/FIFO eviction.
const MAX_WORKTREE_CACHE_SIZE = 50;
const worktreeCheckCache = new Map<string, { is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean }>();

function setCachedWorktreeCheck(path: string, result: { is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean }): void {
  // If entry exists, delete before setting to refresh LRU order
  if (worktreeCheckCache.has(path)) {
    worktreeCheckCache.delete(path);
  } else if (worktreeCheckCache.size >= MAX_WORKTREE_CACHE_SIZE) {
    // Evict oldest entry (FIFO / least recently set)
    const oldestKey = worktreeCheckCache.keys().next().value;
    if (oldestKey !== undefined) {
      worktreeCheckCache.delete(oldestKey);
    }
  }
  worktreeCheckCache.set(path, result);
}

function getCachedWorktreeCheck(path: string): { is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean } | undefined {
  const result = worktreeCheckCache.get(path);
  if (result !== undefined) {
    // Re-insert to mark as recently used
    worktreeCheckCache.delete(path);
    worktreeCheckCache.set(path, result);
  }
  return result;
}

export function clearWorktreeCheckCache(): void {
  worktreeCheckCache.clear();
}

export function ManagedAgentDialog({ open, agent, pending = false, error, recentFolders, onClose, onLaunch, onAction, onListDirectories, onModels, onWorktreeCheck }: ManagedAgentDialogProps): ReactElement {
  const [name, setName] = useState("");
  const [cwd, setCwd] = useState("");
  const [selectedModel, setSelectedModel] = useState<ModelSummary>();
  const [catalog, setCatalog] = useState<ModelsResponse>();
  const [catalogLoading, setCatalogLoading] = useState(false);
  const [catalogError, setCatalogError] = useState<string>();
  const [instruction, setInstruction] = useState("");
  const [confirmTermination, setConfirmTermination] = useState(false);

  const [isMainWorktree, setIsMainWorktree] = useState(false);
  const [isCheckingGit, setIsCheckingGit] = useState(false);
  const [worktreeEnabled, setWorktreeEnabled] = useState(false);
  const [worktreeBranch, setWorktreeBranch] = useState("");
  const [worktreeBranchUserEdited, setWorktreeBranchUserEdited] = useState(false);

  // Keep a mutable ref to the latest onWorktreeCheck callback.
  // This ensures that re-renders of the parent component (e.g. state updates or prop changes)
  // never cancel or invalidate in-flight worktree check promises (Scenario WTL-002).
  const onWorktreeCheckRef = useRef(onWorktreeCheck);
  useEffect(() => {
    onWorktreeCheckRef.current = onWorktreeCheck;
  });

  // Track the monotonically increasing ID and path of the active check to guard against
  // out-of-order asynchronous responses (Scenario WTL-005).
  const activeCheckIdRef = useRef(0);
  const activePathRef = useRef("");
  const debounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Helper to execute or debounce worktree check for a given path
  const triggerWorktreeCheck = (rawPath: string, delayMs: number): void => {
    if (debounceTimerRef.current !== null) {
      clearTimeout(debounceTimerRef.current);
      debounceTimerRef.current = null;
    }

    const trimmed = rawPath.trim();
    activePathRef.current = trimmed;

    if (!open || agent || !trimmed) {
      setIsMainWorktree(false);
      setIsCheckingGit(false);
      setWorktreeEnabled(false);
      return;
    }

    // Apply cached result immediately if available (Scenario WTL-004)
    const cached = getCachedWorktreeCheck(trimmed);
    if (cached) {
      setIsCheckingGit(false);
      if (cached.is_main_worktree) {
        setIsMainWorktree(true);
      } else {
        setIsMainWorktree(false);
        setWorktreeEnabled(false);
      }
      return;
    }

    if (!onWorktreeCheckRef.current) {
      setIsMainWorktree(false);
      setIsCheckingGit(false);
      setWorktreeEnabled(false);
      return;
    }

    setIsCheckingGit(true);
    const checkId = ++activeCheckIdRef.current;

    const performCheck = (): void => {
      const checkFn = onWorktreeCheckRef.current;
      if (!checkFn) {
        if (checkId === activeCheckIdRef.current) {
          setIsCheckingGit(false);
        }
        return;
      }

      // Check worktree eligibility without running expensive git status --porcelain (Scenario WTL-006)
      checkFn(trimmed, { includeStatus: false })
        .then((result) => {
          // Discard stale responses if user has since navigated to another folder (Scenario WTL-005)
          if (checkId !== activeCheckIdRef.current || trimmed !== activePathRef.current) return;
          setCachedWorktreeCheck(trimmed, result);
          if (result.is_main_worktree) {
            setIsMainWorktree(true);
          } else {
            setIsMainWorktree(false);
            setWorktreeEnabled(false);
          }
        })
        .catch(() => {
          if (checkId !== activeCheckIdRef.current || trimmed !== activePathRef.current) return;
          setIsMainWorktree(false);
          setWorktreeEnabled(false);
        })
        .finally(() => {
          if (checkId === activeCheckIdRef.current) {
            setIsCheckingGit(false);
          }
        });
    };

    if (delayMs === 0) {
      performCheck();
    } else {
      debounceTimerRef.current = setTimeout(performCheck, delayMs);
    }
  };

  useEffect(() => {
    if (!open) return;
    setName("");
    const initialCwd = agent?.cwd ?? "";
    setCwd(initialCwd);
    setSelectedModel(agent?.model);
    setInstruction("");
    setConfirmTermination(false);
    setIsMainWorktree(false);
    setIsCheckingGit(false);
    setWorktreeEnabled(false);
    setWorktreeBranch("");
    setWorktreeBranchUserEdited(false);

    // When opening the dialog with an initial cwd, check immediately without debounce delay
    if (initialCwd.trim() && !agent) {
      triggerWorktreeCheck(initialCwd, 0);
    }
  }, [agent, open]);

  // Handle cwd changes from ManagedDirectoryPicker (clean onChange(path: string) signature)
  const handleCwdChange = (newPath: string): void => {
    setCwd(newPath);
    const trimmed = newPath.trim();
    // If already in cache, apply immediately (0ms); otherwise debounce 150ms for manual typing
    const isCached = trimmed !== "" && worktreeCheckCache.has(trimmed);
    triggerWorktreeCheck(newPath, isCached ? 0 : 150);
  };

  // Clean up debounce timer on unmount
  useEffect(() => {
    return () => {
      if (debounceTimerRef.current !== null) {
        clearTimeout(debounceTimerRef.current);
      }
    };
  }, []);

  // Auto-slugify branch name from agent name when not user-edited
  useEffect(() => {
    if (!worktreeBranchUserEdited) {
      setWorktreeBranch(name.trim().toLowerCase().replace(/\s+/g, "-"));
    }
  }, [name, worktreeBranchUserEdited]);

  useEffect(() => {
    let cancelled = false;
    if (!open || agent || !onModels) { setCatalog(undefined); setCatalogLoading(false); setCatalogError(undefined); return () => { cancelled = true; }; }
    setCatalogLoading(true); setCatalogError(undefined);
    void onModels().then((result) => { if (!cancelled) setCatalog(result); }).catch(() => { if (!cancelled) setCatalogError("Available models could not be loaded. You can still use the router default."); }).finally(() => { if (!cancelled) setCatalogLoading(false); });
    return () => { cancelled = true; };
  }, [agent, onModels, open]);

  const submit = (event: FormEvent<HTMLFormElement>): void => {
    event.preventDefault();
    if (worktreeEnabled && !worktreeBranch.trim()) return;
    const effectiveProvider = selectedModel?.provider ?? catalog?.default_model?.provider;
    const runtime: "rpc" | "embedded" = "embedded";
    void onLaunch({
      name: normalizeManagedAgentName(name),
      cwd: cwd.trim(),
      runtime,
      ...(selectedModel ? { model: { provider: selectedModel.provider, id: selectedModel.id } } : {}),
      ...(instruction.trim() ? { initial_instruction: instruction } : {}),
      ...(worktreeEnabled ? { worktree: true, worktree_branch: worktreeBranch.trim() } : {}),
    });
  };
  const availableModels = (catalog?.models ?? []).filter((model) => model.available);
  const submitDisabled = pending || (selectedModel === undefined && catalogLoading) || (worktreeEnabled && !worktreeBranch.trim());
  return <DialogShell open={open} title={agent ? `Manage ${agent.agent_id}` : "Launch managed agent"} titleId="managed-agent-dialog-title" description="Router-managed agents run locally through the router." onClose={onClose} closeLabel="Close managed agent dialog" className="message-dialog managed-agent-dialog">
    {agent ? <section className="managed-lifecycle-actions" aria-label={`Actions for ${agent.agent_id}`}>
      <p>State: {agent.lifecycle_state} · {agent.activity_state}</p>
      <div className="dialog-actions"><button type="button" disabled={pending} onClick={() => { void onAction(agent.agent_id, "restart"); }}>Restart</button><button type="button" className="secondary" disabled={pending} onClick={() => { void onAction(agent.agent_id, "reset"); }}>Reset</button><button type="button" className="danger-button" disabled={pending} onClick={() => setConfirmTermination(true)}>Terminate</button></div>
      {confirmTermination ? <div className="managed-confirmation" role="alert"><p>Terminate {agent.agent_id}?</p><button type="button" className="danger-button" disabled={pending} onClick={() => { void onAction(agent.agent_id, "terminate"); }}>Terminate agent</button><button type="button" className="secondary" onClick={() => setConfirmTermination(false)}>Cancel</button></div> : null}
    </section> : <form className="managed-agent-form" onSubmit={submit}>
      <label>Name<input required value={name} onChange={(event) => setName(event.currentTarget.value)} autoComplete="off" /></label>
      <ManagedDirectoryPicker value={cwd} {...(recentFolders === undefined ? {} : { recentFolders })} isCheckingGit={isCheckingGit} onChange={handleCwdChange} list={onListDirectories} />
      {isMainWorktree ? <div className="worktree-section">
        <button
          type="button"
          className="goal-toggle"
          aria-pressed={worktreeEnabled}
          onClick={() => setWorktreeEnabled(!worktreeEnabled)}
        >
          <span className="goal-toggle-label">Create a worktree</span>
          <span className="goal-toggle-track" aria-hidden="true">
            <span className="goal-toggle-thumb">
              <span className="goal-toggle-state">{worktreeEnabled ? "ON" : "OFF"}</span>
            </span>
          </span>
        </button>
        {worktreeEnabled ? <label>
          Branch name
          <input
            required
            value={worktreeBranch}
            onChange={(e) => {
              setWorktreeBranchUserEdited(true);
              setWorktreeBranch(e.currentTarget.value);
            }}
            placeholder="e.g. feature-name"
            autoComplete="off"
          />
        </label> : null}
      </div> : null}
      <fieldset><legend>Model (optional)</legend><ModelPicker models={availableModels} {...(selectedModel ? { currentModel: selectedModel } : {})} loading={catalogLoading} {...(catalog && availableModels.length === 0 ? { emptyMessage: "No connected models. Connect a provider in Provider settings." } : {})} pending={pending} disabled={pending} emptyOption={{ label: "Use router default", description: "Uses the router default when you launch.", onSelect: () => setSelectedModel(undefined) }} onChange={setSelectedModel} /></fieldset>
      {catalogError ? <p className="error" role="alert">{catalogError}</p> : null}
      <label>Initial instruction (optional)<textarea value={instruction} onChange={(event) => setInstruction(event.currentTarget.value)} /></label>
      <div className="dialog-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button type="submit" disabled={submitDisabled}>Launch</button></div>
    </form>}
    {error ? <p className="error" role="alert">{error}</p> : null}
  </DialogShell>;
}
