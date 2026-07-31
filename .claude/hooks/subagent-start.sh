#!/bin/bash
# SubagentStart hook: records that a subagent started running, so the
# healthcheck dashboard's "subagents" section can show it. Never blocks or
# fails the hook — always exits 0, even on any internal error.
set -u

state_dir="/root/.claude/agents/running"

input="$(cat)"

fields="$(node -e '
  const data = JSON.parse(require("fs").readFileSync(0, "utf8"));
  process.stdout.write((data.agent_id || "") + "\t" + (data.agent_type || ""));
' <<<"$input" 2>/dev/null)" || exit 0

agent_id="${fields%%$'\t'*}"
agent_type="${fields#*$'\t'}"

# agent_id is hook-supplied; only trust it as a filename if it's a plain
# token (no "/", no ".."), never build a path from anything else.
if [[ -z "$agent_id" || ! "$agent_id" =~ ^[A-Za-z0-9_-]+$ ]]; then
  exit 0
fi

mkdir -p "$state_dir" 2>/dev/null || exit 0
{
  printf '%s\n' "$agent_type"
  date -u +%FT%TZ
} > "$state_dir/$agent_id" 2>/dev/null

exit 0
