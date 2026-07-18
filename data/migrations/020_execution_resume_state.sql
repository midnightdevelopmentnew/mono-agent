-- 020_execution_resume_state.sql
-- Adds resume_state to workflow_executions so a Human-in-Loop pause can persist
-- enough in-flight state (routed inputs, completed nodes) to resume the run from
-- the pause point after an approval — even across an app/daemon restart —
-- instead of losing the in-memory execution.
ALTER TABLE workflow_executions ADD COLUMN resume_state TEXT NOT NULL DEFAULT '';
