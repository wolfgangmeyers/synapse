import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ProviderAuthDialog } from "../../src/components/ProviderAuthDialog";
import type { ProviderAuthClient } from "../../src/components/ProviderAuthDialog";

const timestamp = "2030-01-01T00:00:00.000Z";
const auth = {
  auth_revision: 3,
  providers: [
    { provider: "openai-codex" as const, label: "ChatGPT" as const, authenticated: true, status: "authenticated" as const, updated_at: timestamp },
    { provider: "google" as const, label: "Google" as const, authenticated: false, status: "signed_out" as const, updated_at: timestamp },
    { provider: "cursor" as const, label: "Cursor" as const, authenticated: false, status: "signed_out" as const, updated_at: timestamp },
  ],
};

function clientFixture(): ProviderAuthClient {
  return {
    providerAuth: vi.fn(async () => auth),
    models: vi.fn(async () => ({ auth_revision: 3, models: [{ provider: "openai-codex" as const, id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true }], default_model: null })),
    startProviderAuth: vi.fn(async () => ({ operation_id: "auth-1" })),
    respondProviderAuth: vi.fn(async () => ({ operation_id: "auth-1" })),
    setDefaultModel: vi.fn(async () => ({ operation_id: "model-1", default_model: null, effective_fallback: { provider: "openai-codex" as const, id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true } })),
  };
}

describe("ProviderAuthDialog", () => {
  it("renders the bounded three-provider shared auth surface", async () => {
    const client = clientFixture();
    render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    expect(await screen.findByText("ChatGPT")).toBeInTheDocument();
    expect(screen.getByText("Google")).toBeInTheDocument();
    expect(screen.getByText("Cursor")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign out" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Connect" })).toHaveLength(2);
  });

  it("submits a secret prompt once and clears input before the next prompt", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    const onPromptHandled = vi.fn();
    const prompt = { type: "provider_auth_prompt" as const, protocol: "mecha.web/v1" as const, projection_version: 2, operation_id: "auth-1", provider: "google" as const, prompt: { id: "key-1", kind: "secret" as const, message: "Enter your Google key" } };
    const { rerender } = render(<ProviderAuthDialog open client={client} prompt={prompt} onClose={() => undefined} onPromptHandled={onPromptHandled} />);
    const input = await screen.findByLabelText("Google API key");
    expect(input).toHaveAttribute("type", "password");
    await user.type(input, "never-persist-this");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(client.respondProviderAuth).toHaveBeenCalledWith("auth-1", "key-1", "never-persist-this");
    expect(input).toHaveValue("");
    expect(onPromptHandled).toHaveBeenCalledOnce();

    rerender(<ProviderAuthDialog open client={client} prompt={{ ...prompt, prompt: { ...prompt.prompt, id: "key-2" } }} onClose={() => undefined} onPromptHandled={onPromptHandled} />);
    expect(screen.getByLabelText("Google API key")).toHaveValue("");
  });
});
