#!/bin/bash
# SubagentStop hook: removes the running-state file written by
# subagent-start.sh. Never blocks or fails the hook — always exits 0, even on
# any internal error. Deliberately reads only agent_id — never persists
# last_assistant_message or any other hook payload field.
set -u

state_dir="/root/.claude/agents/running"

input="$(cat)"

agent_id="$(node -e '
  const data = JSON.parse(require("fs").readFileSync(0, "utf8"));
  process.stdout.write(data.agent_id || "");
' <<<"$input" 2>/dev/null)" || exit 0

if [[ -z "$agent_id" || ! "$agent_id" =~ ^[A-Za-z0-9_-]+$ ]]; then
  exit 0
fi

rm -f "$state_dir/$agent_id" 2>/dev/null

exit 0
