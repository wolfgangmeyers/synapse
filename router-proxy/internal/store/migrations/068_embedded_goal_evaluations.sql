-- Provisional G5/T6: exact settled-turn evaluator intent, never lease-reclaimed.
CREATE TABLE embedded_goal_evaluations (
 agent_id TEXT NOT NULL, session_id TEXT NOT NULL, epoch INTEGER NOT NULL,
 generation TEXT NOT NULL, user_seq INTEGER NOT NULL CHECK(user_seq>0), user_digest TEXT NOT NULL,
 goal_revision INTEGER NOT NULL CHECK(goal_revision>0),
 owner_token INTEGER NOT NULL CHECK(owner_token>0),
 final_seq INTEGER NOT NULL CHECK(final_seq>0), final_digest TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('UNKNOWN','DONE','NOT_DONE')),
 reason TEXT NOT NULL DEFAULT '', claimed_at TEXT NOT NULL, completed_at TEXT,
 PRIMARY KEY(agent_id,session_id,epoch,user_seq,user_digest,goal_revision),
 FOREIGN KEY(agent_id,session_id,epoch,generation) REFERENCES embedded_sessions(agent_id,session_id,epoch,generation) ON DELETE CASCADE,
 FOREIGN KEY(agent_id,session_id,epoch,generation,user_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq),
 FOREIGN KEY(agent_id,session_id,epoch,generation,final_seq) REFERENCES embedded_entries(agent_id,session_id,epoch,generation,seq),
 CHECK((state='UNKNOWN' AND completed_at IS NULL AND reason='') OR (state<>'UNKNOWN' AND completed_at IS NOT NULL))
);
CREATE TRIGGER embedded_goal_evaluation_immutable_delete BEFORE DELETE ON embedded_goal_evaluations
BEGIN SELECT RAISE(ABORT,'embedded goal evaluation is immutable'); END;
CREATE TRIGGER embedded_goal_evaluation_immutable_result BEFORE UPDATE ON embedded_goal_evaluations
WHEN OLD.state<>'UNKNOWN' OR NEW.state='UNKNOWN' OR NEW.agent_id<>OLD.agent_id OR NEW.session_id<>OLD.session_id
 OR NEW.epoch<>OLD.epoch OR NEW.generation<>OLD.generation OR NEW.user_seq<>OLD.user_seq
 OR NEW.user_digest<>OLD.user_digest OR NEW.goal_revision<>OLD.goal_revision OR NEW.owner_token<>OLD.owner_token
 OR NEW.final_seq<>OLD.final_seq OR NEW.final_digest<>OLD.final_digest OR NEW.claimed_at<>OLD.claimed_at
BEGIN SELECT RAISE(ABORT,'embedded goal evaluation is immutable'); END;
