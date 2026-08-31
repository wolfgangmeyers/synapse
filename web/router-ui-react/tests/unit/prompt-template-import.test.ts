import { describe, expect, it, vi } from "vitest";
import { planPromptTemplateImport, PromptTemplateImportError, runPromptTemplateImport } from "../../src/domain/prompt-template-import";
import type { PromptTemplate } from "../../src/domain/prompt-templates";

const updatedAt = "2026-08-25T12:34:56.789Z";
const existing: PromptTemplate[] = [
  { id: "alpha-id", name: "Alpha", body: "old alpha" },
  { id: "beta-id", name: "Beta", body: "old beta" },
  { name: "Legacy", body: "old legacy" },
];

describe("prompt template import collision handling", () => {
  it("plans every collision without rewriting a local template", () => {
    const plan = planPromptTemplateImport([
      { id: "alpha-id", name: "Alpha", body: "new alpha", updated_at: updatedAt },
      { id: "alpha-id", name: "Alpha", body: "stale alpha" },
      { id: "other-alpha-id", name: "Alpha", body: "renamed alpha" },
      { name: "Alpha", body: "name-only alpha" },
      { id: "alpha-id", name: "Beta", body: "beta under alpha id", updated_at: updatedAt },
      { id: "new-id", name: "New", body: "new body" },
      { name: "New without id", body: "new body" },
      { id: "legacy-id", name: "Legacy", body: "legacy collision" },
    ], existing);

    expect(plan.map(({ action, id, reason }) => ({ action, id, reason }))).toEqual([
      { action: "update", id: "alpha-id", reason: undefined },
      { action: "skip", id: undefined, reason: "missing_timestamp" },
      { action: "skip", id: undefined, reason: "name_conflict" },
      { action: "skip", id: undefined, reason: "name_conflict" },
      { action: "skip", id: undefined, reason: "name_conflict" },
      { action: "create", id: undefined, reason: undefined },
      { action: "create", id: undefined, reason: undefined },
      { action: "skip", id: undefined, reason: "name_conflict" },
    ]);
  });

  it("continues after a duplicate before a new template and reports counts", async () => {
    const create = vi.fn(async () => undefined);
    const update = vi.fn(async () => undefined);
    const refresh = vi.fn(async () => undefined);
    const result = await runPromptTemplateImport(
      [
        { id: "alpha-id", name: "Alpha", body: "stale alpha" },
        { name: "New", body: "new body" },
      ],
      true,
      { create, update, refresh },
      undefined,
      { existingTemplates: existing },
    );

    expect(result).toEqual({ imported: 1, skipped: 1 });
    expect(create).toHaveBeenCalledWith({ name: "New", body: "new body" });
    expect(update).not.toHaveBeenCalled();
    expect(refresh).toHaveBeenCalledOnce();
  });

  it("updates timestamped matching IDs and skips timestamp-less matching IDs", async () => {
    const create = vi.fn(async () => undefined);
    const update = vi.fn(async () => undefined);
    const refresh = vi.fn(async () => undefined);
    const result = await runPromptTemplateImport(
      [
        { id: "alpha-id", name: "Alpha", body: "new alpha", updated_at: updatedAt },
        { id: "beta-id", name: "Beta", body: "stale beta" },
      ],
      true,
      { create, update, refresh },
      undefined,
      { existingTemplates: existing },
    );

    expect(result).toEqual({ imported: 1, skipped: 1 });
    expect(update).toHaveBeenCalledWith("alpha-id", { name: "Alpha", body: "new alpha", updated_at: updatedAt });
    expect(create).not.toHaveBeenCalled();
  });

  it("counts only actual creates against capacity", async () => {
    const capacity = Array.from({ length: 99 }, (_, index) => ({ id: `id-${index}`, name: `Existing ${index}`, body: "body" }));
    const create = vi.fn(async () => undefined);
    const update = vi.fn(async () => undefined);
    const refresh = vi.fn(async () => undefined);
    const result = await runPromptTemplateImport(
      [
        { id: "id-0", name: "Existing 0", body: "ignored" },
        { name: "One new template", body: "body" },
      ],
      true,
      { create, update, refresh },
      undefined,
      { existingTemplates: capacity },
    );

    expect(result).toEqual({ imported: 1, skipped: 1 });
    expect(create).toHaveBeenCalledOnce();
  });

  it("skips a race-time name conflict and continues the batch", async () => {
    const create = vi.fn()
      .mockRejectedValueOnce({ status: 409, code: "prompt_template_name_conflict" })
      .mockResolvedValueOnce(undefined);
    const update = vi.fn(async () => undefined);
    const refresh = vi.fn(async () => undefined);
    const result = await runPromptTemplateImport(
      [
        { name: "Raced", body: "body" },
        { name: "After race", body: "body" },
      ],
      true,
      { create, update, refresh },
    );

    expect(result).toEqual({ imported: 1, skipped: 1 });
    expect(create).toHaveBeenCalledTimes(2);
    expect(refresh).toHaveBeenCalledOnce();
  });

  it("stops and reports non-conflict failures", async () => {
    const create = vi.fn().mockRejectedValueOnce(new Error("Router unavailable"));
    const update = vi.fn(async () => undefined);
    const refresh = vi.fn(async () => undefined);
    const progress: Array<{ completed: number; total: number; interrupted: boolean }> = [];

    const importRun = runPromptTemplateImport(
      [{ name: "Failed", body: "body" }, { name: "After failure", body: "body" }],
      true,
      { create, update, refresh },
      (value) => progress.push(value),
    );
    await expect(importRun).rejects.toBeInstanceOf(PromptTemplateImportError);
    await expect(importRun).rejects.toMatchObject({
      completed: 0,
      total: 2,
      imported: 0,
      skipped: 0,
      message: "Import stopped after 0 of 2 templates. Router unavailable",
    });

    expect(create).toHaveBeenCalledOnce();
    expect(progress.at(-1)).toEqual({ completed: 0, total: 2, interrupted: true });
    expect(refresh).toHaveBeenCalledOnce();
    expect(update).not.toHaveBeenCalled();
  });
});
