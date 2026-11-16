import { useEffect, useRef, useState, useCallback, useMemo } from "react";
import type { ManagedProvider, ProviderAuthPromptFrame } from "../protocol/types";
import type { DefaultModelResponse, ProviderAuthResponse, ProviderStatus, ModelsResponse, RouterHttpClient, CustomProvider, CustomProviderModel } from "../transport/http-client";
import { DialogShell } from "./DialogShell";

export type ProviderAuthClient = Pick<
  RouterHttpClient,
  | "models"
  | "providerAuth"
  | "respondProviderAuth"
  | "cancelProviderAuth"
  | "setDefaultModel"
  | "startProviderAuth"
  | "listCustomProviders"
  | "createCustomProvider"
  | "updateCustomProvider"
  | "deleteCustomProvider"
  | "discoverCustomProviderModels"
>;

export interface ProviderAuthDialogProps {
  open: boolean;
  client: ProviderAuthClient;
  prompt?: ProviderAuthPromptFrame;
  refreshKey?: number;
  onClose: () => void;
  onPromptHandled: () => void;
}

export type GoogleAuthMethod = "api_key" | "vertex_adc";

const googleMethodToProvider: Record<GoogleAuthMethod, ManagedProvider> = {
  api_key: "google",
  vertex_adc: "google-vertex",
};

const googleMethodLabels: Record<GoogleAuthMethod, string> = {
  api_key: "API key",
  vertex_adc: "Vertex AI ADC",
};

const providerLabels: Record<ManagedProvider, string> = {
  "openai-codex": "ChatGPT",
  google: "Google",
  cursor: "Cursor",
  "google-vertex": "Google Vertex AI",
};

function isGoogleAuthMethod(value: string): value is GoogleAuthMethod {
  return value === "api_key" || value === "vertex_adc";
}

// Shared status line text; error styling is derived from it so a later provider action that overwrites status clears the red.
const defaultSaveFailedMessage = "Default model could not be saved.";

function computeInitialGoogleMethod(providers?: ProviderStatus[]): GoogleAuthMethod {
  if (!providers) return "api_key";
  const google = providers.find((p) => p.provider === "google");
  if (google?.authenticated) return "api_key";
  const vertex = providers.find((p) => p.provider === "google-vertex");
  if (vertex?.authenticated) return "vertex_adc";
  return "api_key";
}

function getInitialPromptValue(prompt?: ProviderAuthPromptFrame): string {
  if (prompt?.provider === "google-vertex" && prompt.prompt.placeholder === "us-central1") {
    return "us-central1";
  }
  return "";
}

function requiresPromptInput(prompt: ProviderAuthPromptFrame): boolean {
  if (prompt.prompt.kind === "manual_code") return false;
  if (prompt.prompt.url) {
    const msg = (prompt.prompt.message || "").toLowerCase();
    const isExplicitPaste = msg.includes("paste") || msg.includes("callback") || msg.includes("authorization code");
    const isGenericAutomated = msg.includes("requested sign-in value") || msg === "response" || msg === "";
    return isExplicitPaste && !isGenericAutomated;
  }
  return true;
}

