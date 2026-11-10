import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { BackgroundBashActivity } from "../../src/components/BackgroundBashActivity";
import { isManagedAgent } from "../../src/protocol/guards";

const agent = { agent_id: "one", source: "router-managed", cwd: "/tmp", model: { provider: "test", id: "test" }, desired_state: "running", lifecycle_state: "ready", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 0 }, created_at: "2030-01-01T00:00:00.000Z", started_at: null, updated_at: "2030-01-01T00:00:00.000Z", last_error: null };
afterEach(cleanup);

it("validates optional job projections without rejecting older routers", () => {
  expect(isManagedAgent(agent)).toBe(true);
  expect(isManagedAgent({ ...agent, background_bash_jobs: [{ id: "job-1", status: "running" }] })).toBe(true);
  expect(isManagedAgent({ ...agent, background_bash_jobs: [{ id: "", status: "running" }] })).toBe(false);
  expect(isManagedAgent({ ...agent, background_bash_jobs: [{ id: "job-1", status: 42 }] })).toBe(false);
});

it("keeps the header compact and discloses live job details", () => {
  const jobs = Array.from({ length: 40 }, (_, i) => ({ id: `long-job-id-${i}`, status: "running" }));
  const { rerender } = render(<BackgroundBashActivity jobs={jobs} />);
  expect(screen.getByRole("button", { name: /shell jobs 40/i })).toBeInTheDocument();
  expect(screen.queryByText(/long-job-id-0/)).not.toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: /shell jobs 40/i }));
  expect(screen.getByText(/long-job-id-0/)).toBeVisible();
  rerender(<BackgroundBashActivity jobs={[{ id: "job-2", status: "completed" }]} />);
  expect(screen.queryByText(/long-job-id-0/)).not.toBeInTheDocument();
  expect(screen.getByText(/job-2/)).toHaveTextContent("completed");
  fireEvent.click(screen.getByRole("button", { name: "Close dialog" }));
  expect(screen.queryByText(/job-2/)).not.toBeVisible();
  rerender(<BackgroundBashActivity jobs={[]} />);
  expect(screen.queryByLabelText("Background shell jobs")).not.toBeInTheDocument();
});

it("does not reopen the dialog when jobs disappear and return", () => {
  const { rerender } = render(<BackgroundBashActivity jobs={[{ id: "first", status: "running" }]} />);
  fireEvent.click(screen.getByRole("button", { name: /shell jobs 1/i }));
  expect(screen.getByRole("dialog", { name: "Background shell jobs" })).toBeVisible();
  rerender(<BackgroundBashActivity jobs={[]} />);
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  rerender(<BackgroundBashActivity jobs={[{ id: "second", status: "running" }]} />);
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: /shell jobs 1/i }));
  expect(screen.getByRole("dialog", { name: "Background shell jobs" })).toHaveTextContent("second");
});

it("reconciles a missed job change from the authoritative managed snapshot", async () => {
  const { reducer } = await import("../../src/state/reducer");
  const { createInitialState } = await import("../../src/state/app-state");
  const scope = { routerUrl: "http://router.test", agentId: "ui" };
  const managed = { ...agent, source: "router-managed" as const, background_bash_jobs: [{ id: "job-1", status: "running" }] };
  const initial = reducer(createInitialState(scope), { type: "managed/snapshot", scope, version: 1, agents: [managed] });
  const repaired = reducer(initial, { type: "managed/reconcile", scope, version: 2, agents: [{ ...managed, background_bash_jobs: [{ id: "job-1", status: "succeeded" }] }] });
  expect(repaired.managedAgents?.[0]?.background_bash_jobs).toEqual([{ id: "job-1", status: "succeeded" }]);
});
