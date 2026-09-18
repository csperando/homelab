CREATE TABLE agent_sessions (
    id         BIGSERIAL PRIMARY KEY,
    workspace_id BIGINT NOT NULL REFERENCES workspaces(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tasks (
    id                BIGSERIAL PRIMARY KEY,
    agent_session_id  BIGINT NOT NULL REFERENCES agent_sessions(id),
    prompt            TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE agent_runs (
    id                BIGSERIAL PRIMARY KEY,
    task_id           BIGINT NOT NULL REFERENCES tasks(id),
    claude_session_id TEXT,
    state             TEXT NOT NULL CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'stopped', 'interrupted')),
    transcript        TEXT,
    error             TEXT,
    started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at       TIMESTAMPTZ
);
