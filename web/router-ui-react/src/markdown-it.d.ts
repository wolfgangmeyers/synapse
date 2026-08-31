declare module "markdown-it" {
  interface MarkdownToken {
    content: string;
    attrGet(name: string): string | null;
  }
  interface MarkdownRenderer {
    validateLink: (value: string) => boolean;
    render(value: string): string;
    renderer: {
      rules: Record<string, (tokens: MarkdownToken[], index: number) => string>;
      utils: { escapeHtml(value: string): string };
    };
  }
  const MarkdownIt: new (options: { html: boolean; breaks: boolean; linkify: boolean }) => MarkdownRenderer;
  export default MarkdownIt;
}
