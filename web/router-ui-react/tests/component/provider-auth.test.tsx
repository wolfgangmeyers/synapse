import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
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
    cancelProviderAuth: vi.fn(async () => ({ operation_id: "auth-1", status: "cancelled" })),
    setDefaultModel: vi.fn(async () => ({ operation_id: "model-1", default_model: null, effective_fallback: { provider: "openai-codex" as const, id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true } })),
  };
}

afterEach(cleanup);

describe("ProviderAuthDialog", () => {
  it("renders the bounded three-provider shared auth surface", async () => {
    const client = clientFixture();
    render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    const dialog = screen.getByRole("dialog", { name: "Provider settings" });
    expect(dialog).toHaveClass("message-dialog", "provider-auth-dialog");
    expect(screen.getByRole("heading", { name: "Providers" })).toBeInTheDocument();
    expect(await screen.findByText("ChatGPT")).toBeInTheDocument();
    expect(screen.getByText("Google")).toBeInTheDocument();
    expect(screen.getByText("Cursor")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign out" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Connect" })).toHaveLength(2);
  });

  it("opens the provider sign-in link in a new tab with noopener noreferrer", async () => {
    const client = clientFixture();
    const prompt = { type: "provider_auth_prompt" as const, protocol: "mecha.web/v1" as const, projection_version: 2, operation_id: "auth-1", provider: "openai-codex" as const, prompt: { id: "code-1", kind: "manual_code" as const, message: "Complete ChatGPT sign-in, then paste the code.", url: "https://auth.openai.com/authorize" } };
    render(<ProviderAuthDialog open client={client} prompt={prompt} onClose={() => undefined} onPromptHandled={() => undefined} />);
    const link = screen.getByRole("link", { name: "Open ChatGPT sign-in page" });
    expect(link).toHaveAttribute("href", "https://auth.openai.com/authorize");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("Cancel button calls cancelProviderAuth on the backend and dismisses the prompt", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    const onPromptHandled = vi.fn();
    const prompt = { type: "provider_auth_prompt" as const, protocol: "mecha.web/v1" as const, projection_version: 2, operation_id: "op-cancel-1", provider: "openai-codex" as const, prompt: { id: "code-1", kind: "manual_code" as const, message: "Enter code" } };
    render(<ProviderAuthDialog open client={client} prompt={prompt} onClose={() => undefined} onPromptHandled={onPromptHandled} />);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(client.cancelProviderAuth).toHaveBeenCalledWith("op-cancel-1"));
    expect(onPromptHandled).toHaveBeenCalledOnce();
  });

  it("Cancel button still dismisses the prompt when backend cancel fails", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    (client.cancelProviderAuth as ReturnType<typeof vi.fn>).mockRejectedValue(new Error("already settled"));
    const onPromptHandled = vi.fn();
    const prompt = { type: "provider_auth_prompt" as const, protocol: "mecha.web/v1" as const, projection_version: 2, operation_id: "op-cancel-2", provider: "google" as const, prompt: { id: "k1", kind: "secret" as const, message: "Enter key" } };
    render(<ProviderAuthDialog open client={client} prompt={prompt} onClose={() => undefined} onPromptHandled={onPromptHandled} />);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    // Dismissal must not block on backend error
    await waitFor(() => expect(onPromptHandled).toHaveBeenCalledOnce());
  });

  it("icon-only copy button copies the device code and shows success feedback", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    let copied = "";
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: vi.fn(async (text: string) => { copied = text; }) },
      configurable: true,
    });
    const prompt = { type: "provider_auth_prompt" as const, protocol: "mecha.web/v1" as const, projection_version: 2, operation_id: "auth-1", provider: "openai-codex" as const, prompt: { id: "code-1", kind: "manual_code" as const, message: "Your code is ABCD-1234 please enter it." } };
    render(<ProviderAuthDialog open client={client} prompt={prompt} onClose={() => undefined} onPromptHandled={() => undefined} />);
    const copyBtn = screen.getByRole("button", { name: "Copy code" });
    // Icon-only: no visible text in the button
    expect(copyBtn).not.toHaveTextContent(/copy/i);
    await user.click(copyBtn);
    expect(copied).toBe("ABCD-1234");
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
