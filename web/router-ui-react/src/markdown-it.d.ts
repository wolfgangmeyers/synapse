declare module "markdown-it" {
  interface MarkdownToken {
    type: string;
    tag: string;
    content: string;
    children?: MarkdownToken[] | undefined;
    attrGet(name: string): string | null;
    attrSet(name: string, value: string): void;
  }
  interface MarkdownRenderer {
    validateLink: (value: string) => boolean;
    render(value: string): string;
    renderer: {
      renderToken(tokens: MarkdownToken[], index: number, options: unknown): string;
      rules: Record<string, (tokens: MarkdownToken[], index: number, options?: unknown, env?: unknown, self?: unknown) => string>;
      utils: { escapeHtml(value: string): string };
    };
  }
  const MarkdownIt: new (options: { html: boolean; breaks: boolean; linkify: boolean }) => MarkdownRenderer;
  export default MarkdownIt;
}
