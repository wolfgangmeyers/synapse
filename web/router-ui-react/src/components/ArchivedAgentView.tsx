import type { ReactElement } from "react";
import type { ManagedAgentArchive } from "../protocol/types";

export function ArchivedAgentView({ archive }: { archive: ManagedAgentArchive }): ReactElement {
  return <section className="archived-agent-view" aria-labelledby="archived-agent-title">
    <h2 id="archived-agent-title">Archived worker history</h2>
    <p><strong>{archive.name}</strong> is archived.</p>
    <p>Completed {new Date(archive.completed_at).toLocaleString()} with outcome {archive.terminal_outcome}.</p>
    <p>Delivery: {archive.delivery.disposition}. {archive.delivery.total} message{archive.delivery.total === 1 ? "" : "s"}.</p>
    <p>No session or conversation is available for this worker.</p>
  </section>;
}
