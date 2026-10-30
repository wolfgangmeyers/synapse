import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { WorkflowSchedulesView } from "../../src/components/WorkflowSchedulesView";
import { describeCron, intervalToCron } from "../../src/components/ScheduleCronPicker";
import type { RouterHttpClient } from "../../src/transport/http-client";
import type { WorkflowSchedule } from "../../src/protocol/types";
import type { Workflow } from "../../src/domain/workflow-states";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("ScheduleCronPicker utilities", () => {
  it("describes cron expressions accurately in human-readable terms", () => {
    expect(describeCron("*/5 * * * *")).toBe("Runs every 5 minutes");
    expect(describeCron("0 * * * *")).toBe("Runs at minute 0 of every hour");
    expect(describeCron("0 0 * * *")).toBe("Runs every day at 00:00");
    expect(describeCron("0 9 * * 1-5")).toBe("Runs Monday through Friday at 09:00");
    expect(describeCron("0 0 * * 1")).toBe("Runs every Monday at 00:00");
    expect(describeCron("0 0 1 * *")).toBe("Runs on the 1st of every month at 00:00");
    expect(describeCron("15 3 * * *")).toBe("Runs every day at 03:15");
  });

  it("converts intervals in ms to reasonable cron expressions", () => {
    expect(intervalToCron(60000)).toBe("* * * * *");
    expect(intervalToCron(300000)).toBe("*/5 * * * *");
    expect(intervalToCron(3600000)).toBe("0 * * * *");
    expect(intervalToCron(86400000)).toBe("0 0 * * *");
  });
});

describe("WorkflowSchedulesView Component", () => {
  const fakeSchedules: WorkflowSchedule[] = [
    {
      id: "sched-1",
      workflow_id: "wf-1",
      name: "Nightly Security Audit",
      enabled: true,
      schedule_type: "cron",
      cron_expression: "0 0 * * *",
      interval_ms: null,
      variables: { REPO: "mecha" },
      next_run_at: "2026-11-01T00:00:00Z",
      created_at: "2026-10-30T00:00:00Z",
      updated_at: "2026-10-30T00:00:00Z",
    },
    {
      id: "sched-2",
      workflow_id: "wf-1",
      name: "Hourly Health Ping",
      enabled: false,
      schedule_type: "cron",
      cron_expression: "0 * * * *",
      interval_ms: null,
      variables: { REPO: "mecha" },
      next_run_at: "2026-11-01T01:00:00Z",
      created_at: "2026-10-30T00:00:00Z",
      updated_at: "2026-10-30T00:00:00Z",
    },
  ];

  const fakeWorkflows: Workflow[] = [
    {
      id: "wf-1",
      name: "Security Audit Workflow",
      description: "Audits repository security",
      entry_state_id: "state-1",
      created_at: "2026-10-30T00:00:00Z",
      updated_at: "2026-10-30T00:00:00Z",
    },
  ];

  const createMockClient = () =>
    ({
      listWorkflowSchedules: vi.fn().mockResolvedValue(fakeSchedules),
      listWorkflows: vi.fn().mockResolvedValue(fakeWorkflows),
      createWorkflowSchedule: vi.fn(),
      updateWorkflowSchedule: vi.fn(),
      deleteWorkflowSchedule: vi.fn(),
    }) as unknown as RouterHttpClient;

  it("renders the configured schedules with human-readable descriptions", async () => {
    const client = createMockClient();
    render(<WorkflowSchedulesView client={client} />);

    expect(await screen.findByText("Nightly Security Audit")).toBeInTheDocument();
    expect(screen.getByText("Hourly Health Ping")).toBeInTheDocument();

    // Human-readable cron descriptions rendered
    expect(screen.getByText("Runs every day at 00:00")).toBeInTheDocument();
    expect(screen.getByText("Runs at minute 0 of every hour")).toBeInTheDocument();
  });

  it("opens add form with unselected workflow by default and cron picker", async () => {
    const client = createMockClient();
    render(<WorkflowSchedulesView client={client} />);

    const addBtns = await screen.findAllByText("+ Add Schedule");
    const targetAddBtn = addBtns[0];
    expect(targetAddBtn).toBeDefined();
    if (targetAddBtn) {
      fireEvent.click(targetAddBtn);
    }

    expect(screen.getByText("Add Schedule")).toBeInTheDocument();
    // Target workflow select placeholder is visible
    expect(screen.getByText("Select a workflow...")).toBeInTheDocument();

    // Cron picker presets and preview
    expect(screen.getByText("Common Presets")).toBeInTheDocument();
    expect(screen.getByText("Custom Cron")).toBeInTheDocument();
  });
});
