import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { isAllowedMarkdownLink, markdownToReact, markdownToSafeHtml, textWithoutMarkdownImages } from "../../src/domain/markdown";

afterEach(cleanup);

describe("safe markdown", () => {
  it("allows only safe links", () => {
    expect(isAllowedMarkdownLink("https://example.com")).toBe(true);
    expect(isAllowedMarkdownLink("javascript:alert(1)")).toBe(false);
    expect(isAllowedMarkdownLink("mailto:user@example.com")).toBe(true);
  });

  it("linkifies literal HTTP and HTTPS URLs on the Markdown-it path", () => {
    const html = markdownToSafeHtml("HTTP https://example.com/path and http://localhost:3000/x");
    expect(html).toContain('<a target="_blank" rel="noopener noreferrer" href="https://example.com/path">https://example.com/path</a>');
    expect(html).toContain('<a target="_blank" rel="noopener noreferrer" href="http://localhost:3000/x">http://localhost:3000/x</a>');
  });

  it.each([
    "example.com",
    "user@example.com",
    "ftp://example.com/file",
    "//example.com/path",
    "javascript:alert(1)",
    "data:text/plain,hello",
    "/relative/path",
    "https:/malformed.example",
    "mailto:user@example.com",
    "file:///tmp/example",
  ])("keeps unsafe or non-HTTP text plain: %s", (value) => {
    const html = markdownToSafeHtml(value);
    expect(html).not.toContain("<a ");
    expect(html).toContain(value.replaceAll("&", "&amp;"));

    const { container } = render(markdownToReact(value));
    expect(container.textContent).toBe(value);
  });

  it("does not linkify URLs inside inline or fenced code", () => {
    const html = markdownToSafeHtml("`https://inline.example`\n\n```\nhttps://fenced.example\n```");
    expect(html).not.toContain("<a ");
    expect(html).toContain("https://inline.example");
    expect(html).toContain("https://fenced.example");
  });

  it("preserves explicit safe Markdown links but does not auto-link literal URLs in fallback", () => {
    const html = markdownToSafeHtml(
      "[safe](https://safe.example) https://literal.example and http://localhost:3000/x [unsafe](javascript:alert(1))",
      null,
    );
    expect(html).toContain('<a target="_blank" rel="noopener noreferrer" href="https://safe.example">safe</a>');
    expect(html).not.toContain('href="https://literal.example"');
    expect(html).not.toContain('href="http://localhost:3000/x"');
    expect(html).not.toContain('href="javascript:alert(1)"');
    expect(html).toContain("https://literal.example and http://localhost:3000/x");
  });

  it("keeps rendered images as escaped literal text when linkify is enabled", () => {
    const html = markdownToSafeHtml("![<diagram>](https://example.com/diagram.png?x=1&y=2)");
    expect(html).toContain("![&lt;diagram&gt;](https://example.com/diagram.png?x=1&amp;y=2)");
    expect(html).not.toContain("<img ");
    expect(html).not.toContain("<a ");
  });

  it("escapes fallback content and image syntax", () => {
    expect(markdownToSafeHtml("<script>alert(1)</script> `x`")).not.toContain("<script>");
    expect(textWithoutMarkdownImages("hello ![x](pic.png)")).toBe("hello ");
    expect(markdownToReact("hello")).toBeTruthy();
  });
});
