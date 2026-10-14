import { describe, it, expect } from "vitest";
import { parseTemplateTokens, renderTemplate } from "../../src/domain/template-engine";

describe("template-engine", () => {
  describe("parseTemplateTokens", () => {
    it("extracts distinct variable names in order from ${VAR} and {{VAR}}", () => {
      const template = "Hello ${name}, your task is {{ task_id }} and ${name}. Next is {{step-1}} and ${COUNT_2}.";
      expect(parseTemplateTokens(template)).toEqual(["name", "task_id", "step-1", "COUNT_2"]);
    });

    it("handles whitespace in mustache syntax", () => {
      const template = "{{ foo }} {{bar}} {{   baz   }}";
      expect(parseTemplateTokens(template)).toEqual(["foo", "bar", "baz"]);
    });

    it("handles null or undefined or non-string inputs", () => {
      expect(parseTemplateTokens(null)).toEqual([]);
      expect(parseTemplateTokens(undefined)).toEqual([]);
      expect(parseTemplateTokens(123)).toEqual([]);
    });

    it("ignores malformed tokens", () => {
      const template = "${} {{}} ${with spaces} {{with special!}}";
      expect(parseTemplateTokens(template)).toEqual([]);
    });
  });

  describe("renderTemplate", () => {
    it("renders ${VAR} and {{VAR}} when matching keys exist in values", () => {
      const template = "Hi ${name}, welcome to {{ org }}. Role: ${role}.";
      const values = { name: "Alice", org: "Mecha Corp" };
      const output = renderTemplate(template, values);
      expect(output).toBe("Hi Alice, welcome to Mecha Corp. Role: ${role}.");
    });

    it("stringifies values including numbers and falsy values", () => {
      const template = "Count: ${count}, Zero: {{zero}}, Empty: ${empty}, False: {{flag}}";
      const values = { count: 42, zero: 0, empty: "", flag: false };
      expect(renderTemplate(template, values)).toBe("Count: 42, Zero: 0, Empty: , False: false");
    });

    it("leaves unmatched tokens untouched", () => {
      const template = "Missing: ${missing} and {{ missing_mustache }}";
      expect(renderTemplate(template, {})).toBe("Missing: ${missing} and {{ missing_mustache }}");
    });

    it("handles null or undefined template", () => {
      expect(renderTemplate(null, { a: "1" })).toBe("");
      expect(renderTemplate(undefined, { a: "1" })).toBe("");
    });
  });
});
