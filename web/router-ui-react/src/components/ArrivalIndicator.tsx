import type { ReactElement } from "react";
import type { Arrival } from "../domain/arrivals";

export interface ArrivalIndicatorProps { arrival: Arrival; onOpen?: ((arrival: Arrival) => void) | undefined; onDismiss?: ((agent: string) => void) | undefined; header?: boolean | undefined }
export function ArrivalIndicator({ arrival, onOpen, header = false }: ArrivalIndicatorProps): ReactElement {
  const Wrapper = header ? "span" : "div";
  const tooltip = `${arrival.from}: ${arrival.subject || arrival.preview || "New message"}`;
  return <Wrapper className={`arrival-indicator${header ? " header" : ""}`} role="status">
    <button type="button" className="arrival-open" onClick={() => onOpen?.(arrival)} aria-label={`Open message from ${arrival.from}`} title={tooltip}>
      <i className="fa-solid fa-envelope arrival-icon" aria-hidden="true" />
    </button>
  </Wrapper>;
}
