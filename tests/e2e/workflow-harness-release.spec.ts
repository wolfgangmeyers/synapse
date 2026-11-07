import { test, expect } from "@playwright/test";
import { createRunningWorkflow, workflowHarness } from "./workflow-implicit-goals-harness";

for (const destroy of [false, true]) {
  test(`provider barrier release reports destroyed=${destroy}`, async ({ page }) => {
    test.setTimeout(90_000);
    let release!: () => void;
    const barrier = new Promise<void>(resolve => { release = resolve; });
    const observed: boolean[] = [];
    const f = await workflowHarness({ initialReplies: [{ answer: "Barrier response", barrier,
      onRelease: destroyed => { observed.push(destroyed); } }] });
    try {
      await createRunningWorkflow(f, page, 600000, false);
      await expect.poll(() => f.requests.length).toBe(1);
      expect(observed).toEqual([]);
      if (destroy) f.releaseHeld();
      release();
      await expect.poll(() => observed).toEqual([destroy]);
      if (!destroy) await expect(page.getByText("Barrier response", { exact: true }).first()).toBeVisible();
      expect(f.violations).toEqual([]);
    } finally {
      release();
      await f.close();
    }
  });
}

for (const destroy of [false, true]) {
  test(`held evaluator 503 reports destroyed=${destroy} and returns failure when live`, async () => {
    test.setTimeout(90_000);
    let release!: () => void;
    const barrier = new Promise<void>(resolve => { release = resolve; });
    const observed: boolean[] = [];
    const f = await workflowHarness({ initialReplies: [{ failure: 503, evaluation: true, barrier,
      onRelease: destroyed => { observed.push(destroyed); } }] });
    try {
      const response = fetch(f.providerUrl, {
        method: "POST",
        signal: AbortSignal.timeout(5000),
        headers: { authorization: "Bearer .terra-test-secret", "chatgpt-account-id": "terra-test-account" },
        body: JSON.stringify({ model: "gpt-6.1-terra", instructions: "Goal: evaluate" }),
      }).then(async res => ({ status: res.status, body: await res.json() }), () => null);
      await expect.poll(() => f.requests.length).toBe(1);
      expect(observed).toEqual([]);
      if (destroy) f.releaseHeld();
      release();
      await expect.poll(() => observed).toEqual([destroy]);
      if (destroy) expect(await response).toBeNull();
      else expect(await response).toEqual({ status: 503, body: { error: { message: "deterministic evaluator failure", type: "server_error" } } });
      expect(f.violations).toEqual([]);
    } finally {
      release();
      await f.close();
    }
  });
}
