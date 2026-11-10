-- Publication decisions live as long as their bounded evaluation evidence, not
-- as long as the independently capped display ledger.
CREATE TABLE embedded_goal_activity_publications (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 user_seq INTEGER NOT NULL, user_digest TEXT NOT NULL, goal_revision INTEGER NOT NULL,
 event_key TEXT NOT NULL, reason TEXT NOT NULL,
 PRIMARY KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision),
 FOREIGN KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision)
 REFERENCES embedded_goal_evaluations(agent_id,session_id,epoch,user_seq,user_digest,goal_revision) ON DELETE CASCADE
);
INSERT OR IGNORE INTO embedded_goal_activity_publications
-- Older display rows may already have been evicted. Treat all completed legacy
-- evidence as handled rather than resurrecting activity on upgrade. Legacy data
-- cannot distinguish eviction from a crash before publication; the latter activity
-- is also suppressed and cannot be recovered across this upgrade.
SELECT e.agent_id,e.session_id,e.epoch,e.user_seq,e.user_digest,e.goal_revision,
 'goal-evaluation:'||e.agent_id||':'||e.user_seq||':'||e.final_seq||':'||CASE WHEN e.state='DONE' THEN 'COMPLETED' ELSE e.state END,e.reason
FROM embedded_goal_evaluations e
WHERE e.state IN ('DONE','NOT_DONE','COMPLETED') AND e.completed_at IS NOT NULL;
