import "@testing-library/jest-dom/vitest";
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ContextThresholdDialog } from "../../src/components/ContextThresholdDialog";
import type { RouterHttpClient, ContextThresholdConfig, ModelsResponse } from "../../src/transport/http-client";

afterEach(cleanup);

describe("ContextThresholdDialog", () => {
  const initialConfig: ContextThresholdConfig = {
    warning_percent: 10,
    reset_percent: 20,
    model_thresholds: {
      "google-vertex/gemini-3.8-flash": {
        warning_percent: 15,
        reset_percent: 25,
      },
    },
  };

  const initialModels: ModelsResponse = {
    auth_revision: 1,
    models: [
      { provider: "google-vertex", id: "gemini-3.8-flash", name: "Gemini 3.8 Flash", available: true },
      { provider: "google-vertex", id: "gemini-3.8-pro", name: "Gemini 3.8 Pro", available: true },
    ],
    default_model: { provider: "google-vertex", id: "gemini-3.8-flash" },
  };

  const createMockClient = (overrides?: Partial<RouterHttpClient>): RouterHttpClient => {
    return {
      contextThresholds: vi.fn().mockResolvedValue(initialConfig),
      saveContextThresholds: vi.fn().mockImplementation(async (cfg) => cfg),
      models: vi.fn().mockResolvedValue(initialModels),
      ...overrides,
    } as unknown as RouterHttpClient;
  };

  it("renders thresholds and explanation text when opened", async () => {
    const client = createMockClient();
    render(<ContextThresholdDialog open={true} client={client} onClose={vi.fn()} />);

    expect(await screen.findByText(/Context Reset & Warning Thresholds/i)).toBeInTheDocument();
    expect(screen.getByText("Warning threshold:")).toBeInTheDocument();
    expect(screen.getByText("Fallback Auto-Reset threshold:")).toBeInTheDocument();

    await waitFor(() => {
      expect(screen.getByLabelText("Global Warning Threshold:")).toBeInTheDocument();
      expect(document.querySelector("#global-warning-input")).toHaveValue(10);
      expect(document.querySelector("#global-reset-input")).toHaveValue(20);
      expect(document.querySelector(".override-model-name")).toHaveTextContent("google-vertex/gemini-3.8-flash");
    });
  });

  it("validates and calls saveContextThresholds on save", async () => {
    const client = createMockClient();
    const user = userEvent.setup();
    render(<ContextThresholdDialog open={true} client={client} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(document.querySelector(".override-model-name")).toHaveTextContent("google-vertex/gemini-3.8-flash");
    });

    const saveBtn = screen.getByRole("button", { name: "Save Thresholds" });
    await user.click(saveBtn);

    await waitFor(() => {
      expect(client.saveContextThresholds).toHaveBeenCalledWith({
        warning_percent: 10,
        reset_percent: 20,
        model_thresholds: {
          "google-vertex/gemini-3.8-flash": {
            warning_percent: 15,
            reset_percent: 25,
          },
        },
      });
      expect(screen.getByText(/Context thresholds saved successfully/i)).toBeInTheDocument();
    });
  });

  it("allows removing a model override", async () => {
    const client = createMockClient();
    const user = userEvent.setup();
    render(<ContextThresholdDialog open={true} client={client} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(document.querySelector(".override-model-name")).toHaveTextContent("google-vertex/gemini-3.8-flash");
    });

    const removeBtn = screen.getByRole("button", { name: "Remove override for google-vertex/gemini-3.8-flash" });
    await user.click(removeBtn);

    expect(document.querySelector(".override-model-name")).toBeNull();
    expect(screen.getByText(/No per-model overrides configured/i)).toBeInTheDocument();
  });

  it("allows adding a model override", async () => {
    const client = createMockClient();
    const user = userEvent.setup();
    render(<ContextThresholdDialog open={true} client={client} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(document.querySelector(".override-model-name")).toHaveTextContent("google-vertex/gemini-3.8-flash");
    });

    // Select second model
    const select = screen.getByLabelText("Model:");
    await user.selectOptions(select, "google-vertex/gemini-3.8-pro");

    const addBtn = screen.getByRole("button", { name: "Add Override" });
    await user.click(addBtn);

    await waitFor(() => {
      const names = Array.from(document.querySelectorAll(".override-model-name")).map((el) => el.textContent);
      expect(names).toContain("google-vertex/gemini-3.8-pro");
    });
  });
});
