import { test, expect } from "@playwright/test";
import { providerFixture } from "./antigravity-provider-fixture";

test("Gemini 4 multi-turn tool calling preserves authentic thought signature across turns through router-proxy and managed runtime", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await providerFixture();
  try {
    await f.open(page);

    const testCases = [
      { provider: "google", id: "gemini-4-hydrogen" },
      { provider: "google-vertex", id: "gemini-4-argon" },
      { provider: "google-vertex", id: "gemini-4-carbon" },
      { provider: "google-vertex", id: "gemini-3.8-flash" },
    ];

    for (const [i, model] of testCases.entries()) {
      const name = `g4-turn-${i}`;
      const answer = `Completed turn for ${model.id}`;
      const authenticThoughtSignature = `authentic_sig_${model.id}_${Date.now()}`;
      const start = f.turns.length;

      // Turn 1: model returns tool call with authentic thoughtSignature
      // Turn 2: model sees tool output in context with thoughtSignature replayed, returns final answer
      f.replies.push(
        {
          tool: "read",
          args: { path: "note.txt" },
          thoughtSignature: authenticThoughtSignature,
        } as any,
        { answer }
      );

      await f.api("/v1/managed-agents", {
        name,
        cwd: f.work,
        runtime: "embedded",
        model,
        initial_instruction: "Read note.txt and complete.",
      });

      await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${name}"]`).click();
      await expect(page.getByText(answer, { exact: true })).toBeVisible({ timeout: 20_000 });

      // Verify recorded WebSocket frame
      await expect.poll(() =>
        f.frames.some((text) => {
          try {
            const frame = JSON.parse(text);
            return (
              frame.type === "history_entry" &&
              frame.entry?.agent_id === name &&
              frame.entry.message_content === answer
            );
          } catch {
            return false;
          }
        })
      ).toBe(true);

      expect(f.turns.slice(start)).toHaveLength(2);
      const [firstTurn, secondTurn] = f.turns.slice(start);

      // Verify request routes
      if (model.provider === "google") {
        expect(firstTurn.path).toContain("/gemini/v1beta/models/" + model.id);
        expect(secondTurn.path).toContain("/gemini/v1beta/models/" + model.id);
      } else {
        expect(firstTurn.path).toContain("/vertex/v1/projects/agr-project/locations/global/");
        expect(firstTurn.path).toContain(model.id + ":streamGenerateContent");
        expect(secondTurn.path).toContain("/vertex/v1/projects/agr-project/locations/global/");
      }

      // Verify the second turn replayed the authentic thoughtSignature to the model
      const contents = secondTurn.body.contents as any[];
      expect(contents).toBeDefined();

      let foundFunctionCallWithSignature = false;
      for (const turn of contents) {
        if (turn.role === "model" && Array.isArray(turn.parts)) {
          for (const part of turn.parts) {
            if (part.functionCall) {
              expect(part.thoughtSignature).toBe(authenticThoughtSignature);
              expect(part.thought_signature).toBe(authenticThoughtSignature);
              expect(part.thoughtSignature).not.toBe("skip_thought_signature_validator");
              foundFunctionCallWithSignature = true;
            }
          }
        }
      }
      expect(foundFunctionCallWithSignature).toBe(true);

      // Verify tool result content was provided in second turn
      const wire = JSON.stringify(secondTurn.body);
      expect(wire).toContain("functionResponse");
      expect(wire).toContain("AGR real tool evidence");
    }

    // Additional coverage: Multi-turn chaining across multiple sequential tool calls with distinct signatures
    {
      const chainedName = "g4-chained-multi";
      const chainedModel = { provider: "google-vertex", id: "gemini-4-hydrogen" };
      const sig1 = `sig_chain_1_${Date.now()}`;
      const sig2 = `sig_chain_2_${Date.now()}`;
      const chainedAnswer = "Chained multi-turn complete";
      const startChained = f.turns.length;

      // Turn 1: tool call 1 with sig1
      // Turn 2: tool call 2 with sig2
      // Turn 3: final answer
      f.replies.push(
        { tool: "read", args: { path: "note.txt" }, thoughtSignature: sig1 } as any,
        { tool: "read", args: { path: "note.txt" }, thoughtSignature: sig2 } as any,
        { answer: chainedAnswer }
      );

      await f.api("/v1/managed-agents", {
        name: chainedName,
        cwd: f.work,
        runtime: "embedded",
        model: chainedModel,
        initial_instruction: "Read note.txt twice sequentially and complete.",
      });

      await expect(page.locator(`[data-agent-id="${chainedName}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${chainedName}"]`).click();
      await expect(page.getByText(chainedAnswer, { exact: true })).toBeVisible({ timeout: 20_000 });

      expect(f.turns.slice(startChained)).toHaveLength(3);
      const [turn1, turn2, turn3] = f.turns.slice(startChained);

      // In turn 2, context contains turn 1's function call with sig1
      const turn2Contents = turn2.body.contents as any[];
      let foundSig1InTurn2 = false;
      for (const turn of turn2Contents) {
        if (turn.role === "model" && Array.isArray(turn.parts)) {
          for (const part of turn.parts) {
            if (part.functionCall && part.thoughtSignature === sig1) {
              expect(part.thought_signature).toBe(sig1);
              foundSig1InTurn2 = true;
            }
          }
        }
      }
      expect(foundSig1InTurn2).toBe(true);

      // In turn 3, context contains turn 1's function call with sig1 and turn 2's function call with sig2
      const turn3Contents = turn3.body.contents as any[];
      const observedSigs: string[] = [];
      for (const turn of turn3Contents) {
        if (turn.role === "model" && Array.isArray(turn.parts)) {
          for (const part of turn.parts) {
            if (part.functionCall && part.thoughtSignature) {
              observedSigs.push(part.thoughtSignature);
              expect(part.thought_signature).toBe(part.thoughtSignature);
              expect(part.thoughtSignature).not.toBe("skip_thought_signature_validator");
            }
          }
        }
      }
      expect(observedSigs).toContain(sig1);
      expect(observedSigs).toContain(sig2);
    }

    // Additional coverage: Scenario 7 verification of omission of dummy signatures over the wire
    // When a tool call returns without thought signature (e.g. empty or undefined),
    // verifying that over the wire thoughtSignature and thought_signature are omitted / undefined
    // and NOT populated with "skip_thought_signature_validator" for both Gemini 4 and Gemini 3.8 Flash.
    const emptySigCases = [
      { provider: "google", id: "gemini-4-hydrogen", name: "g4-empty-sig-omission" },
      { provider: "google-vertex", id: "gemini-3.8-flash", name: "g38-empty-sig-omission" },
    ];

    for (const emptyCase of emptySigCases) {
      const emptySigName = emptyCase.name;
      const emptySigModel = { provider: emptyCase.provider, id: emptyCase.id };
      const emptySigAnswer = `Empty signature omission verified for ${emptyCase.id}`;
      const startEmptySig = f.turns.length;

      // Turn 1: tool call with empty/omitted thoughtSignature
      // Turn 2: final answer
      f.replies.push(
        { tool: "read", args: { path: "note.txt" } } as any,
        { answer: emptySigAnswer }
      );

      await f.api("/v1/managed-agents", {
        name: emptySigName,
        cwd: f.work,
        runtime: "embedded",
        model: emptySigModel,
        initial_instruction: "Read note.txt without signature.",
      });

      await expect(page.locator(`[data-agent-id="${emptySigName}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${emptySigName}"]`).click();
      await expect(page.getByText(emptySigAnswer, { exact: true })).toBeVisible({ timeout: 20_000 });

      expect(f.turns.slice(startEmptySig)).toHaveLength(2);
      const [, secondTurnEmpty] = f.turns.slice(startEmptySig);

      const contents = secondTurnEmpty.body.contents as any[];
      expect(contents).toBeDefined();

      let foundFunctionCallWithoutDummySignature = false;
      for (const turn of contents) {
        if (turn.role === "model" && Array.isArray(turn.parts)) {
          for (const part of turn.parts) {
            if (part.functionCall) {
              expect(part.thoughtSignature).toBeUndefined();
              expect(part.thought_signature).toBeUndefined();
              expect(JSON.stringify(part)).not.toContain("skip_thought_signature_validator");
              foundFunctionCallWithoutDummySignature = true;
            }
          }
        }
      }
      expect(foundFunctionCallWithoutDummySignature).toBe(true);
    }

    expect(f.replies).toHaveLength(0);
    expect(f.violations).toEqual([]);
  } finally {
    await f.close();
  }
});
