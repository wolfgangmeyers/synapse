# Visual parity checklist

Compare the React page with the supported page using the same isolated Go router, deterministic fixture data, and stable timestamps. Run the supported React page on its own temporary loopback port. The React page connects directly to the isolated Go URL. Do not connect either page to the live router during this test slice.

## Mechanical checks

- Text in `index.html` and visible labels match the reference behavior.
- Control names, dialog titles, empty states, error text, and accessibility names are present.
- The React page connects directly to the Go router at runtime.
- Both pages use the same fixture agent IDs, message IDs, attachments, timestamps, activity states, model catalogs, goals, reminders, arrivals, and lifecycle data.
- Differences are recorded with the source-backed reason. Do not add behavior to make a screenshot match.

## Desktop checks

At the reported desktop viewport, compare:

- brand header, router selector, connection status, refresh control, count, and filter;
- connected, working, compacting, selected, and arrival-indicator agent rows;
- subagent count and descriptions;
- goal and reminder indicators;
- selected conversation header, loading, empty, and error states;
- incoming, outgoing, pending, failed, and canonical message bubbles;
- reply footer, Markdown, links, code fallback, attachment previews, and galleries;
- composer, image controls, model picker, goal editor, reminder dialog, prompt-template dialog, arrival dialog, and router manager;
- keyboard focus rings, dialog spacing, scroll position, and follow-to-bottom behavior.

## Narrow checks

Use an external browser/device-emulation session when BCB reports that outer-window resizing does not change `window.innerWidth`. Verify the `820px` breakpoint, `35dvh` mobile sidebar, one-column layout, dialog width, text wrapping, message scrolling, composer position, focus visibility, and attachment sizing.

## Shared visual criteria

- Preserve the CSS custom properties, colors, typography, border radii, grid widths, animation treatments, and dark dialog spacing from the approved reference.
- Preserve the responsive behavior at desktop and the verified narrow viewport.
- Preserve visible labels, icon meaning, focus behavior, keyboard behavior, and state colors.
- DOM structure may differ when the rendered layout and interaction behavior remain materially equivalent.
- Capture screenshots for the sidebar, status card, agent states, conversation states, every dialog, and loading/error/empty states.
- Record the actual viewport with each screenshot and note any intentional difference beside its source-backed reason.
