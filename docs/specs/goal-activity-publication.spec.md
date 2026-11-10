# Goal evaluation activity publication

Status: Draft; acceptance testing pending.

## Scenarios

- A current completed goal evaluation appears in activity, including its verdict and evaluator detail. Repeated publication of the same evaluation does not duplicate it.
- Consecutive NOT_DONE evaluations with the same parsed reason produce one visible activity, even if raw evaluator output differs. A changed reason or intervening completed verdict remains visible.
- An evaluation from a superseded goal revision or stale owner cannot publish activity. A goal that disables itself on completion may publish its completion.
- After interruption, eligible completed evaluations are recovered once; display-history eviction must not resurrect already handled evaluations. Publication markers are retained only as long as their evaluation evidence and cascade on its deletion.
- On upgrade, migration 128 marks all completed legacy evidence handled, including evidence whose display row is missing. Ambiguous pre-upgrade interrupted publication is therefore not recovered. The migration's ledger schema and backfill semantics remain unchanged.
- A database failure rolls back the publication decision and visible history together; a later retry can succeed.
- Activity identity includes the full durable evaluation identity, including session and epoch; reused turn sequence numbers in a later session do not hide a distinct evaluation. Recovery does not associate an unmarked evaluation with a legacy display key; those keys do not identify a unique evaluation. Migration 128 remains unchanged.
- Recovery publishes committed completed evidence when its session, generation, and epoch still match the current session and managed binding, even if the owner token has rotated since completion. Completion remains fenced to the live owner; an old owner cannot complete an unfinished claim or publish live activity. Superseded session, generation, epoch, managed binding, or goal revision evidence is not published or marked handled.
- Publication failures roll back; recovery is attempted at router startup. There is no automatic in-process retry scheduler, so a later startup or explicit retry is required.

- When legacy activity shares an ambiguous key across migrated evaluations, deduplication must not choose an arbitrary evaluation reason. It uses the activity’s own stored content instead; historical associations that cannot be determined are not reconstructed.
