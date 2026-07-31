#!/bin/bash
set -e

# Seed Claude defaults (settings, skills) into the persistent, bind-mounted
# /root/.claude on first boot without clobbering runtime state (credentials,
# sessions, etc.) that may already live there.
cp -rn /opt/claude-defaults/. /root/.claude/ 2>/dev/null || true
[ -s /root/.claude/claude.json ] || echo '{}' > /root/.claude/claude.json
ln -sf /root/.claude/claude.json /root/.claude.json

# Unlike the seed step above, this state is ephemeral live-status (which
# Claude Code subagents are currently running, written by the
# SubagentStart/SubagentStop hooks) and must not survive a restart, so it's
# deliberately reset on every boot rather than preserved.
rm -rf /root/.claude/agents/running
mkdir -p /root/.claude/agents/running

/usr/local/bin/homelab-healthcheck &

exec "$@"
