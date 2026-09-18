ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_state_check;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_state_check
    CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'stopped', 'interrupted', 'awaiting_approval'));

CREATE TABLE approvals (
    id            BIGSERIAL PRIMARY KEY,
    agent_run_id  BIGINT NOT NULL REFERENCES agent_runs(id),
    tool_name     TEXT NOT NULL,
    tool_input    TEXT NOT NULL,
    reason        TEXT NOT NULL,
    state         TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'denied')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at    TIMESTAMPTZ
);
