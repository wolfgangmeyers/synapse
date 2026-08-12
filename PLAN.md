# WebUI Markdown Rendering Plan

## Requirements source

SPEC.md is approved. Supervisor added these explicit requirements after approval:

- Use a small browser Markdown dependency, not ReactMarkdown.
- Keep raw HTML as text.
- Allow only `http`, `https`, and `mailto` links.
- Reject executable URL schemes.
- Test paragraphs, lists, links, fenced code, plain multiline text, unsafe HTML, and unsafe URLs.
- Keep arrival-dialog behavior explicit.
- Preserve attachments.
- Do not touch router batching or mailbox protocol.

## Chosen implementation

Use `markdown-it` as the single Markdown dependency.

Why this dependency:

- It works in a no-build browser app through its distributed browser module.
- It supports common Markdown, including fenced code blocks, without adding a React or bundler dependency.
- It can keep raw HTML disabled, which makes HTML input render as text.
- It exposes `validateLink`, so the WebUI can enforce `http`, `https`, and `mailto` only.

## File changes

### `package.json` and `package-lock.json`

- Add `markdown-it` as a normal dependency.
- Run `npm install` so the lockfile records the exact package and transitive dependencies.
- Avoid unrelated dependency updates.

### `tools/router_web_ui.mjs`

- Add a narrowly scoped static route for `/vendor/markdown-it.js`.
- Serve only the installed `node_modules/markdown-it/dist/markdown-it.js` file.
- Do not expose a general `node_modules` file server.
- Keep existing WebSocket, router polling, message send, batching, and protocol handling unchanged.

### `web/router-ui/index.html`

- Load the Markdown browser bundle from `/vendor/markdown-it.js` before the existing no-build module script for `/app.js`.
- Keep the existing no-build module script for `/app.js`.

### `web/router-ui/markdown.js`

Add a small renderer module:

- Read `MarkdownIt` from the browser bundle's `globalThis.markdownit`; tests pass the package import directly.
- Create one configured parser with:
  - `html: false` so raw HTML is escaped and displayed as text;
  - `linkify: false` so only explicit Markdown links become anchors;
  - `breaks: true` so plain multiline text keeps visible line breaks.
- Add one URL helper used by the parser and tests. It allows only absolute `http:`, `https:`, and `mailto:` URLs, and rejects relative URLs, protocol-relative URLs, malformed URLs, `javascript:`, `data:`, and any other scheme.
- Disable Markdown image rendering so `![alt](url)` remains text-like content and never creates an `<img>` or external image request from agent text. Existing mailbox image attachments remain the only image path.
- Expose `renderMarkdownBody(document, body)`:
  - returns a `.markdown-body` element;
  - parses the string body into safe HTML;
  - assigns the parser output to a detached container;
  - normalizes allowed anchors with `target="_blank"` and `rel="noopener noreferrer"`.
- No app code should pass unsanitized user content directly into `innerHTML`; the only `innerHTML` use for message bodies will be inside this helper after Markdown parsing with raw HTML disabled, image rendering disabled, and link validation enforced.

### `web/router-ui/app.js`

- Import `renderMarkdownBody`.
- Replace the current message body `textContent` node in `renderMessage(message)` with `renderMarkdownBody(document, message.body || "")`.
- Keep attachments below the rendered body exactly as they are now.
- Update the arrival dialog to use the same renderer by replacing its body children with `renderMarkdownBody(document, arrival.body || "")`. This makes the dialog match conversation bubbles and avoids two rendering rules for agent output.

### `web/router-ui/styles.css`

- Move the whitespace behavior from `.bubble` to Markdown body content where needed.
- Add scoped `.markdown-body` styles for paragraphs, lists, anchors, inline code, and fenced code blocks.
- Keep spacing compact so single-line plain text still looks like today.
- Ensure long URLs and words wrap inside bubbles and the arrival dialog.

### `tests/router_web_ui.test.mjs`

Add focused tests for the rendering boundary:

- Import `markdownToSafeHtml` or equivalent pure helper from `web/router-ui/markdown.js`.
- Assert paragraphs produce paragraph tags.
- Assert unordered or ordered lists produce list tags and items.
- Assert Markdown links render as anchors for `http`, `https`, and `mailto`.
- Assert fenced code renders as a pre/code block and escapes code content.
- Assert plain multiline text keeps visible line breaks.
- Assert raw HTML input renders escaped text and does not create a real tag.
- Assert `javascript:`, `data:`, relative URLs, protocol-relative URLs, malformed URLs, and other disallowed schemes do not become anchors.
- Assert Markdown image syntax does not create an `<img>` and does not trigger the attachment renderer.
- Assert the WebUI server serves `/vendor/markdown-it.js` with JavaScript content.
- Update any existing static assertions that currently expect `textContent` for arrival bodies.

## Validation plan

1. Run the targeted WebUI/router tests:

   ```sh
   node --test tests/router_web_ui.test.mjs
   ```

2. Run the full repository test script if targeted tests pass:

   ```sh
   npm test
   ```

3. Run a syntax check through the existing `npm test` check script coverage for changed JavaScript files. If needed, also run direct `node --check` on changed Node-served files.

4. Run coverage using Node's built-in coverage output for the targeted WebUI test path unless the repo has a stronger existing coverage command:

   ```sh
   rm -rf /tmp/webui-markdown-coverage
   NODE_V8_COVERAGE=/tmp/webui-markdown-coverage node --test tests/router_web_ui.test.mjs
   ```

5. For user-visible behavior, start a separate WebUI process from this worktree on a separate port after tests:

   ```sh
   npm run router-web-ui -- --port 0
   ```

   Do not touch the existing WebUI runtime/window. If the separate runtime or browser validation genuinely fails for local-environment reasons, report the failure and use the strongest automated renderer/server tests available.

## Review plan

Run robot-review on this plan with one reviewer covering plan completeness, plan consistency, plan architecture, requirements, security, and scope. Validate any findings with a different persona through pr-validate rules. Iterate until the plan reviewer signs off before implementation.

Implementation review later will include at least requirements, security, test-coverage, scope, and diff-reading focus areas, with pr-validate on findings and a fresh follow-up review after fixes.
