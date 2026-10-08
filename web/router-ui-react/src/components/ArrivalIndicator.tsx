import type { ReactElement } from "react";
import type { Arrival } from "../domain/arrivals";

export interface ArrivalIndicatorProps { arrival: Arrival; onOpen?: ((arrival: Arrival) => void) | undefined; onDismiss?: ((agent: string) => void) | undefined; header?: boolean | undefined }
export function ArrivalIndicator({ arrival, onOpen, onDismiss, header = false }: ArrivalIndicatorProps): ReactElement {
  const Wrapper = header ? "span" : "div";
  const tooltip = `${arrival.from}: ${arrival.subject || arrival.preview || "New message"}`;
  return <Wrapper className={`arrival-indicator${header ? " header" : ""}`} role="status">
    <button type="button" className="arrival-open" onClick={() => onOpen?.(arrival)} aria-label={`Open message from ${arrival.from}`} title={tooltip}>
      <i className="fa-solid fa-envelope arrival-icon" aria-hidden="true" />
      {!header ? <span className="arrival-copy"><strong>{arrival.from}</strong><small>{arrival.subject || arrival.preview || "New message"}</small></span> : null}
    </button>
    {!header && onDismiss ? <button type="button" className="arrival-dismiss" onClick={() => onDismiss(arrival.agent)} aria-label={`Dismiss message from ${arrival.from}`}>×</button> : null}
  </Wrapper>;
}
