import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ProviderAuthDialog } from "../../src/components/ProviderAuthDialog";
import type { ProviderAuthClient } from "../../src/components/ProviderAuthDialog";

function settingsClient(): ProviderAuthClient {
  const timestamp = "2030-01-01T00:00:00.000Z";
  return {
    providerAuth: vi.fn(async () => ({ auth_revision: 2, providers: [
      { provider: "openai-codex" as const, label: "ChatGPT" as const, authenticated: true, status: "authenticated" as const, updated_at: timestamp },
      { provider: "google" as const, label: "Google" as const, authenticated: false, status: "signed_out" as const, updated_at: timestamp },
      { provider: "google-vertex" as const, label: "Google Vertex AI" as const, authenticated: false, status: "signed_out" as const, updated_at: timestamp },
      { provider: "antigravity" as const, label: "Google Antigravity" as const, authenticated: false, status: "signed_out" as const, updated_at: timestamp },
      { provider: "cursor" as const, label: "Cursor" as const, authenticated: false, status: "signed_out" as const, updated_at: timestamp },
    ] })),
    models: vi.fn(async () => ({ auth_revision: 2, models: [{ provider: "openai-codex" as const, id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true }], default_model: null })),
    startProviderAuth: vi.fn(async () => ({ operation_id: "auth-1" })),
    respondProviderAuth: vi.fn(async () => ({ operation_id: "auth-1" })),
    cancelProviderAuth: vi.fn(async () => ({ operation_id: "auth-1", status: "cancelled" })),
    setDefaultModel: vi.fn(async () => ({ operation_id: "default-1", default_model: { provider: "openai-codex", id: "gpt-5.6-terra", name: "GPT-5.6 Terra" }, effective_fallback: { provider: "openai-codex" as const, id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true } })),
  };
}

describe("managed provider settings", () => {
  it("saves a discovered default model and permits clearing it", async () => {
    const user = userEvent.setup();
    const client = settingsClient();
    render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    const selector = await screen.findByRole("combobox", { name: "Default model" });
    await user.selectOptions(selector, "openai-codex:gpt-5.6-terra");
    expect(client.setDefaultModel).toHaveBeenCalledWith({ provider: "openai-codex", id: "gpt-5.6-terra" });
    await user.selectOptions(selector, "");
    expect(client.setDefaultModel).toHaveBeenLastCalledWith(null);
  });
});
