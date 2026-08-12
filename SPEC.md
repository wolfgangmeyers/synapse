# WebUI Markdown Rendering Spec

## Scope

Update the router WebUI conversation surface so agent messages render safe Markdown. The app in this worktree is the plain browser UI under `web/router-ui`; it does not use React and does not already have `ReactMarkdown` or another Markdown renderer.

This spec covers only rendered message text in the WebUI. It does not change router batching, mailbox protocol frames, delivery semantics, message storage, or image attachment handling.

## Relevant existing behavior

- Conversation messages render in `web/router-ui/app.js` through `renderMessage(message)`.
- Message bodies are currently assigned with `text.textContent = message.body`, and `.bubble` uses `white-space: pre-wrap` so plain multiline text stays readable.
- New-message arrival dialog bodies are also rendered as plain text with `elements.arrivedDialogBody.textContent = arrival.body || ""`.
- The WebUI is served directly by `tools/router_web_ui.mjs`; there is no frontend build step.
- KB search found router/WebUI operational notes about connected-agent reconciliation and stale router processes, but no existing WebUI Markdown-rendering decision.

## Behavior

### Agent message bubbles

Incoming agent output in the conversation view renders common Markdown:

- paragraphs become separate blocks;
- unordered and ordered lists render as lists;
- Markdown links render as clickable anchors;
- fenced code blocks render as preformatted code blocks.

Plain text remains readable:

- normal text without Markdown still appears as text;
- single-line messages do not gain distracting extra spacing;
- multiline plain text keeps visible line breaks;
- long words and URLs still wrap inside the bubble.

Rendering stays safe:

- raw HTML in a message is displayed as text, not interpreted as DOM;
- scriptable or unsafe link targets do not become executable links;
- no `dangerouslySetInnerHTML`-style raw injection is introduced without sanitization.

Outgoing web-user messages may use the same renderer for consistency, but the acceptance criterion is agent text output. If implementation can keep one shared message renderer without special-casing direction, that is preferred.

### Arrival dialog

The new-message arrival dialog should use the same safe body renderer as conversation bubbles if doing so is straightforward. If it remains plain text in this pass, that should be called out in the plan because the task's core wording is agent text output in the WebUI conversation.

### Attachments

Image attachment rendering stays unchanged. Text rendering remains the first child of the bubble, and any image gallery stays below the text.

## High-level design

Add a small Markdown rendering layer at the WebUI boundary:

1. Add the smallest suitable Markdown dependency to `package.json` and `package-lock.json`.
2. Expose the browser module through the existing WebUI static server without adding a bundler or broad static access to `node_modules`.
3. Add a `renderMessageBody(body)` helper in the WebUI that returns DOM nodes for safe Markdown output.
4. Use that helper from `renderMessage(message)` and, if practical, the arrival dialog.
5. Add CSS for Markdown body elements inside bubbles/dialogs so paragraphs, lists, links, and code blocks fit the existing visual design.
6. Add focused tests that prove the dependency is wired safely and that plain text is preserved.

The renderer should produce DOM from sanitized Markdown output. If the chosen library can disable raw HTML before rendering, use that setting. Link handling should allow ordinary `http`, `https`, and `mailto` links and avoid executable schemes.

## Alternatives considered

### 1. Use ReactMarkdown

This is not a fit. The current WebUI is plain JavaScript served directly from `web/router-ui`; there is no React app, JSX, or existing ReactMarkdown dependency. Adding React only for message bodies would add a framework and build problem for a small rendering change.

### 2. Add a full frontend build pipeline

This would make dependency imports cleaner, but it is too much scope. The WebUI currently has no bundler. A build step would create unrelated churn in scripts, serving, tests, and local runtime behavior.

### 3. Write a small Markdown parser by hand

This would avoid a dependency, but it is the wrong risk tradeoff. Lists, fenced code, links, escaping, and unsafe HTML behavior are easy to get subtly wrong. The task explicitly allows adding the smallest appropriate Markdown dependency when no existing renderer exists.

### 4. Add a small Markdown library and serve only its browser module

This is the chosen approach. It keeps the existing no-build WebUI shape, adds one focused dependency and lockfile update, and lets a maintained parser handle Markdown edge cases. The server should expose only the exact browser module needed by the app, not a general `node_modules` file server.

## Success criteria

- Agent messages in the WebUI render paragraphs, lists, links, and fenced code blocks as Markdown.
- Plain text messages remain readable, including multiline text.
- Raw HTML is not executed or inserted as trusted DOM.
- Tests cover Markdown rendering and plain text preservation.
- The diff stays scoped to WebUI rendering, styling, tests, and the minimal dependency/lockfile changes needed for the renderer.
