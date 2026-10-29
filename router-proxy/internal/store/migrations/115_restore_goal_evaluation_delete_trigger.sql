CREATE TRIGGER IF NOT EXISTS embedded_goal_evaluation_immutable_delete BEFORE DELETE ON embedded_goal_evaluations
WHEN EXISTS (SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'embedded goal evaluation is immutable'); END;
