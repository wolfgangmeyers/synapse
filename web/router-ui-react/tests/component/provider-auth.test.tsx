import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi, type Mock } from "vitest";
import { ProviderAuthDialog } from "../../src/components/ProviderAuthDialog";
import type { ProviderAuthClient } from "../../src/components/ProviderAuthDialog";
import type { ManagedProvider, ProviderAuthPromptFrame } from "../../src/protocol/types";
import type { DefaultModelResponse, ModelsResponse, ProviderAuthResponse } from "../../src/transport/http-client";

const timestamp = "2030-01-01T00:00:00.000Z";
const defaultAuth: ProviderAuthResponse = {
  auth_revision: 3,
  providers: [
    { provider: "openai-codex", label: "ChatGPT", authenticated: true, status: "authenticated", updated_at: timestamp },
    { provider: "google", label: "Google", authenticated: false, status: "signed_out", updated_at: timestamp },
    { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out", updated_at: timestamp },
  ],
};

interface TestAuthClient extends ProviderAuthClient {
  providerAuthMock: Mock<() => Promise<ProviderAuthResponse>>;
  modelsMock: Mock<() => Promise<ModelsResponse>>;
  startProviderAuthMock: Mock<(provider: ManagedProvider, action: "login" | "logout") => Promise<{ operation_id: string }>>;
  respondProviderAuthMock: Mock<(operationId: string, promptId: string, value: string) => Promise<{ operation_id: string }>>;
  cancelProviderAuthMock: Mock<(operationId: string) => Promise<{ operation_id: string; status: string }>>;
  setDefaultModelMock: Mock<(model: { provider: string; id: string } | null) => Promise<DefaultModelResponse>>;
}

function clientFixture(): TestAuthClient {
  const providerAuthMock = vi.fn(async () => defaultAuth);
  const modelsMock = vi.fn(async () => ({ auth_revision: 3, models: [{ provider: "openai-codex" as const, id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true }], default_model: null }));
  const startProviderAuthMock = vi.fn(async () => ({ operation_id: "auth-1" }));
  const respondProviderAuthMock = vi.fn(async () => ({ operation_id: "auth-1" }));
  const cancelProviderAuthMock = vi.fn(async () => ({ operation_id: "auth-1", status: "cancelled" }));
  const setDefaultModelMock = vi.fn(async () => ({ operation_id: "model-1", default_model: null, effective_fallback: { provider: "openai-codex" as const, id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true } }));

  return {
    providerAuth: providerAuthMock,
    models: modelsMock,
    startProviderAuth: startProviderAuthMock,
    respondProviderAuth: respondProviderAuthMock,
    cancelProviderAuth: cancelProviderAuthMock,
    setDefaultModel: setDefaultModelMock,
    providerAuthMock,
    modelsMock,
    startProviderAuthMock,
    respondProviderAuthMock,
    cancelProviderAuthMock,
    setDefaultModelMock,
  };
}

afterEach(cleanup);

describe("ProviderAuthDialog", () => {
  it("offers Claude defaults without connected providers and labels automatic Pi fallback", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    client.providerAuthMock.mockResolvedValue({ auth_revision: 3, providers: [] });
    client.modelsMock.mockResolvedValue({ auth_revision: 3, models: [
      { provider: "claude-code", id: "sonnet", name: "Claude Sonnet", available: true },
    ], default_model: null });
    render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    await user.click(await screen.findByRole("button", { name: "Automatic (Pi model)" }));
    const list = screen.getByRole("listbox", { name: "Default model options" });
    expect(within(list).getByRole("option", { name: "Automatic (Pi model)" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText(/first available Pi model/i)).toBeInTheDocument();
    expect(screen.getByText(/install the Claude CLI and sign in/i)).toBeInTheDocument();
    const select = screen.getByLabelText("Default model", { selector: "select" });
    expect(within(select).getByRole("option", { name: "Automatic (Pi model)" })).toBeInTheDocument();
    expect(within(select).getByRole("option", { name: "Claude Sonnet" })).toHaveValue("claude-code:sonnet");
    await user.click(within(list).getByRole("option", { name: /Claude Sonnet/ }));
    await waitFor(() => expect(client.setDefaultModelMock).toHaveBeenCalledWith({ provider: "claude-code", id: "sonnet" }));
  });

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
    const prompt: ProviderAuthPromptFrame = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 2,
      operation_id: "auth-1",
      provider: "openai-codex",
      prompt: { id: "code-1", kind: "manual_code", message: "Complete ChatGPT sign-in, then paste the code.", url: "https://auth.openai.com/authorize" },
    };
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
    const prompt: ProviderAuthPromptFrame = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 2,
      operation_id: "op-cancel-1",
      provider: "openai-codex",
      prompt: { id: "code-1", kind: "manual_code", message: "Enter code" },
    };
    render(<ProviderAuthDialog open client={client} prompt={prompt} onClose={() => undefined} onPromptHandled={onPromptHandled} />);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(client.cancelProviderAuth).toHaveBeenCalledWith("op-cancel-1"));
    expect(onPromptHandled).toHaveBeenCalledOnce();
  });

  it("Cancel button still dismisses the prompt when backend cancel fails", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    client.cancelProviderAuthMock.mockRejectedValue(new Error("already settled"));
    const onPromptHandled = vi.fn();
    const prompt: ProviderAuthPromptFrame = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 2,
      operation_id: "op-cancel-2",
      provider: "google",
      prompt: { id: "k1", kind: "secret", message: "Enter key" },
    };
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
    const prompt: ProviderAuthPromptFrame = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 2,
      operation_id: "auth-1",
      provider: "openai-codex",
      prompt: { id: "code-1", kind: "manual_code", message: "Your code is ABCD-1234 please enter it." },
    };
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
    const prompt: ProviderAuthPromptFrame = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 2,
      operation_id: "auth-1",
      provider: "google",
      prompt: { id: "key-1", kind: "secret", message: "Enter your Google key" },
    };
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

  it("renders exactly one Google section with an auth method selector switching between methods", async () => {
    const client = clientFixture();
    client.providerAuthMock.mockResolvedValue({
      auth_revision: 5,
      providers: [
        { provider: "openai-codex", label: "ChatGPT", authenticated: true, status: "authenticated", updated_at: timestamp },
        { provider: "google", label: "Google", authenticated: false, status: "signed_out", updated_at: timestamp },
        { provider: "google-vertex", label: "Google Vertex AI", authenticated: false, status: "signed_out", updated_at: timestamp },
        { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out", updated_at: timestamp },
      ],
    });

    render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    expect(await screen.findByText("ChatGPT")).toBeInTheDocument();
    expect(screen.getByText("Cursor")).toBeInTheDocument();

    // Exactly one Google heading / label across the provider rows
    const googleElements = screen.getAllByText("Google");
    expect(googleElements).toHaveLength(1);

    // Dropdown selector for Google auth method exists
    const methodSelect = screen.getByLabelText(/google authentication method/i);
    expect(methodSelect).toBeInTheDocument();
    expect(methodSelect).toHaveValue("api_key");

    // Has all 3 methods
    const selectElement = methodSelect;
    const optionElements = selectElement.querySelectorAll("option");
    const optionValues = Array.from(optionElements).map((opt) => opt.value);
    expect(optionValues).toContain("api_key");
    expect(optionValues).toContain("vertex_adc");
    expect(optionValues).not.toContain("antigravity");
    expect(optionValues).toHaveLength(2);
  });

  it("initializes auth method to first authenticated provider in priority order: google -> google-vertex, hiding dropdown when connected", async () => {
    const client = clientFixture();

    // Case 1: google authenticated, others also authenticated -> priority 1 is api_key, dropdown hidden
    client.providerAuthMock.mockResolvedValue({
      auth_revision: 5,
      providers: [
        { provider: "openai-codex", label: "ChatGPT", authenticated: true, status: "authenticated", updated_at: timestamp },
        { provider: "google", label: "Google", authenticated: true, status: "authenticated", updated_at: timestamp },
        { provider: "google-vertex", label: "Google Vertex AI", authenticated: true, status: "authenticated", account_label: "proj-1", updated_at: timestamp },
        { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out", updated_at: timestamp },
      ],
    });
    const { unmount } = render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    expect(await screen.findByText("ChatGPT")).toBeInTheDocument();
    expect(screen.queryByLabelText(/google authentication method/i)).not.toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Sign out" })).toHaveLength(2);
    unmount();

    // Case 2: google signed_out, google-vertex authenticated -> priority 2 is vertex_adc, dropdown hidden
    client.providerAuthMock.mockResolvedValue({
      auth_revision: 5,
      providers: [
        { provider: "openai-codex", label: "ChatGPT", authenticated: true, status: "authenticated", updated_at: timestamp },
        { provider: "google", label: "Google", authenticated: false, status: "signed_out", updated_at: timestamp },
        { provider: "google-vertex", label: "Google Vertex AI", authenticated: true, status: "authenticated", account_label: "proj-1", updated_at: timestamp },
        { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out", updated_at: timestamp },
      ],
    });
    const render2 = render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    expect(await render2.findByText(/project: proj-1/i)).toBeInTheDocument();
    expect(render2.queryByLabelText(/google authentication method/i)).not.toBeInTheDocument();
    render2.unmount();

    // Case 4: all disconnected -> dropdown visible, defaults to api_key
    client.providerAuthMock.mockResolvedValue({
      auth_revision: 5,
      providers: [
        { provider: "openai-codex", label: "ChatGPT", authenticated: true, status: "authenticated", updated_at: timestamp },
        { provider: "google", label: "Google", authenticated: false, status: "signed_out", updated_at: timestamp },
        { provider: "google-vertex", label: "Google Vertex AI", authenticated: false, status: "signed_out", updated_at: timestamp },
        { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out", updated_at: timestamp },
      ],
    });
    const render4 = render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    expect(await render4.findByLabelText(/google authentication method/i)).toHaveValue("api_key");
    render4.unmount();
  });

  it("changing dropdown selection does not mutate credentials and updates guidance when disconnected", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    client.providerAuthMock.mockResolvedValue({
      auth_revision: 5,
      providers: [
        { provider: "openai-codex", label: "ChatGPT", authenticated: true, status: "authenticated", updated_at: timestamp },
        { provider: "google", label: "Google", authenticated: false, status: "signed_out", updated_at: timestamp },
        { provider: "google-vertex", label: "Google Vertex AI", authenticated: false, status: "signed_out", account_label: "gcp-production-project", updated_at: timestamp },
        { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out", updated_at: timestamp },
      ],
    });

    render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    const methodSelect = await screen.findByLabelText(/google authentication method/i);

    // Initial is api_key
    expect(methodSelect).toHaveValue("api_key");
    expect(screen.queryByText(/gcloud auth application-default login/i)).not.toBeInTheDocument();

    // Switch to Vertex ADC: MUST NOT call client.startProviderAuth or mutate credentials
    await user.selectOptions(methodSelect, "vertex_adc");
    expect(client.startProviderAuth).not.toHaveBeenCalled();
    expect(methodSelect).toHaveValue("vertex_adc");
    expect(screen.getByText(/gcloud auth application-default login/i)).toBeInTheDocument();

  });

  it("independent logout only signs out the selected Google provider without affecting others", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    client.providerAuthMock.mockResolvedValue({
      auth_revision: 5,
      providers: [
        { provider: "openai-codex", label: "ChatGPT", authenticated: true, status: "authenticated", updated_at: timestamp },
        { provider: "google", label: "Google", authenticated: false, status: "signed_out", updated_at: timestamp },
        { provider: "google-vertex", label: "Google Vertex AI", authenticated: true, status: "authenticated", account_label: "my-gcp-project", updated_at: timestamp },
        { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out", updated_at: timestamp },
      ],
    });

    render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    await screen.findByText("ChatGPT");

    // Vertex is the active Google provider (first authenticated)
    expect(screen.getByText(/project: my-gcp-project/i)).toBeInTheDocument();
    expect(screen.queryByLabelText(/google authentication method/i)).not.toBeInTheDocument();

    // Click Sign out for Vertex
    const signOutButtons = screen.getAllByRole("button", { name: "Sign out" });
    // ChatGPT and Google (Vertex) are authenticated
    expect(signOutButtons.length).toBeGreaterThanOrEqual(2);
    // Click the Sign out button in the Google section
    await user.click(signOutButtons[1]!);

    expect(client.startProviderAuth).toHaveBeenCalledWith("google-vertex", "logout");
    expect(client.startProviderAuth).not.toHaveBeenCalledWith("google", "logout");
    expect(client.startProviderAuth).not.toHaveBeenCalledWith("antigravity", "logout");
  });

  it("handles Vertex ADC sequential text prompts for Project ID and Region with accessible labels", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    const onPromptHandled = vi.fn();
    const prompt1: ProviderAuthPromptFrame = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 1,
      operation_id: "op-v1",
      provider: "google-vertex",
      prompt: { id: "p-proj", kind: "text", message: "Google Cloud Project ID", placeholder: "my-project-id" },
    };

    const { rerender } = render(<ProviderAuthDialog open client={client} prompt={prompt1} onClose={() => undefined} onPromptHandled={onPromptHandled} />);
    const projInput = await screen.findByLabelText("Google Cloud Project ID");
    expect(projInput).toHaveAttribute("type", "text"); // Text, NOT password / secret
    expect(projInput).toHaveAttribute("placeholder", "my-project-id");

    await user.type(projInput, "custom-cloud-project");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(client.respondProviderAuth).toHaveBeenCalledWith("op-v1", "p-proj", "custom-cloud-project");
    expect(onPromptHandled).toHaveBeenCalledOnce();

    // Prompt 2: Region with default placeholder us-central1
    const prompt2: ProviderAuthPromptFrame = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 1,
      operation_id: "op-v1",
      provider: "google-vertex",
      prompt: { id: "p-reg", kind: "text", message: "Google Cloud Region", placeholder: "us-central1" },
    };
    rerender(<ProviderAuthDialog open client={client} prompt={prompt2} onClose={() => undefined} onPromptHandled={onPromptHandled} />);
    const regionInput = await screen.findByLabelText("Google Cloud Region");
    expect(regionInput).toHaveAttribute("type", "text");
    expect(regionInput).toHaveAttribute("placeholder", "us-central1");
    expect(regionInput).toHaveValue("us-central1");

    await user.clear(regionInput);
    await user.type(regionInput, "us-east4");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(client.respondProviderAuth).toHaveBeenCalledWith("op-v1", "p-reg", "us-east4");
  });

  it("handles prompt submission failure gracefully and reports error status", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    client.respondProviderAuthMock.mockRejectedValue(new Error("operation expired"));
    const onPromptHandled = vi.fn();
    const prompt: ProviderAuthPromptFrame = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 1,
      operation_id: "op-fail",
      provider: "google-vertex",
      prompt: { id: "p-fail", kind: "text", message: "Google Cloud Project ID" },
    };

    render(<ProviderAuthDialog open client={client} prompt={prompt} onClose={() => undefined} onPromptHandled={onPromptHandled} />);
    const input = await screen.findByLabelText("Google Cloud Project ID");
    await user.type(input, "test-proj");
    await user.click(screen.getByRole("button", { name: "Continue" }));

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("That sign-in prompt is no longer available."));
    expect(onPromptHandled).toHaveBeenCalledOnce();
  });

  it("submits default us-central1 for untouched Vertex Region prompt without typing, while blank project and key remain blocked", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    const onPromptHandled = vi.fn();

    // 1. Untouched Project prompt: must NOT default to placeholder and must block blank continue
    const projectPrompt: ProviderAuthPromptFrame = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 1,
      operation_id: "op-v-proj",
      provider: "google-vertex",
      prompt: { id: "p-proj", kind: "text", message: "Google Cloud Project ID", placeholder: "my-gcp-project" },
    };

    const { rerender } = render(<ProviderAuthDialog open client={client} prompt={projectPrompt} onClose={() => undefined} onPromptHandled={onPromptHandled} />);
    const projInput = await screen.findByLabelText("Google Cloud Project ID");
    expect(projInput).toHaveValue("");
    // Clicking Continue with untouched blank project must do nothing
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(client.respondProviderAuth).not.toHaveBeenCalled();
    expect(onPromptHandled).not.toHaveBeenCalled();

    // 2. Untouched Region prompt with placeholder "us-central1": must be visibly initialized to "us-central1" and submittable without typing
    const regionPrompt: ProviderAuthPromptFrame = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 1,
      operation_id: "op-v-reg",
      provider: "google-vertex",
      prompt: { id: "p-reg", kind: "text", message: "Google Cloud Region", placeholder: "us-central1" },
    };

    rerender(<ProviderAuthDialog open client={client} prompt={regionPrompt} onClose={() => undefined} onPromptHandled={onPromptHandled} />);
    const regionInput = await screen.findByLabelText("Google Cloud Region");
    expect(regionInput).toHaveValue("us-central1");

    // Click Continue without typing anything
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(client.respondProviderAuth).toHaveBeenCalledWith("op-v-reg", "p-reg", "us-central1");
    expect(onPromptHandled).toHaveBeenCalledOnce();
  });

  it("retains active prompt provider binding even when switching the Google method dropdown in background", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    const onPromptHandled = vi.fn();

    const regionPrompt: ProviderAuthPromptFrame = {
      type: "provider_auth_prompt",
      protocol: "mecha.web/v1",
      projection_version: 1,
      operation_id: "op-v-reg",
      provider: "google-vertex",
      prompt: { id: "p-reg", kind: "text", message: "Google Cloud Region", placeholder: "us-central1" },
    };

    render(<ProviderAuthDialog open client={client} prompt={regionPrompt} onClose={() => undefined} onPromptHandled={onPromptHandled} />);
    const regionInput = await screen.findByLabelText("Google Cloud Region");
    expect(regionInput).toHaveValue("us-central1");

    // Switch Google dropdown in background section to api_key
    const methodSelect = screen.getByLabelText(/google authentication method/i);
    await user.selectOptions(methodSelect, "api_key");
    expect(methodSelect).toHaveValue("api_key");

    // Active prompt remains bound to Vertex and submits us-central1
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(client.respondProviderAuth).toHaveBeenCalledWith("op-v-reg", "p-reg", "us-central1");
    expect(onPromptHandled).toHaveBeenCalledOnce();
  });

  it("shows initial loading skeleton while auth settings are loading without partial rows", () => {
    const client = clientFixture();
    // Return a promise that does not resolve immediately
    client.providerAuthMock.mockReturnValue(new Promise(() => {}));

    render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    expect(screen.getByRole("status", { name: "Loading provider settings…" })).toBeInTheDocument();
    expect(screen.getByText("Loading provider settings…")).toBeInTheDocument();
    // Should NOT render partial provider rows while loading
    expect(screen.queryByText("ChatGPT")).not.toBeInTheDocument();
    expect(screen.queryByText("Cursor")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign out" })).not.toBeInTheDocument();
  });

  it("opens custom dropdowns and allows selecting methods and models", async () => {
    const user = userEvent.setup();
    const client = clientFixture();

    render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    await screen.findByText("ChatGPT");

    // Click custom Google method dropdown trigger
    const googleTrigger = screen.getByRole("button", { name: /API key/i });
    expect(googleTrigger).toHaveAttribute("aria-expanded", "false");
    await user.click(googleTrigger);
    expect(googleTrigger).toHaveAttribute("aria-expanded", "true");

    // Choose Vertex AI ADC option in the custom menu
    const googleListbox = screen.getByRole("listbox", { name: "Google authentication method" });
    const vertexOption = within(googleListbox).getByRole("option", { name: "Vertex AI ADC" });
    await user.click(vertexOption);
    expect(googleTrigger).toHaveTextContent("Vertex AI ADC");

    // Click custom Default model dropdown trigger
    const modelTrigger = screen.getByRole("button", { name: /Automatic \(Pi model\)/i });
    await user.click(modelTrigger);
    const modelListbox = screen.getByRole("listbox", { name: "Default model options" });
    const modelOption = within(modelListbox).getByRole("option", { name: /GPT-5.6 Terra/i });
    await user.click(modelOption);
    expect(client.setDefaultModel).toHaveBeenCalledWith({ provider: "openai-codex", id: "gpt-5.6-terra" });
  });

  it("shows busy state on provider button while sign-in request is starting", async () => {
    const user = userEvent.setup();
    const client = clientFixture();
    let resolveAuth: (value: { operation_id: string }) => void = () => {};
    client.startProviderAuthMock.mockReturnValue(new Promise((resolve) => {
      resolveAuth = resolve;
    }));

    render(<ProviderAuthDialog open client={client} onClose={() => undefined} onPromptHandled={() => undefined} />);
    await screen.findByText("ChatGPT");

    // Cursor is not authenticated
    const connectButtons = screen.getAllByRole("button", { name: "Connect" });
    const cursorConnect = connectButtons[1]!; // Cursor is second Connect button
    await user.click(cursorConnect);

    // Button should now show Connecting... and be disabled
    expect(cursorConnect).toHaveTextContent("Connecting…");
    expect(cursorConnect).toBeDisabled();

    // Resolve the promise
    resolveAuth({ operation_id: "cursor-1" });
    await waitFor(() => expect(cursorConnect).not.toBeDisabled());
  });
});