export function ProviderAuthDialog({ open, client, prompt, refreshKey = 0, onClose, onPromptHandled }: ProviderAuthDialogProps) {
  const [auth, setAuth] = useState<ProviderAuthResponse>();
  const [catalog, setCatalog] = useState<ModelsResponse>();
  const [status, setStatus] = useState("");
  const [refreshNotice, setRefreshNotice] = useState("");
  const [value, setValue] = useState(() => getInitialPromptValue(prompt));
  const [codeCopied, setCodeCopied] = useState(false);
  const [userSelectedGoogleMethod, setUserSelectedGoogleMethod] = useState<GoogleAuthMethod | null>(null);
  const [pendingProvider, setPendingProvider] = useState<ManagedProvider | null>(null);
  const [googleMenuOpen, setGoogleMenuOpen] = useState(false);
  const [modelMenuOpen, setModelMenuOpen] = useState(false);

  // Custom provider states
  const [dialogMode, setDialogMode] = useState<"list" | "form">("list");
  const [customProviders, setCustomProviders] = useState<CustomProvider[]>([]);
  const [editingCustomId, setEditingCustomId] = useState<string | null>(null);
  const [customLabel, setCustomLabel] = useState("");
  const [customBaseUrl, setCustomBaseUrl] = useState("");
  const [customApiKey, setCustomApiKey] = useState("");
  const [discoveredModels, setDiscoveredModels] = useState<CustomProviderModel[]>([]);
  const [manualModelInput, setManualModelInput] = useState("");
  const [discovering, setDiscovering] = useState(false);
  const [savingCustom, setSavingCustom] = useState(false);
  const [customFormError, setCustomFormError] = useState("");

  const promptInputRef = useRef<HTMLInputElement>(null);
  const copyTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const googlePickerRef = useRef<HTMLDivElement>(null);
  const modelPickerRef = useRef<HTMLDivElement>(null);

  const selectedGoogleMethod: GoogleAuthMethod =
    userSelectedGoogleMethod ?? computeInitialGoogleMethod(auth?.providers);
  const activeGoogleProvider = googleMethodToProvider[selectedGoogleMethod];
  const activeGoogleStatus = auth?.providers.find((p) => p.provider === activeGoogleProvider);
  const isGoogleConnected = Boolean(activeGoogleStatus?.authenticated);

  const googleStatusText = useMemo(() => {
    if (!isGoogleConnected) return "Not connected";
    if (selectedGoogleMethod === "vertex_adc") {
      return activeGoogleStatus?.account_label
        ? `Connected (project: ${activeGoogleStatus.account_label})`
        : "Connected";
    }
    return activeGoogleStatus?.account_label
      ? `Connected (${activeGoogleStatus.account_label})`
      : "Connected";
  }, [isGoogleConnected, selectedGoogleMethod, activeGoogleStatus?.account_label]);

  const defaultModelLabel = useMemo(() => {
    if (!catalog?.default_model) return "Automatic (Pi model)";
    const matched = (catalog.models ?? []).find(
      (m) => m.provider === catalog.default_model?.provider && m.id === catalog.default_model?.id
    );
    return matched ? matched.name : `${catalog.default_model.provider}:${catalog.default_model.id}`;
  }, [catalog]);

  const seenUnauthenticated = useRef(false);

  const refresh = async (): Promise<void> => {
    const nextAuth = await client.providerAuth();
    setAuth(nextAuth);
    try { setCatalog(await client.models()); } catch { setCatalog(undefined); }
    try {
      if (client.listCustomProviders) {
        setCustomProviders(await client.listCustomProviders());
      }
    } catch {
      setCustomProviders([]);
    }
  };

  useEffect(() => {
    if (!open) {
      setValue("");
      setStatus("");
      setRefreshNotice("");
      setPendingProvider(null);
      setGoogleMenuOpen(false);
      setModelMenuOpen(false);
      setDialogMode("list");
      setEditingCustomId(null);
      setCustomLabel("");
      setCustomBaseUrl("");
      setCustomApiKey("");
      setDiscoveredModels([]);
      setManualModelInput("");
      setDiscovering(false);
      setSavingCustom(false);
      setCustomFormError("");
      return;
    }
    void refresh().catch(() => setStatus("Provider settings are unavailable."));
  }, [open, client, refreshKey]);

  useEffect(() => {
    setValue(getInitialPromptValue(prompt));
    seenUnauthenticated.current = false;
  }, [prompt?.operation_id, prompt?.prompt.id, prompt?.provider, prompt?.prompt.placeholder]);

  useEffect(() => {
    if (!open || !prompt) return;
    if (!requiresPromptInput(prompt)) return;
    const frame = requestAnimationFrame(() => promptInputRef.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, [open, prompt?.operation_id, prompt?.prompt.id, prompt]);

  useEffect(() => {
    if (!googleMenuOpen && !modelMenuOpen) return;
    const handlePointerDown = (event: MouseEvent) => {
      const target = event.target instanceof Node ? event.target : null;
      if (!target) return;
      if (googleMenuOpen && googlePickerRef.current && !googlePickerRef.current.contains(target)) {
        setGoogleMenuOpen(false);
      }
      if (modelMenuOpen && modelPickerRef.current && !modelPickerRef.current.contains(target)) {
        setModelMenuOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        if (googleMenuOpen) {
          event.stopPropagation();
          setGoogleMenuOpen(false);
        } else if (modelMenuOpen) {
          event.stopPropagation();
          setModelMenuOpen(false);
        }
      }
    };
    window.addEventListener("pointerdown", handlePointerDown);
    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("pointerdown", handlePointerDown);
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [googleMenuOpen, modelMenuOpen]);

  useEffect(() => {
    if (!prompt || !auth) return;
    const providerStatus = auth.providers.find((p) => p.provider === prompt.provider);
    if (!providerStatus?.authenticated) {
      seenUnauthenticated.current = true;
      return;
    }
    if (seenUnauthenticated.current) {
      onPromptHandled?.();
    }
  }, [auth, prompt, onPromptHandled]);

  const begin = async (provider: ManagedProvider, action: "login" | "logout"): Promise<void> => {
    setPendingProvider(provider);
    setStatus(action === "login" ? `Starting ${providerLabels[provider]} sign-in…` : `Signing out of ${providerLabels[provider]}…`);
    try {
      await client.startProviderAuth(provider, action);
      await refresh();
      setStatus("Waiting for provider sign-in to finish.");
    } catch {
      setStatus("Provider request could not be started.");
    } finally {
      setPendingProvider(null);
    }
  };

  const handleCopyCode = useCallback((code: string): void => {
    void navigator.clipboard.writeText(code).then(() => {
      setCodeCopied(true);
      if (copyTimeoutRef.current !== null) clearTimeout(copyTimeoutRef.current);
      copyTimeoutRef.current = setTimeout(() => { setCodeCopied(false); copyTimeoutRef.current = null; }, 2000);
    });
  }, []);

  const handleCancelPrompt = (): void => {
    if (prompt) {
      void client.cancelProviderAuth(prompt.operation_id).catch(() => {});
    }
    setValue("");
    onPromptHandled?.();
  };

  const submitPrompt = async (): Promise<void> => {
    if (!prompt || value === "") return;
    try {
      await client.respondProviderAuth(prompt.operation_id, prompt.prompt.id, value);
      setValue("");
      onPromptHandled();
      setStatus("Provider sign-in is continuing.");
    } catch {
      setValue("");
      onPromptHandled();
      setStatus("That sign-in prompt is no longer available.");
    }
  };

  const setDefault = async (model: { provider: string; id: string } | null): Promise<void> => {
    setRefreshNotice("");
    let result: DefaultModelResponse;
    try {
      result = await client.setDefaultModel(model);
    } catch {
      setStatus(defaultSaveFailedMessage);
      return;
    }
    setCatalog((current) => current ? { ...current, default_model: result.default_model } : current);
    setStatus(`New sessions will use ${result.effective_fallback.name}.`);
    try {
      const [nextAuth, nextCatalog] = await Promise.all([client.providerAuth(), client.models()]);
      setAuth(nextAuth);
      setCatalog(nextCatalog);
    } catch {
      setRefreshNotice("Could not refresh the model list. Reload to check availability.");
    }
  };

  const startAddCustom = (): void => {
    setEditingCustomId(null);
    setCustomLabel("");
    setCustomBaseUrl("");
    setCustomApiKey("");
    setDiscoveredModels([]);
    setManualModelInput("");
    setCustomFormError("");
    setDialogMode("form");
  };

  const startEditCustom = (p: CustomProvider): void => {
    setEditingCustomId(p.id);
    setCustomLabel(p.label);
    setCustomBaseUrl(p.base_url);
    setCustomApiKey("");
    setDiscoveredModels(p.models || []);
    setManualModelInput("");
    setCustomFormError("");
    setDialogMode("form");
  };

  const handleDiscoverModels = async (): Promise<void> => {
    const url = customBaseUrl.trim();
    if (!url) {
      setCustomFormError("Base URL is required to discover models.");
      return;
    }
    setDiscovering(true);
    setCustomFormError("");
    try {
      const res = await client.discoverCustomProviderModels({
        base_url: url,
        ...(customApiKey.trim() ? { api_key: customApiKey.trim() } : {}),
      });
      if (res.models && res.models.length > 0) {
        setDiscoveredModels(res.models);
      } else {
        setCustomFormError("No models were discovered from this endpoint. You can add model IDs manually below.");
      }
    } catch {
      setCustomFormError("Model discovery failed. You can add model IDs manually below.");
    } finally {
      setDiscovering(false);
    }
  };

  const handleAddManualModel = (): void => {
    const trimmed = manualModelInput.trim();
    if (!trimmed) return;
    if (discoveredModels.some((m) => m.id === trimmed)) {
      setManualModelInput("");
      return;
    }
    setDiscoveredModels((prev) => [
      ...prev,
      {
        id: trimmed,
        display_name: trimmed,
        context_window: 131072,
        max_tokens: 8192,
        supports_tools: true,
      },
    ]);
    setManualModelInput("");
  };

  const handleRemoveModel = (id: string): void => {
    setDiscoveredModels((prev) => prev.filter((m) => m.id !== id));
  };

  const handleSaveCustomProvider = async (e: React.FormEvent): Promise<void> => {
    e.preventDefault();
    const label = customLabel.trim();
    const baseUrl = customBaseUrl.trim();
    if (!label) {
      setCustomFormError("Provider name is required.");
      return;
    }
    if (!baseUrl) {
      setCustomFormError("Base URL is required.");
      return;
    }
    if (discoveredModels.length === 0) {
      setCustomFormError("At least one model must be added or discovered.");
      return;
    }

    setSavingCustom(true);
    setCustomFormError("");
    try {
      const providerId = editingCustomId || label.toLowerCase().replace(/[^a-z0-9_-]+/g, "-").replace(/^-+|-+$/g, "") || "custom";
      if (editingCustomId) {
        await client.updateCustomProvider(editingCustomId, {
          label,
          base_url: baseUrl,
          ...(customApiKey.trim() ? { api_key: customApiKey.trim() } : {}),
          models: discoveredModels,
        });
      } else {
        await client.createCustomProvider({
          id: providerId,
          label,
          base_url: baseUrl,
          ...(customApiKey.trim() ? { api_key: customApiKey.trim() } : {}),
          models: discoveredModels,
        });
      }
      await refresh();
      setDialogMode("list");
      setStatus(`Saved custom provider "${label}".`);
    } catch (err) {
      setCustomFormError(err instanceof Error ? err.message : "Failed to save custom provider.");
    } finally {
      setSavingCustom(false);
    }
  };

  const handleDeleteCustomProvider = async (p: CustomProvider): Promise<void> => {
    try {
      await client.deleteCustomProvider(p.id);
      await refresh();
      setStatus(`Deleted custom provider "${p.label}".`);
    } catch {
      setStatus(`Failed to delete custom provider "${p.label}".`);
    }
  };

  return <DialogShell open={open} title="Provider settings" titleId="provider-settings-title" description={dialogMode === "form" ? "Configure an OpenAI-compatible custom endpoint." : "These settings apply to every managed agent."} onClose={onClose} closeLabel="Close provider settings" className="message-dialog provider-auth-dialog">
    {dialogMode === "form" ? (
      <section className="custom-provider-form-section">
        <form onSubmit={(e) => void handleSaveCustomProvider(e)}>
          <div className="custom-provider-form-fields" style={{ display: "grid", gap: "12px" }}>
            <label>
              Provider Name / Label
              <input
                type="text"
                placeholder="e.g. Local Ollama, vLLM"
                value={customLabel}
                onChange={(e) => setCustomLabel(e.target.value)}
                required
                aria-label="Provider name"
              />
            </label>
            <label>
              Base URL (OpenAI-compatible)
              <input
                type="url"
                placeholder="http://127.0.0.1:11434/v1"
                value={customBaseUrl}
                onChange={(e) => setCustomBaseUrl(e.target.value)}
                required
                aria-label="Base URL"
              />
            </label>
            <label>
              API Key (optional)
              <input
                type="password"
                placeholder={editingCustomId ? "Leave blank to keep existing key" : "Optional secret token"}
                value={customApiKey}
                onChange={(e) => setCustomApiKey(e.target.value)}
                autoComplete="off"
                aria-label="API key"
              />
            </label>

            <div style={{ display: "flex", gap: "8px", alignItems: "center", marginTop: "4px" }}>
              <button
                type="button"
                className="secondary"
                disabled={discovering || !customBaseUrl.trim()}
                onClick={() => void handleDiscoverModels()}
              >
                {discovering ? "Discovering…" : "Test & Discover Models"}
              </button>
            </div>

            {customFormError && (
              <p className="provider-auth-status error" role="alert">{customFormError}</p>
            )}

            <div className="custom-models-section" style={{ display: "grid", gap: "8px", marginTop: "8px" }}>
              <strong>Configured Models ({discoveredModels.length})</strong>
              {discoveredModels.length > 0 ? (
                <ul className="custom-models-list" style={{ listStyle: "none", padding: 0, margin: 0, display: "grid", gap: "4px" }}>
                  {discoveredModels.map((m) => (
                    <li key={m.id} style={{ display: "flex", justifyContent: "space-between", alignItems: "center", background: "var(--canvas)", padding: "6px 10px", borderRadius: "8px" }}>
                      <span>{m.display_name || m.id}</span>
                      <button
                        type="button"
                        className="icon-btn-round danger"
                        title={`Remove model ${m.id}`}
                        aria-label={`Remove model ${m.id}`}
                        onClick={() => handleRemoveModel(m.id)}
                      >
                        <i className="fa-solid fa-xmark" aria-hidden="true" />
                      </button>
                    </li>
                  ))}
                </ul>
              ) : (
                <p style={{ fontSize: "13px", color: "var(--text-muted)" }}>No models added yet. Click &quot;Test &amp; Discover Models&quot; or enter one below.</p>
              )}

              <div style={{ display: "flex", gap: "8px", marginTop: "4px" }}>
                <input
                  type="text"
                  placeholder="Enter manual model ID (e.g. llama3.2:3b)"
                  value={manualModelInput}
                  onChange={(e) => setManualModelInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      handleAddManualModel();
                    }
                  }}
                  aria-label="Manual model ID"
                />
                <button
                  type="button"
                  className="secondary"
                  disabled={!manualModelInput.trim()}
                  onClick={handleAddManualModel}
                >
                  Add Model
                </button>
              </div>
            </div>
          </div>

          <div className="provider-auth-actions" style={{ marginTop: "18px" }}>
            <button
              type="button"
              className="ghost"
              onClick={() => {
                setCustomFormError("");
                setDialogMode("list");
              }}
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={savingCustom || !customLabel.trim() || !customBaseUrl.trim() || discoveredModels.length === 0}
            >
              {savingCustom ? "Saving…" : "Save Provider"}
            </button>
          </div>
        </form>
      </section>
    ) : (
      <>
    {prompt ? <section className="provider-auth-prompt" aria-live="polite" aria-labelledby="provider-auth-prompt-title">
      <h3 id="provider-auth-prompt-title">Sign-in request</h3>
      {prompt.prompt.kind === "manual_code" ? (() => {
        const raw = prompt.prompt.message;
        const match = /\b([A-Z0-9]{4,}(?:-[A-Z0-9]{4,})+)\b/.exec(raw);
        const code = match ? (match[1] ?? raw.trim()) : raw.trim();
        const before = match ? raw.slice(0, match.index) : "";
        const after = match ? raw.slice(match.index + match[0].length) : "";
        return (
          <>
            {before ? <p>{before.trim()}</p> : null}
            <div className="provider-auth-code">
              <code className="provider-auth-device-code">{code}</code>
              <button
                type="button"
                className="icon-button ghost"
                aria-label="Copy code"
                title="Copy code"
                onClick={() => handleCopyCode(code)}
              >
                {codeCopied
                  ? <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><polyline points="20 6 9 17 4 12" /></svg>
                  : <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><rect x="9" y="9" width="13" height="13" rx="2" /><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" /></svg>
                }
              </button>
            </div>
            {after ? <p>{after.trim()}</p> : null}
          </>
        );
      })() : (
        prompt.prompt.message && !prompt.prompt.message.toLowerCase().includes("requested sign-in value") ? (
          <p>{prompt.prompt.message}</p>
        ) : null
      )}
      {prompt.prompt.url ? <a href={prompt.prompt.url} target="_blank" rel="noopener noreferrer">Open {providerLabels[prompt.provider]} sign-in page</a> : null}
      {requiresPromptInput(prompt) ? (() => {
        const isSecret = prompt.provider === "google" || prompt.prompt.kind === "secret";
        const promptLabel = prompt.provider === "google"
          ? "Google API key"
          : (prompt.provider === "google-vertex" || prompt.prompt.kind === "text")
            ? (prompt.prompt.message || "Response")
            : isSecret
              ? "API key"
              : (prompt.prompt.message || "Response");
        return (
          <label>
            {promptLabel}
            <input
              ref={promptInputRef}
              type={isSecret ? "password" : "text"}
              value={value}
              placeholder={prompt.prompt.placeholder}
              onChange={(event) => setValue(event.currentTarget.value)}
              autoComplete="off"
            />
          </label>
        );
      })() : (
        <div className="provider-auth-waiting">
          <span className="provider-auth-spinner small" aria-hidden="true" />
          <span>Waiting for sign-in to complete…</span>
        </div>
      )}
      <div className="provider-auth-actions">
        <button type="button" className="ghost" onClick={handleCancelPrompt}>Cancel</button>
        {requiresPromptInput(prompt) && (
          <button type="button" onClick={() => void submitPrompt()}>Continue</button>
        )}
      </div>
    </section> : null}
    <section className="provider-auth-section" aria-labelledby="provider-auth-providers-title">
      <div className="provider-auth-section-heading">
        <h3 id="provider-auth-providers-title">Providers</h3>
        <p>Connect the providers managed agents can use.</p>
      </div>
      {auth === undefined ? (
        <div className="provider-auth-loading" role="status" aria-label="Loading provider settings…">
          <div className="provider-auth-loading-header">
            <span className="provider-auth-spinner" aria-hidden="true" />
            <span>Loading provider settings…</span>
          </div>
          <div className="provider-auth-skeleton-list" aria-hidden="true">
            <div className="provider-auth-skeleton-row" />
            <div className="provider-auth-skeleton-row" />
            <div className="provider-auth-skeleton-row" />
          </div>
        </div>
      ) : (() => {
        const codex = auth.providers.find((p) => p.provider === "openai-codex");
        const cursor = auth.providers.find((p) => p.provider === "cursor");
        return (
          <>
            {codex && (
              <div className="provider-auth-row" key="openai-codex">
                <div className="provider-auth-provider">
                  <strong>{codex.label}</strong>
                  <p className="provider-auth-connection">
                    <span className={`status-dot${codex.authenticated ? " connected" : ""}`} aria-hidden="true" />
                    {codex.authenticated ? "Connected" : "Not connected"}
                  </p>
                </div>
                <button
                  type="button"
                  className="secondary"
                  disabled={pendingProvider !== null}
                  onClick={() => void begin("openai-codex", codex.authenticated ? "logout" : "login")}
                >
                  {pendingProvider === "openai-codex" ? (
                    <span className="provider-button-busy">
                      <span className="provider-auth-spinner small" aria-hidden="true" />
                      <span>{codex.authenticated ? "Signing out…" : "Connecting…"}</span>
                    </span>
                  ) : (
                    codex.authenticated ? "Sign out" : "Connect"
                  )}
                </button>
              </div>
            )}
            <div className="provider-auth-row provider-auth-row-google" key="google-unified">
              <div className="provider-auth-provider">
                <strong>Google</strong>
                {!isGoogleConnected && (
                  <div ref={googlePickerRef} className="model-picker provider-dropdown">
                    <button
                      type="button"
                      className="model-current"
                      aria-haspopup="listbox"
                      aria-expanded={googleMenuOpen}
                      onClick={() => setGoogleMenuOpen((v) => !v)}
                    >
                      <span>{googleMethodLabels[selectedGoogleMethod]}</span>
                      <i className="fa-solid fa-chevron-down" aria-hidden="true" />
                    </button>
                    {googleMenuOpen && (
                      <div className="model-menu provider-dropdown-menu" role="listbox" aria-label="Google authentication method">
                        <div className="model-options">
                          <button
                            type="button"
                            className="model-option"
                            role="option"
                            aria-selected={selectedGoogleMethod === "api_key"}
                            onClick={() => {
                              setUserSelectedGoogleMethod("api_key");
                              setGoogleMenuOpen(false);
                            }}
                          >
                            <strong>API key</strong>
                          </button>
                          <button
                            type="button"
                            className="model-option"
                            role="option"
                            aria-selected={selectedGoogleMethod === "vertex_adc"}
                            onClick={() => {
                              setUserSelectedGoogleMethod("vertex_adc");
                              setGoogleMenuOpen(false);
                            }}
                          >
                            <strong>Vertex AI ADC</strong>
                          </button>

                        </div>
                      </div>
                    )}
                    <select
                      className="visually-hidden"
                      tabIndex={-1}
                      aria-label="Google authentication method"
                      value={selectedGoogleMethod}
                      onChange={(event) => {
                        const next = event.currentTarget.value;
                        if (isGoogleAuthMethod(next)) {
                          setUserSelectedGoogleMethod(next);
                        }
                      }}
                    >
                      <option value="api_key">API key</option>
                      <option value="vertex_adc">Vertex AI ADC</option>
                    </select>
                  </div>
                )}
                <p className="provider-auth-connection">
                  <span className={`status-dot${isGoogleConnected ? " connected" : ""}`} aria-hidden="true" />
                  {googleStatusText}
                </p>
                {!isGoogleConnected && selectedGoogleMethod === "vertex_adc" && (
                  <p className="provider-auth-guidance">
                    <small>Requires host credentials. Run <code>gcloud auth application-default login</code> on the host.</small>
                  </p>
                )}
              </div>
              <button
                type="button"
                className="secondary"
                disabled={pendingProvider !== null}
                onClick={() => void begin(activeGoogleProvider, isGoogleConnected ? "logout" : "login")}
              >
                {pendingProvider === activeGoogleProvider ? (
                  <span className="provider-button-busy">
                    <span className="provider-auth-spinner small" aria-hidden="true" />
                    <span>{isGoogleConnected ? "Signing out…" : "Connecting…"}</span>
                  </span>
                ) : (
                  isGoogleConnected ? "Sign out" : "Connect"
                )}
              </button>
            </div>
            {cursor && (
              <div className="provider-auth-row" key="cursor">
                <div className="provider-auth-provider">
                  <strong>{cursor.label}</strong>
                  <p className="provider-auth-connection">
                    <span className={`status-dot${cursor.authenticated ? " connected" : ""}`} aria-hidden="true" />
                    {cursor.authenticated ? "Connected" : "Not connected"}
                  </p>
                </div>
                <button
                  type="button"
                  className="secondary"
                  disabled={pendingProvider !== null}
                  onClick={() => void begin("cursor", cursor.authenticated ? "logout" : "login")}
                >
                  {pendingProvider === "cursor" ? (
                    <span className="provider-button-busy">
                      <span className="provider-auth-spinner small" aria-hidden="true" />
                      <span>{cursor.authenticated ? "Signing out…" : "Connecting…"}</span>
                    </span>
                  ) : (
                    cursor.authenticated ? "Sign out" : "Connect"
                  )}
                </button>
              </div>
            )}
          </>
        );
      })()}
    </section>
    <section className="provider-auth-section" aria-labelledby="custom-providers-title">
      <div className="provider-auth-section-heading" style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
        <div>
          <h3 id="custom-providers-title">Custom Providers</h3>
          <p>Add self-hosted endpoints (Ollama, vLLM, LM Studio) or OpenAI-compatible providers.</p>
        </div>
        <button
          type="button"
          className="btn btn-primary"
          onClick={startAddCustom}
        >
          + Add Custom Provider
        </button>
      </div>

      {customProviders.length === 0 ? (
        <p style={{ fontSize: "13px", color: "var(--text-muted)", margin: 0 }}>No custom providers configured yet.</p>
      ) : (
        <div style={{ display: "grid", gap: "8px" }}>
          {customProviders.map((cp) => (
            <div className="provider-auth-row" key={cp.id}>
              <div className="provider-auth-provider">
                <strong>{cp.label}</strong>
                <p className="provider-auth-connection">
                  <span className={`status-dot${cp.enabled ? " connected" : ""}`} aria-hidden="true" />
                  <span>{cp.models.length} {cp.models.length === 1 ? "model" : "models"} ({cp.base_url})</span>
                </p>
              </div>
              <div style={{ display: "flex", gap: "6px", alignItems: "center" }}>
                <button
                  type="button"
                  className="icon-btn-round"
                  title={`Edit ${cp.label}`}
                  aria-label={`Edit ${cp.label}`}
                  onClick={() => startEditCustom(cp)}
                >
                  <i className="fa-solid fa-pen" aria-hidden="true" />
                </button>
                <button
                  type="button"
                  className="icon-btn-round danger"
                  title={`Delete ${cp.label}`}
                  aria-label={`Delete ${cp.label}`}
                  onClick={() => void handleDeleteCustomProvider(cp)}
                >
                  <i className="fa-solid fa-trash" aria-hidden="true" />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </section>

    {auth !== undefined && (
      <section className="provider-auth-section" aria-labelledby="provider-default-model-title">
        <div className="provider-auth-section-heading">
          <h3 id="provider-default-model-title">Default model</h3>
          <p>Used for new worker launches when no model is chosen. Automatic uses a Pi model; Claude models need the Claude CLI installed and signed in on the router host.</p>
        </div>
        <div ref={modelPickerRef} className="model-picker provider-dropdown default-model-picker">
          <button
            type="button"
            className="model-current"
            aria-haspopup="listbox"
            aria-expanded={modelMenuOpen}
            onClick={() => setModelMenuOpen((v) => !v)}
          >
            <span>{defaultModelLabel}</span>
            <i className="fa-solid fa-chevron-down" aria-hidden="true" />
          </button>
          {modelMenuOpen && (
            <div className="model-menu provider-dropdown-menu" role="listbox" aria-label="Default model options">
              <div className="model-options">
                <button
                  type="button"
                  className="model-option"
                  role="option"
                  aria-selected={!catalog?.default_model}
                  onClick={() => {
                    void setDefault(null);
                    setModelMenuOpen(false);
                  }}
                >
                  <strong>Automatic (Pi model)</strong>
                </button>
                {(catalog?.models ?? []).filter((model) => model.available).map((model) => (
                  <button
                    key={`${model.provider}:${model.id}`}
                    type="button"
                    className="model-option"
                    role="option"
                    aria-selected={catalog?.default_model ? `${catalog.default_model.provider}:${catalog.default_model.id}` === `${model.provider}:${model.id}` : false}
                    onClick={() => {
                      void setDefault({ provider: model.provider, id: model.id });
                      setModelMenuOpen(false);
                    }}
                  >
                    <strong>{model.name}</strong>
                    <small>{model.provider}:{model.id}</small>
                  </button>
                ))}
              </div>
            </div>
          )}
          <select
            className="visually-hidden"
            tabIndex={-1}
            aria-label="Default model"
            value={catalog?.default_model ? `${catalog.default_model.provider}:${catalog.default_model.id}` : ""}
            onChange={(event) => {
              const selected = event.currentTarget.value;
              if (selected === "") { void setDefault(null); return; }
              const [provider, ...id] = selected.split(":");
              if (provider !== undefined) void setDefault({ provider, id: id.join(":") });
            }}
          >
            <option value="">Automatic (Pi model)</option>
            {(catalog?.models ?? []).filter((model) => model.available).map((model) => (
              <option key={`${model.provider}:${model.id}`} value={`${model.provider}:${model.id}`}>
                {model.name}
              </option>
            ))}
          </select>
        </div>
      </section>
    )}
    {status ? <p className={`provider-auth-status${status === defaultSaveFailedMessage ? " error" : ""}`} role="status">{status}</p> : null}
    {refreshNotice ? <p className="provider-auth-status">{refreshNotice}</p> : null}
    </>
    )}
  </DialogShell>;
}
