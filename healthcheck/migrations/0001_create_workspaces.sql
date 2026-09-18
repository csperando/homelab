CREATE TABLE workspaces (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    path       TEXT NOT NULL UNIQUE,
    repo_url   TEXT NOT NULL DEFAULT '',
    env_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
