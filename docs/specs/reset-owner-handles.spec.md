# Reset owner-bound handles

Status: Draft. A reset or Claude restart targets the full agent, session, epoch and generation binding captured for the operation. If a successor has replaced the runtime or process, the old operation must not interrupt, close, revoke or clear that successor. Matching handles are interrupted and closed as before; concurrent process stops join the same close. After rotation, only the old binding's active correlation is cleared.

Limits: This protects in-memory handles selected by the local manager; it does not fence other managers or cancel a process start not yet inserted in the pool. Those mechanisms require separate coordination.

## Transaction failure

When reset fails and the store confirms the transaction rolled back, the caller receives the original transaction error and the old owner remains current. If the transaction committed but post-commit processing failed, or its outcome is ambiguous, the reset checks the same claim identity before publishing the replacement.

A committed reset with a post-commit state-file permission failure returns that failure rather than reporting success. The committed claim can still be checked separately; the error does not imply rollback. Claim resolution rejects different old and next agent IDs and reads the database opened at startup even if the process working directory changes.

## Reset seed and owner outcome

A reset commits its replacement owner and complete prepared context seed in the same local SQLite transaction. A seed failure rolls back the epoch and retains the old owner. A committed transaction with a post-commit error reports the error and updates the manager to the committed owner; an unresolved outcome invalidates the old cached owner rather than treating it as current. Owner fences still apply. A successful reset dispatches the existing direct wake turn; wake delivery is not an external durability guarantee.
