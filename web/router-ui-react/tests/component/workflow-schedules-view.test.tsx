// @vitest-environment jsdom

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { WorkflowSchedulesView } from "../../src/components/WorkflowSchedulesView";
import {
  describeStructuredSchedule,
  describeDays,
  formatTime12h,
} from "../../src/components/StructuredSchedulePicker";
import type { RouterHttpClient } from "../../src/transport/http-client";
import type { WorkflowSchedule } from "../../src/protocol/types";
import type { Workflow } from "../../src/domain/workflow-states";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("StructuredSchedule description utilities", () => {
  it("formats 24-hour clock strings into 12-hour strings with AM/PM", () => {
    expect(formatTime12h("00:00")).toBe("12:00 AM");
    expect(formatTime12h("09:15")).toBe("09:15 AM");
    expect(formatTime12h("12:00")).toBe("12:00 PM");
    expect(formatTime12h("17:45")).toBe("05:45 PM");
    expect(formatTime12h("23:59")).toBe("11:59 PM");
  });

  it("describes day sets accurately", () => {
    expect(describeDays([0, 1, 2, 3, 4, 5, 6])).toBe("every day");
    expect(describeDays([1, 2, 3, 4, 5])).toBe("Monday through Friday");
    expect(describeDays([6, 0])).toBe("on weekends");
    expect(describeDays([1])).toBe("every Monday");
    expect(describeDays([1, 3])).toBe("on Monday and Wednesday");
    expect(describeDays([1, 3, 5])).toBe("on Monday, Wednesday, and Friday");
  });

  it("describes structured schedules in natural English", () => {
    expect(
      describeStructuredSchedule({
        days_mode: "every_day",
        time_mode: "specific_times",
        times: ["09:00"],
      })
    ).toBe("Runs every day at 09:00 AM");

    expect(
      describeStructuredSchedule({
        days_mode: "specific_days",
        days: [1, 2, 3, 4, 5],
        time_mode: "specific_times",
        times: ["09:15", "17:45"],
      })
    ).toBe("Runs Monday through Friday at 09:15 AM and 05:45 PM");

    expect(
      describeStructuredSchedule({
        days_mode: "specific_days",
        days: [1, 2, 3, 4, 5],
        time_mode: "repeating_interval",
        interval: { value: 2, unit: "hours" },
      })
    ).toBe("Runs every 2 hours, Monday through Friday");

    expect(
      describeStructuredSchedule({
        days_mode: "every_day",
        time_mode: "repeating_interval",
        interval: { value: 15, unit: "minutes" },
      })
    ).toBe("Runs every 15 minutes continuously");
  });
});

describe("WorkflowSchedulesView Component", () => {
  const fakeSchedules: WorkflowSchedule[] = [
    {
      id: "sched-1",
      workflow_id: "wf-1",
      name: "Nightly Security Audit",
      enabled: true,
      schedule: {
        days_mode: "every_day",
        time_mode: "specific_times",
        times: ["00:00"],
      },
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
      schedule: {
        days_mode: "specific_days",
        days: [1, 2, 3, 4, 5],
        time_mode: "repeating_interval",
        interval: { value: 1, unit: "hours" },
      },
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

  it("renders the configured schedules with natural English descriptions", async () => {
    const client = createMockClient();
    render(<WorkflowSchedulesView client={client} />);

    expect(await screen.findByText("Nightly Security Audit")).toBeInTheDocument();
    expect(screen.getByText("Hourly Health Ping")).toBeInTheDocument();

    // Natural English structured schedule descriptions rendered
    expect(screen.getByText("Runs every day at 12:00 AM")).toBeInTheDocument();
    expect(screen.getByText("Runs every 1 hour, Monday through Friday")).toBeInTheDocument();
  });

  it("opens add form with unselected workflow and structured schedule picker", async () => {
    const client = createMockClient();
    render(<WorkflowSchedulesView client={client} recentFolders={["/recent/repo"]} />);

    const addBtns = await screen.findAllByText("+ Add Schedule");
    const targetAddBtn = addBtns[0];
    expect(targetAddBtn).toBeDefined();
    if (targetAddBtn) {
      fireEvent.click(targetAddBtn);
    }

    expect(screen.getByText("Add Schedule")).toBeInTheDocument();
    // Target workflow select placeholder is visible
    expect(screen.getByText("Select a workflow...")).toBeInTheDocument();

    // Structured schedule picker toggles are present
    expect(screen.getByText("Every day")).toBeInTheDocument();
    expect(screen.getByText("Specific days")).toBeInTheDocument();
    expect(screen.getByText("At specific times")).toBeInTheDocument();
    expect(screen.getByText("Repeating interval")).toBeInTheDocument();

    // Working directory picker is present with recent folders toggle
    expect(screen.getByText("Working Directory (optional)")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Recent folders" })).toBeInTheDocument();
  });
});
