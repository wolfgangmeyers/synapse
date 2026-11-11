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
  expect(screen.queryByText(/long-job-id-0/)).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: /shell jobs 40/i }));
  expect(screen.getByRole("button", { name: /shell job 1 running/i })).toBeVisible();
  expect(screen.queryByText(/long-job-id-0/)).not.toBeInTheDocument();
  rerender(<BackgroundBashActivity jobs={[{ id: "job-2", status: "completed" }]} />);
  expect(screen.queryByText(/long-job-id-0/)).not.toBeInTheDocument();
  fireEvent.change(screen.getByLabelText("Filter:"), { target: { value: "failed" } });
  expect(screen.getByRole("button", { name: /shell job 1 completed/i })).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Close dialog" }));
  expect(screen.queryByText(/job-2/)).not.toBeInTheDocument();
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
  fireEvent.change(screen.getByLabelText("Filter:"), { target: { value: "completed" } });
  fireEvent.click(screen.getByRole("button", { name: /shell job 1 succeeded/i }));
  expect(await screen.findByText(/Could not load output/)).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("final")).toBeVisible();
  expect(screen.getByText("Shell job 1: succeeded")).toBeVisible();
  expect(detail).toHaveBeenCalledTimes(2);
});

it("polls only the selected running job while open", async () => {
  vi.useFakeTimers();
  try {
    const detail = vi.fn((_agent: string, id: string) => Promise.resolve({ id, status: "running", output: id, truncated: false, exitCode: null }));
    const client = { backgroundBashDetail: detail } as unknown as import("../../src/transport/http-client").RouterHttpClient;
    render(<BackgroundBashActivity jobs={[{ id: "running", status: "running" }, { id: "done", status: "succeeded" }]} agentId="one" client={client} />);
    fireEvent.click(screen.getByRole("button", { name: /shell jobs 1/i }));
    expect(detail).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /shell job 1 running/i }));
    await act(async () => { await vi.advanceTimersByTimeAsync(2100); });
    expect(detail.mock.calls.filter(call => call[1] === "running").length).toBeGreaterThanOrEqual(2);
    fireEvent.click(screen.getByRole("button", { name: "Back to shell jobs" }));
    fireEvent.change(screen.getByLabelText("Filter:"), { target: { value: "completed" } });
    fireEvent.click(screen.getByRole("button", { name: /shell job 2 succeeded/i }));
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(detail.mock.calls.filter(call => call[1] === "done")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Close dialog" }));
    const count = detail.mock.calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(detail).toHaveBeenCalledTimes(count);
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
  fireEvent.click(screen.getByRole("button", { name: /shell job 1 running/i }));
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
  fireEvent.click(screen.getByRole("button", { name: /shell job 1 running/i }));
  expect(await screen.findByText("finished")).toBeVisible();
  expect(screen.queryByText(/Exit code:/)).not.toBeInTheDocument();
});

it.each(["agent", "client"])("never shows another %s's cached or late output", async (change) => {
  type Value = { id: string; status: string; output: string; truncated: boolean; exitCode: number | null };
  const value = (output: string): Value => ({ id: "same", status: "succeeded", output, truncated: false, exitCode: 0 });
  let resolveOld!: (value: Value) => void;
  let resolveNew!: (value: Value) => void;
  const oldDetail = vi.fn().mockResolvedValueOnce(value("old secret"))
    .mockImplementationOnce(() => new Promise<Value>(resolve => { resolveOld = resolve; }))
    .mockImplementation(() => new Promise<Value>(resolve => { resolveNew = resolve; }));
  const newDetail = vi.fn(() => new Promise<Value>(resolve => { resolveNew = resolve; }));
  const oldClient = { backgroundBashDetail: oldDetail } as unknown as import("../../src/transport/http-client").RouterHttpClient;
  const newClient = change === "client" ? { backgroundBashDetail: newDetail } as unknown as typeof oldClient : oldClient;
  const jobs = [{ id: "same", status: "succeeded" }];
  const { rerender } = render(<BackgroundBashActivity jobs={jobs} agentId="one" client={oldClient} />);
  fireEvent.click(screen.getByRole("button", { name: /shell jobs 0/i }));
  fireEvent.change(screen.getByLabelText("Filter:"), { target: { value: "completed" } });
  fireEvent.click(screen.getByRole("button", { name: /shell job 1 succeeded/i }));
  expect(await screen.findByText("old secret")).toBeVisible();
  // Start a second request for the old scope and leave it in flight.
  fireEvent.click(screen.getByRole("button", { name: "Back to shell jobs" }));
  fireEvent.change(screen.getByLabelText("Filter:"), { target: { value: "completed" } });
  fireEvent.click(screen.getByRole("button", { name: /shell job 1 succeeded/i }));
  rerender(<BackgroundBashActivity jobs={jobs} agentId={change === "agent" ? "two" : "one"} client={newClient} />);
  expect(screen.queryByText("old secret")).not.toBeInTheDocument();
  expect(screen.getByText("Loading output…")).toBeVisible();
  await act(async () => resolveOld(value("late old secret")));
  expect(screen.queryByText("late old secret")).not.toBeInTheDocument();
  await act(async () => resolveNew(value("new output")));
  expect(screen.getByText("new output")).toBeVisible();
  // Returning to the original scope must not resurrect its earlier cached output.
  rerender(<BackgroundBashActivity jobs={jobs} agentId="one" client={oldClient} />);
  expect(screen.queryByText("old secret")).not.toBeInTheDocument();
  expect(screen.queryByText("late old secret")).not.toBeInTheDocument();
});

