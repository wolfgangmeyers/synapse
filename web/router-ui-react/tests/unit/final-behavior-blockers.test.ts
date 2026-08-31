import { describe, expect, it, vi } from "vitest";
import { markdownToSafeHtml } from "../../src/domain/markdown";
import { runPromptTemplateImport } from "../../src/domain/prompt-template-import";

describe("final behavior blockers", () => {
  it("keeps legacy markdown structure while sanitizing links and images", () => {
    const html = markdownToSafeHtml("# Heading\n\n- one\n- two\n\n> quote\n\n```ts\n<code>\n```\n\n[good](https://example.com) [bad](javascript:alert(1)) ![photo](/v1/attachments/photo.jpg)");
    expect(html).toContain("<h1>Heading</h1>");
    expect(html).toContain("<ul>");
    expect(html).toContain("<blockquote>");
    expect(html).toContain("<pre><code");
    expect(html).toContain("&lt;code&gt;");
    expect(html).toContain('href="https://example.com"');
    expect(html).toContain("[photo](/v1/attachments/photo.jpg)");
    expect(html).not.toContain('href="javascript:');
  });

  it("times out each prompt import and reports completed templates", async () => {
    vi.useFakeTimers();
    try {
      let creates = 0;
      const create = vi.fn(async () => { creates += 1; if (creates > 1) await new Promise<void>(() => undefined); });
      const update = vi.fn(async () => undefined);
      const refresh = vi.fn(async () => undefined);
      const progress: Array<{ completed: number; total: number; interrupted: boolean }> = [];
      const pending = runPromptTemplateImport(
        [{ name: "one", body: "1" }, { name: "two", body: "2" }],
        false,
        { create, update, refresh },
        (value) => progress.push(value),
        { timeoutMs: 15 },
      );
      const rejection = expect(pending).rejects.toMatchObject({ completed: 1, total: 2 });
      await vi.advanceTimersByTimeAsync(15);
      await rejection;
      expect(refresh).toHaveBeenCalledOnce();
      expect(progress).toEqual([
        { completed: 0, total: 2, interrupted: false },
        { completed: 1, total: 2, interrupted: false },
        { completed: 1, total: 2, interrupted: true },
      ]);
    } finally {
      vi.useRealTimers();
    }
  });
});
