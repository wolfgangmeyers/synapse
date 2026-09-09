import { useEffect, useState } from "react";
import type { ProviderAuthPromptFrame } from "../protocol/types";
import type { DefaultModelResponse, ProviderAuthResponse, ModelsResponse, RouterHttpClient } from "../transport/http-client";
import { DialogShell } from "./DialogShell";

export type ProviderAuthClient = Pick<
  RouterHttpClient,
  "models" | "providerAuth" | "respondProviderAuth" | "setDefaultModel" | "startProviderAuth"
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
  const begin = async (provider: "openai-codex" | "google" | "cursor", action: "login" | "logout"): Promise<void> => {
    setStatus(action === "login" ? `Starting ${providerLabels[provider]} sign-in…` : `Signing out of ${providerLabels[provider]}…`);
    try { await client.startProviderAuth(provider, action); await refresh(); setStatus("Waiting for provider sign-in to finish."); }
    catch { setStatus("Provider request could not be started."); }
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
  return <DialogShell open={open} title="Provider settings" titleId="provider-settings-title" description="These settings apply to every managed agent." onClose={onClose} closeLabel="Close provider settings" className="provider-auth-dialog">
    <section aria-label="Providers">
      {(auth?.providers ?? []).map((provider) => <div className="provider-auth-row" key={provider.provider}>
        <div><strong>{provider.label}</strong><p>{provider.authenticated ? "Connected" : "Not connected"}</p></div>
        <button type="button" className="secondary" onClick={() => void begin(provider.provider, provider.authenticated ? "logout" : "login")}>{provider.authenticated ? "Sign out" : "Connect"}</button>
      </div>)}
    </section>
    {prompt ? <section className="provider-auth-prompt" aria-live="polite"><p>{prompt.prompt.message}</p><label>{prompt.provider === "google" ? "Google API key" : prompt.prompt.kind === "secret" ? "API key" : "Response"}<input type={prompt.provider === "google" || prompt.prompt.kind === "secret" ? "password" : "text"} value={value} placeholder={prompt.prompt.placeholder} onChange={(event) => setValue(event.currentTarget.value)} autoComplete="off" /></label><button type="button" onClick={() => void submitPrompt()}>Continue</button></section> : null}
    <section aria-label="Default model"><h3>Default model</h3><p>New managed agents use this model when you do not choose one.</p><select value={catalog?.default_model ? `${catalog.default_model.provider}:${catalog.default_model.id}` : ""} onChange={(event) => { const selected = event.currentTarget.value; if (selected === "") { void setDefault(null); return; } const [provider, ...id] = selected.split(":"); if (provider !== undefined) void setDefault({ provider, id: id.join(":") }); }}>
      <option value="">Use ChatGPT default</option>
      {(catalog?.models ?? []).filter((model) => model.available).map((model) => <option key={`${model.provider}:${model.id}`} value={`${model.provider}:${model.id}`}>{model.name}</option>)}
    </select></section>
    {status ? <p className="status" role="status">{status}</p> : null}
  </DialogShell>;
}
