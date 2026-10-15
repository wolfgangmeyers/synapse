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

    it("extracts tokens recursively across nested templates", () => {
      const templates = {
        child1: "Child 1: ${child_var} and {{ shared_var }} and {{ .child2 }}",
        child2: "Child 2: ${sub_child_var}",
      };
      const root = "Root: ${root_var} and {{ .child1 }}";
      expect(parseTemplateTokens(root, templates)).toEqual([
        "root_var",
        "child_var",
        "shared_var",
        "sub_child_var",
      ]);
    });

    it("avoids infinite loops when extracting tokens across cyclic templates", () => {
      const templates = {
        a: "A var: ${var_a} includes {{ .b }}",
        b: "B var: ${var_b} includes {{ .a }}",
      };
      const root = "{{ .a }}";
      expect(parseTemplateTokens(root, templates)).toEqual(["var_a", "var_b"]);
    });

    it("works with Map as templates parameter", () => {
      const templates = new Map([
        ["snippet", "Snippet: ${from_snippet}"],
      ]);
      const root = "{{ .snippet }}";
      expect(parseTemplateTokens(root, templates)).toEqual(["from_snippet"]);
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

    it("expands basic inclusion {{ .snippet }}", () => {
      const templates = {
        snippet: "Hello from snippet!",
      };
      const output = renderTemplate("Prefix {{ .snippet }} suffix", {}, templates);
      expect(output).toBe("Prefix Hello from snippet! suffix");
    });

    it("expands dot prefix with optional whitespace around dot/id {{.snippet}}", () => {
      const templates = {
        snippet: "expanded",
      };
      expect(renderTemplate("{{.snippet}}", {}, templates)).toBe("expanded");
      expect(renderTemplate("{{   .snippet   }}", {}, templates)).toBe("expanded");
      expect(renderTemplate("{{ .   snippet }}", {}, templates)).toBe("expanded");
    });

    it("outputs [Missing template: <template-id>] when template is not found", () => {
      const output = renderTemplate("Before {{ .missing }} After", {}, {});
      expect(output).toBe("Before [Missing template: missing] After");
    });

    it("handles recursive inclusion (A includes B which has variables)", () => {
      const templates = {
        header: "HEADER: ${TITLE}",
        footer: "FOOTER: ${AUTHOR}",
        body: "CONTENT:\n{{ .header }}\nMain text\n{{ .footer }}",
        nested: "START -> {{ .body }} -> END",
      };
      const values = {
        TITLE: "My Document",
        AUTHOR: "Wolfgang",
      };
      const output = renderTemplate("{{ .nested }}", values, templates);
      expect(output).toBe("START -> CONTENT:\nHEADER: My Document\nMain text\nFOOTER: Wolfgang -> END");
    });

    it("detects template cycles and outputs [Template cycle detected: a -> b -> a]", () => {
      const templates = {
        a: "A includes {{ .b }}",
        b: "B includes {{ .c }}",
        c: "C includes {{ .a }}",
      };
      const output = renderTemplate("{{ .a }}", {}, templates);
      expect(output).toBe("A includes B includes C includes [Template cycle detected: a -> b -> c -> a]");

      const selfTemplates = {
        self: "Self {{ .self }}",
      };
      const selfOutput = renderTemplate("{{ .self }}", {}, selfTemplates);
      expect(selfOutput).toBe("Self [Template cycle detected: self -> self]");
    });

    it("enforces max recursion depth and outputs [Max template depth exceeded]", () => {
      const templates: Record<string, string> = {};
      for (let i = 1; i <= 15; i++) {
        templates[`t${i}`] = `{{ .t${i + 1} }}`;
      }
      const output = renderTemplate("{{ .t1 }}", {}, templates);
      expect(output).toBe("[Max template depth exceeded]");
    });

    it("supports variable passthrough from parent to child", () => {
      const templates = {
        base: "Base: ${COMMON_VAR} and {{ COMMON_VAR }}",
        wrapper: "Wrapper: {{ .base }}",
      };
      const values = {
        COMMON_VAR: "RESOLVED_VALUE",
      };
      const output = renderTemplate("{{ .wrapper }}", values, templates);
      expect(output).toBe("Wrapper: Base: RESOLVED_VALUE and RESOLVED_VALUE");
    });

    it("supports templates passed as a Map", () => {
      const templates = new Map([
        ["greeting", "Hi ${user}!"],
      ]);
      const output = renderTemplate("Message: {{ .greeting }}", { user: "Wolfgang" }, templates);
      expect(output).toBe("Message: Hi Wolfgang!");
    });
  });
});

