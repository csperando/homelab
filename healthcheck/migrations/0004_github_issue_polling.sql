ALTER TABLE workspaces ADD COLUMN github_issue_polling_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tasks ADD COLUMN github_issue_number INTEGER;
