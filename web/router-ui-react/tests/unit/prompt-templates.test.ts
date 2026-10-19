import { describe, expect, it } from "vitest";
import {
  parsePromptTemplateImport,
  parsePromptTemplateTokens,
  promptTemplateCreatePayload,
  promptTemplateExportPayload,
  promptTemplateUpdatePayload,
  renderPromptTemplate,
} from "../../src/domain/prompt-templates";

describe("prompt templates", () => {
  it("round trips portable export", () => {
    const templates = [{ name: "x", body: "Use ${file}", id: "id" }];
    expect(parsePromptTemplateImport(JSON.stringify(promptTemplateExportPayload(templates)))).toEqual(templates);
  });

  it("keeps token order and unknown tokens", () => {
    expect(parsePromptTemplateTokens("${b} ${a} ${b}")).toEqual(["b", "a"]);
    expect(renderPromptTemplate("${a} ${missing} ${bad", { a: "ok" })).toBe("ok ${missing} ${bad");
  });

  it("rejects invalid imports", () => {
    expect(() => parsePromptTemplateImport(JSON.stringify({ format: "wrong", version: 1, templates: [] }))).toThrow();
  });

  it("promptTemplateUpdatePayload never includes id and only includes name, body, and canonical updated_at", () => {
    const payload = promptTemplateUpdatePayload({
      id: "pr-review",
      name: "PR Review",
      body: "Review PR code",
      updated_at: "2030-01-01T00:00:00.000Z",
    });
    expect(payload).toEqual({
      name: "PR Review",
      body: "Review PR code",
      updated_at: "2030-01-01T00:00:00.000Z",
    });
    expect("id" in payload).toBe(false);

    // Omit updated_at when not canonical
    const noTimestamp = promptTemplateUpdatePayload({
      id: "code-review",
      name: "Code Review",
      body: "Review this",
      updated_at: "invalid",
    });
    expect(noTimestamp).toEqual({
      name: "Code Review",
      body: "Review this",
    });
    expect("id" in noTimestamp).toBe(false);
  });

  it("promptTemplateCreatePayload includes id, name, body, and canonical updated_at", () => {
    const withId = promptTemplateCreatePayload({
      id: "custom-slug",
      name: "Custom Slug",
      body: "Body",
      updated_at: "2030-01-01T00:00:00.000Z",
    });
    expect(withId).toEqual({
      id: "custom-slug",
      name: "Custom Slug",
      body: "Body",
      updated_at: "2030-01-01T00:00:00.000Z",
    });

    const generatedSlug = promptTemplateCreatePayload({
      name: "Review Pull Request",
      body: "Body",
    });
    expect(generatedSlug).toEqual({
      id: "review-pull-request",
      name: "Review Pull Request",
      body: "Body",
    });
  });
});
