import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SystemOneConfigDialog } from "../../src/components/SystemOneConfigDialog";
import type { RouterHttpClient, SystemOneConfig, SystemOneTestResponse } from "../../src/transport/http-client";

afterEach(cleanup);

function createMockClient(overrides: Partial<RouterHttpClient> = {}): RouterHttpClient {
  let config: SystemOneConfig = { host: "127.0.0.1", port: 11434, enabled: false };

  const defaultClient: Partial<RouterHttpClient> = {
    systemOneConfig: vi.fn(async () => ({ ...config })),
    saveSystemOneConfig: vi.fn(async (newCfg: SystemOneConfig) => {
      config = { ...newCfg };
      return { ...config };
    }),
    testSystemOne: vi.fn(async () => {
      const res: SystemOneTestResponse = {
        status: "ok",
        response: {
          model: "clef",
          answers: {
            is_sql_injection: {
              type: "noul",
              noul: 0.985,
            },
          },
          usage: {
            input_tokens: 150,
          },
        },
      };
      return res;
    }),
  };

  return { ...defaultClient, ...overrides } as unknown as RouterHttpClient;
}

describe("SystemOneConfigDialog component", () => {
  it("loads current configuration when opened", async () => {
    const client = createMockClient();
    render(<SystemOneConfigDialog open={true} client={client} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByLabelText("Host")).toHaveValue("127.0.0.1");
      expect(screen.getByLabelText("Port")).toHaveValue(11434);
      expect(screen.getByLabelText("Enable System 1 decision engine")).not.toBeChecked();
    });
  });

  it("saves updated host, port, and enabled state", async () => {
    const user = userEvent.setup();
    const client = createMockClient();
    render(<SystemOneConfigDialog open={true} client={client} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByLabelText("Host")).toHaveValue("127.0.0.1");
    });

    const hostInput = screen.getByLabelText("Host");
    const portInput = screen.getByLabelText("Port");
    const enabledCheckbox = screen.getByLabelText("Enable System 1 decision engine");

    await user.clear(hostInput);
    await user.type(hostInput, "100.115.63.19");
    await user.clear(portInput);
    await user.type(portInput, "11434");
    await user.click(enabledCheckbox);

    const saveBtn = screen.getByRole("button", { name: "Save Configuration" });
    await user.click(saveBtn);

    await waitFor(() => {
      expect(client.saveSystemOneConfig).toHaveBeenCalledWith({
        host: "100.115.63.19",
        port: 11434,
        enabled: true,
      });
      expect(screen.getByText("System 1 configuration saved.")).toBeInTheDocument();
    });
  });

  it("executes sandbox test and displays formatted calibrated decision response", async () => {
    const user = userEvent.setup();
    const client = createMockClient();
    render(<SystemOneConfigDialog open={true} client={client} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByLabelText("Host")).toBeInTheDocument();
    });

    const runBtn = screen.getByRole("button", { name: "Run Test Decision" });
    await user.click(runBtn);

    await waitFor(() => {
      expect(client.testSystemOne).toHaveBeenCalled();
      expect(screen.getByText("Decision Success")).toBeInTheDocument();
      expect(screen.getByText("is_sql_injection")).toBeInTheDocument();
      expect(screen.getByText("98.5%")).toBeInTheDocument();
    });
  });

  it("displays clear error state when sandbox decision fails", async () => {
    const user = userEvent.setup();
    const client = createMockClient({
      testSystemOne: vi.fn(async () => ({
        status: "error" as const,
        error: "system one is not configured",
      })),
    });
    render(<SystemOneConfigDialog open={true} client={client} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByLabelText("Host")).toBeInTheDocument();
    });

    const runBtn = screen.getByRole("button", { name: "Run Test Decision" });
    await user.click(runBtn);

    await waitFor(() => {
      expect(screen.getByText("Decision Failed")).toBeInTheDocument();
      expect(screen.getByText("system one is not configured")).toBeInTheDocument();
    });
  });
});