it.each(["agent", "client"])("hides displayed output immediately on %s change", async change => {
  const detail = vi.fn().mockResolvedValue({ id: "same", status: "succeeded", output: "old output", truncated: false, exitCode: 0 });
  const pending = vi.fn(() => new Promise(() => {}));
  const client = { backgroundBashDetail: detail } as unknown as import("../../src/transport/http-client").RouterHttpClient;
  const other = { backgroundBashDetail: pending } as unknown as typeof client;
  const jobs = [{ id: "same", status: "succeeded" }];
  const { rerender } = render(<BackgroundBashActivity jobs={jobs} agentId="one" client={client} />);
  fireEvent.click(screen.getByRole("button", { name: /shell jobs 0/i }));
  fireEvent.change(screen.getByLabelText("Filter:"), { target: { value: "completed" } });
  fireEvent.click(screen.getByRole("button", { name: /shell job 1 succeeded/i }));
  expect(await screen.findByText("old output")).toBeVisible();
  if (change === "agent") detail.mockImplementation(() => new Promise(() => {}));
  rerender(<BackgroundBashActivity jobs={jobs} agentId={change === "agent" ? "two" : "one"} client={change === "client" ? other : client} />);
  expect(screen.queryByText("old output")).not.toBeInTheDocument();
  expect(screen.getByText("Loading output…")).toBeVisible();
});

it("times out a hung detail request and waits for retry", async () => {
  vi.useFakeTimers();
  try {
    const detail = vi.fn(() => new Promise(() => {}));
    const client = { backgroundBashDetail: detail } as unknown as import("../../src/transport/http-client").RouterHttpClient;
    render(<BackgroundBashActivity jobs={[{ id: "job", status: "running" }]} agentId="one" client={client} />);
    fireEvent.click(screen.getByRole("button", { name: /shell jobs 1/i }));
    fireEvent.click(screen.getByRole("button", { name: /shell job 1 running/i }));
    await act(async () => { await vi.advanceTimersByTimeAsync(10000); });
    expect(screen.getByText(/Could not load output/)).toBeVisible();
    expect(detail).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(detail).toHaveBeenCalledTimes(2);
  } finally { vi.useRealTimers(); }
});

it("filters all statuses and orders dated jobs newest first", () => {
  const jobs = [
    { id: "old", status: "running", started_at: "2026-01-01T00:00:00Z" },
    { id: "new", status: "uncertain", started_at: "2026-01-02T00:00:00Z" },
    { id: "success", status: "succeeded" },
    ...["failed", "killed", "timed_out", "future"].map((status, i) => ({ id: `failed-${i}`, status })),
    { id: "invalid", status: "running", started_at: "invalid" },
  ];
  render(<BackgroundBashActivity jobs={jobs} />);
  fireEvent.click(screen.getByRole("button", { name: /shell jobs 3/i }));
  const filter = screen.getByLabelText("Filter:");
  expect(filter).toHaveValue("active");
  const cards = screen.getAllByRole("listitem");
  expect(cards.map(card => card.textContent)).toEqual([expect.stringContaining("uncertain"), expect.stringContaining("running"), expect.stringContaining("Shell job 8")]);
  fireEvent.change(filter, { target: { value: "completed" } });
  expect(screen.getAllByRole("listitem")).toHaveLength(1);
  expect(screen.getByRole("listitem")).toHaveTextContent("succeeded");
  fireEvent.change(filter, { target: { value: "failed" } });
  expect(screen.getAllByRole("listitem")).toHaveLength(4);
  for (const status of ["failed", "killed", "timed_out", "future"]) expect(screen.getByText(status)).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Close dialog" }));
  fireEvent.click(screen.getByRole("button", { name: /shell jobs 3/i }));
  expect(screen.getByLabelText("Filter:")).toHaveValue("active");
});
