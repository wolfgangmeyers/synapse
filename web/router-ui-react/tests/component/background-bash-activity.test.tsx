import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
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
  expect(screen.getByRole("button", { name: /shell jobs 0/i })).toBeInTheDocument();
});

it("keeps the empty dialog open when jobs disappear", () => {
  const { rerender } = render(<BackgroundBashActivity jobs={[{ id: "first", status: "running" }]} />);
  fireEvent.click(screen.getByRole("button", { name: /shell jobs 1/i }));
  rerender(<BackgroundBashActivity jobs={[]} />);
  expect(screen.getByRole("dialog")).toHaveTextContent("No retained shell jobs");
  expect(screen.getByRole("button", { name: /shell jobs 0/i })).toBeInTheDocument();
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


it("retries errors explicitly and uses projection status consistently", async () => {
  const { HttpRequestError } = await import("../../src/transport/http-client");
  const detail = vi.fn().mockRejectedValueOnce(new HttpRequestError(503, "temporary", "temporary", {}))
    .mockResolvedValue({ id: "job-1", status: "running", output: "final", truncated: false, exitCode: 7 });
  const client = { backgroundBashDetail: detail } as unknown as import("../../src/transport/http-client").RouterHttpClient;
  render(<BackgroundBashActivity jobs={[{ id: "job-1", status: "succeeded" }]} agentId="one" client={client} />);
  fireEvent.click(screen.getByRole("button", { name: /shell jobs 0/i }));
  expect(await screen.findByText(/Could not load output/)).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("final")).toBeVisible();
  expect(screen.getByText("job-1: succeeded")).toBeVisible();
  expect(detail).toHaveBeenCalledTimes(2);
});

it("bounds requests, times out hung reads, and refreshes only active results", async () => {
  vi.useFakeTimers();
  try {
    const jobs = Array.from({ length: 10 }, (_, i) => ({ id: `job-${i}`, status: i === 9 ? "running" : "succeeded" }));
    const detail = vi.fn((_agent: string, id: string) => id === "job-0" ? new Promise(() => {}) : Promise.resolve({ id, status: "succeeded", output: id, truncated: false, exitCode: 0 }));
    const client = { backgroundBashDetail: detail } as unknown as import("../../src/transport/http-client").RouterHttpClient;
    render(<BackgroundBashActivity jobs={jobs} agentId="one" client={client} />);
    fireEvent.click(screen.getByRole("button", { name: /shell jobs 1/i }));
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(screen.getByText(/Could not load output/)).toBeVisible();
    expect(screen.getByText("job-9", { selector: "code" })).toBeVisible();
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
    expect(detail.mock.calls.filter(call => call[1] === "job-9")).toHaveLength(2);
    expect(detail.mock.calls.filter(call => call[1] === "job-1")).toHaveLength(1);
    expect(detail.mock.calls.filter(call => call[1] === "job-0")).toHaveLength(1);
  } finally { vi.useRealTimers(); }
});

it("hides cached detail immediately when projection status changes", async () => {
  let resolveNext!: (value: { id: string; status: string; output: string; truncated: boolean; exitCode: number | null }) => void;
  const detail = vi.fn()
    .mockResolvedValueOnce({ id: "job-1", status: "running", output: "old output", truncated: false, exitCode: null })
    .mockImplementationOnce(() => new Promise(resolve => { resolveNext = resolve; }));
  const client = { backgroundBashDetail: detail } as unknown as import("../../src/transport/http-client").RouterHttpClient;
  const { rerender } = render(<BackgroundBashActivity jobs={[{ id: "job-1", status: "running" }]} agentId="one" client={client} />);
  fireEvent.click(screen.getByRole("button", { name: /shell jobs 1/i }));
  expect(await screen.findByText("old output")).toBeVisible();
  rerender(<BackgroundBashActivity jobs={[{ id: "job-1", status: "succeeded" }]} agentId="one" client={client} />);
  expect(screen.queryByText("old output")).not.toBeInTheDocument();
  expect(screen.getByText("Loading output…")).toBeVisible();
  await act(async () => resolveNext({ id: "job-1", status: "succeeded", output: "new output", truncated: false, exitCode: 0 }));
  expect(screen.getByText("new output")).toBeVisible();
  expect(screen.getByText("Exit code: 0")).toBeVisible();
});

it("does not show a terminal exit code under an active projection", async () => {
  const detail = vi.fn().mockResolvedValue({ id: "job-1", status: "succeeded", output: "finished", truncated: false, exitCode: 0 });
  const client = { backgroundBashDetail: detail } as unknown as import("../../src/transport/http-client").RouterHttpClient;
  render(<BackgroundBashActivity jobs={[{ id: "job-1", status: "running" }]} agentId="one" client={client} />);
  fireEvent.click(screen.getByRole("button", { name: /shell jobs 1/i }));
  expect(await screen.findByText("finished")).toBeVisible();
  expect(screen.queryByText(/Exit code:/)).not.toBeInTheDocument();
});
