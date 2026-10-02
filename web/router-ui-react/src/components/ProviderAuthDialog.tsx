import { useEffect, useRef, useState, useCallback } from "react";
import type { ProviderAuthPromptFrame } from "../protocol/types";
import type { DefaultModelResponse, ProviderAuthResponse, ModelsResponse, RouterHttpClient } from "../transport/http-client";
import { DialogShell } from "./DialogShell";

export type ProviderAuthClient = Pick<
  RouterHttpClient,
  "models" | "providerAuth" | "respondProviderAuth" | "cancelProviderAuth" | "setDefaultModel" | "startProviderAuth"
>;

export interface ProviderAuthDialogProps {
  open: boolean;
  client: ProviderAuthClient;
  prompt?: ProviderAuthPromptFrame;
  refreshKey?: number;
  onClose: () => void;
  onPromptHandled: () => void;
}

const providerLabels: Record<"openai-codex" | "google" | "cursor", string> = {
  "openai-codex": "ChatGPT",
  google: "Google",
  cursor: "Cursor",
};

export function ProviderAuthDialog({ open, client, prompt, refreshKey = 0, onClose, onPromptHandled }: ProviderAuthDialogProps) {
  const [auth, setAuth] = useState<ProviderAuthResponse>();
  const [catalog, setCatalog] = useState<ModelsResponse>();
  const [status, setStatus] = useState("");
  const [value, setValue] = useState("");
  const [codeCopied, setCodeCopied] = useState(false);
  const promptInputRef = useRef<HTMLInputElement>(null);
  const copyTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const refresh = async (): Promise<void> => {
    // Auth status remains useful even while a separate model catalog refresh is
    // temporarily unavailable. Do not hide the only way to repair sign-in.
    const nextAuth = await client.providerAuth();
    setAuth(nextAuth);
    try { setCatalog(await client.models()); } catch { setCatalog(undefined); }
  };
  useEffect(() => {
    if (!open) { setValue(""); setStatus(""); return; }
    void refresh().catch(() => setStatus("Provider settings are unavailable."));
  }, [open, client, refreshKey]);
  useEffect(() => {
    // A newly issued prompt must never inherit input from an earlier prompt.
    setValue("");
  }, [prompt?.operation_id, prompt?.prompt.id]);
  useEffect(() => {
    if (!open || !prompt) return;
    const frame = requestAnimationFrame(() => promptInputRef.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, [open, prompt?.operation_id, prompt?.prompt.id]);
  const begin = async (provider: "openai-codex" | "google" | "cursor", action: "login" | "logout"): Promise<void> => {
    setStatus(action === "login" ? `Starting ${providerLabels[provider]} sign-in…` : `Signing out of ${providerLabels[provider]}…`);
    try { await client.startProviderAuth(provider, action); await refresh(); setStatus("Waiting for provider sign-in to finish."); }
    catch { setStatus("Provider request could not be started."); }
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
      void client.cancelProviderAuth(prompt.operation_id).catch(() => {
        // Best-effort: backend cancel may fail if the operation already settled.
        // UI dismissal proceeds regardless so the user is never blocked.
      });
    }
    setValue("");
    onPromptHandled?.();
  };
  const submitPrompt = async (): Promise<void> => {
    if (!prompt || value === "") return;
    try {
      await client.respondProviderAuth(prompt.operation_id, prompt.prompt.id, value);
      setValue(""); // Sensitive input is consumed once and not retained in UI state.
      onPromptHandled(); setStatus("Provider sign-in is continuing.");
    } catch { setValue(""); onPromptHandled(); setStatus("That sign-in prompt is no longer available."); }
  };
  const setDefault = async (model: { provider: string; id: string } | null): Promise<void> => {
    try {
      const result: DefaultModelResponse = await client.setDefaultModel(model);
      setStatus(`New sessions will use ${result.effective_fallback.name}.`);
      await refresh();
    } catch { setStatus("Default model could not be saved."); }
  };

  return <DialogShell open={open} title="Provider settings" titleId="provider-settings-title" description="These settings apply to every managed agent." onClose={onClose} closeLabel="Close provider settings" className="message-dialog provider-auth-dialog">
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
      })() : <p>{prompt.prompt.message}</p>}
      {prompt.prompt.url ? <a href={prompt.prompt.url} target="_blank" rel="noopener noreferrer">Open {providerLabels[prompt.provider]} sign-in page</a> : null}
      <label>{prompt.provider === "google" ? "Google API key" : prompt.prompt.kind === "secret" ? "API key" : "Response"}<input ref={promptInputRef} type={prompt.provider === "google" || prompt.prompt.kind === "secret" ? "password" : "text"} value={value} placeholder={prompt.prompt.placeholder} onChange={(event) => setValue(event.currentTarget.value)} autoComplete="off" /></label>
      <div className="provider-auth-actions"><button type="button" className="ghost" onClick={handleCancelPrompt}>Cancel</button><button type="button" onClick={() => void submitPrompt()}>Continue</button></div>
    </section> : null}
    <section className="provider-auth-section" aria-labelledby="provider-auth-providers-title">
      <div className="provider-auth-section-heading"><h3 id="provider-auth-providers-title">Providers</h3><p>Connect the providers managed agents can use.</p></div>
      {(auth?.providers ?? []).map((provider) => <div className="provider-auth-row" key={provider.provider}>
        <div className="provider-auth-provider"><strong>{provider.label}</strong><p className="provider-auth-connection"><span className={`status-dot${provider.authenticated ? " connected" : ""}`} aria-hidden="true" />{provider.authenticated ? "Connected" : "Not connected"}</p></div>
        <button type="button" className="secondary" onClick={() => void begin(provider.provider, provider.authenticated ? "logout" : "login")}>{provider.authenticated ? "Sign out" : "Connect"}</button>
      </div>)}
    </section>
    <section className="provider-auth-section" aria-labelledby="provider-default-model-title"><div className="provider-auth-section-heading"><h3 id="provider-default-model-title">Default model</h3><p>New managed agents use this model when you do not choose one.</p></div><select aria-label="Default model" value={catalog?.default_model ? `${catalog.default_model.provider}:${catalog.default_model.id}` : ""} onChange={(event) => { const selected = event.currentTarget.value; if (selected === "") { void setDefault(null); return; } const [provider, ...id] = selected.split(":"); if (provider !== undefined) void setDefault({ provider, id: id.join(":") }); }}>
      <option value="">Use ChatGPT default</option>
      {(catalog?.models ?? []).filter((model) => model.available).map((model) => <option key={`${model.provider}:${model.id}`} value={`${model.provider}:${model.id}`}>{model.name}</option>)}
    </select></section>
    {status ? <p className="provider-auth-status" role="status">{status}</p> : null}
  </DialogShell>;
}
