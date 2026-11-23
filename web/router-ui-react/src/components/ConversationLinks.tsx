import { useMemo, useState, type FormEvent, type ReactElement } from "react";
import type { HistoryMessage } from "../domain/history";
import {
  addManualLink,
  clearLinks,
  extractLinks,
  previewUrl,
  removeLink,
  visibleLinks,
  type LinksState,
} from "../domain/links";
import { getLinksState, setLinksState } from "../persistence/links-session";
import { DialogShell } from "./DialogShell";
import { HeaderActionButton } from "./HeaderActionButton";

export interface ConversationLinksProps {
  agentId: string;
  messages: readonly HistoryMessage[];
}

/**
 * Header button + dialog listing links shared in the conversation. The parent
 * must mount it with `key={agentId}` so state never leaks between agents.
 */
export function ConversationLinks({ agentId, messages }: ConversationLinksProps): ReactElement {
  const [open, setOpen] = useState(false);
  const [state, setState] = useState<LinksState>(() => getLinksState(agentId));
  const [adding, setAdding] = useState(false);
  const [url, setUrl] = useState("");
  const [label, setLabel] = useState("");
  const [error, setError] = useState<string | undefined>();

  const extracted = useMemo(() => extractLinks(messages), [messages]);
  const links = useMemo(() => visibleLinks(extracted, state), [extracted, state]);

  const update = (next: LinksState): void => {
    setLinksState(agentId, next);
    setState(next);
  };
  const resetForm = (): void => {
    setAdding(false);
    setUrl("");
    setLabel("");
    setError(undefined);
  };
  const close = (): void => {
    setOpen(false);
    resetForm();
  };
  const submit = (event: FormEvent): void => {
    event.preventDefault();
    const next = addManualLink(state, url, label);
    if (next === undefined) {
      setError("Enter a valid http(s) URL.");
      return;
    }
    update(next);
    resetForm();
  };

  return <>
    <HeaderActionButton
      className="links-header-button"
      aria-label={links.length > 0 ? `Links (${links.length})` : "Links"}
      title={links.length > 0 ? `Links (${links.length})` : "Links"}
      icon="fa-solid fa-link"
      active={links.length > 0}
      activeTone="blue"
      onClick={() => setOpen(true)}
    />
    <DialogShell open={open} title="Links" titleId="links-dialog-title" closeLabel="Close dialog" description="Links shared in this conversation." className="message-dialog links-dialog" onClose={close}>
      {adding ? <form className="links-add-form" onSubmit={submit}>
        <label>URL<input type="text" aria-label="Link URL" placeholder="https://…" value={url} onChange={(event) => setUrl(event.target.value)} /></label>
        <label>Label (optional)<input type="text" aria-label="Link label" value={label} onChange={(event) => setLabel(event.target.value)} /></label>
        {error ? <p role="alert" className="links-error">{error}</p> : null}
        <div className="dialog-actions links-actions">
          <button type="button" className="secondary" onClick={resetForm}>Cancel</button>
          <button type="submit">Save</button>
        </div>
      </form> : <>
        <div className="dialog-actions links-toolbar"><button type="button" onClick={() => setAdding(true)}>+ Add link</button></div>
        {links.length === 0 ? <p className="links-empty">No links yet.</p> : <ul className="links-list">
          {links.map((link) => <li key={link.url} className="links-item">
            <a href={link.url} target="_blank" rel="noopener noreferrer" title={link.url}>
              <span className="links-item-label">{link.label}</span>
              {/* Show the full URL only when the label does not already describe it. */}
              {link.label !== previewUrl(link.url) ? <span className="links-item-url">{link.url}</span> : null}
            </a>
            <button type="button" className="icon-btn-round" aria-label={`Remove link ${link.label}`} title="Remove link" onClick={() => update(removeLink(state, link, extracted))}>
              <i className="fa-solid fa-xmark" aria-hidden="true" />
            </button>
          </li>)}
        </ul>}
        {links.length > 0 ? <div className="dialog-actions links-actions"><button type="button" className="danger-button" onClick={() => update(clearLinks(state, extracted))}>Clear all</button></div> : null}
      </>}
    </DialogShell>
  </>;
}
