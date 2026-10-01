FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# Server setup
RUN apt-get update && apt-get install -y \
    golang-go \
    git \
    curl \
    build-essential \
    sqlite3 \
    postgresql-client \
    vim \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

# Node (apt's ubuntu-shipped nodejs is too old for current pnpm/tooling)
RUN curl -fsSL https://deb.nodesource.com/setup_24.x | bash - \
 && apt-get install -y nodejs \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

# Go tools
RUN go install github.com/go-delve/delve/cmd/dlv@latest \
 && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

ENV PATH=$PATH:/go/bin

RUN npm install -g pnpm

# Claude
RUN npm install -g @anthropic-ai/claude-code
# Container's Claude config (CLAUDE.md, skills, hooks, settings seed) — synced
# into /root/.claude by entrypoint.sh at container start rather than COPY'd
# there directly, since that path is bind-mounted at runtime to persist
# auth/session state across container recreation.
COPY bit-brain /opt/bit-brain
# Flatten the mattpocock-skills submodule's promoted skills (nested as
# skills/<bucket>/<name>, listed in its plugin.json) into skills/<name>, the
# only layout ~/.claude/skills discovers, alongside any of our own skills. A
# name collision fails the build rather than silently overwriting one.
RUN set -e; src=/opt/bit-brain/mattpocock-skills; \
    if [ ! -f "$src/.claude-plugin/plugin.json" ]; then \
      echo "bit-brain/mattpocock-skills is empty — run: git submodule update --init" >&2; exit 1; \
    fi; \
    skills="$(node -p "require('$src/.claude-plugin/plugin.json').skills.join(' ')")"; \
    if [ -z "$skills" ]; then echo "mattpocock-skills plugin.json lists no skills" >&2; exit 1; fi; \
    mkdir -p /opt/bit-brain/skills; \
    for dir in $skills; do \
      name="$(basename "$dir")"; \
      if [ -e "/opt/bit-brain/skills/$name" ]; then \
        echo "skill name collision: $name (bit-brain/skills vs mattpocock-skills)" >&2; exit 1; \
      fi; \
      cp -a "$src/$dir" "/opt/bit-brain/skills/$name"; \
    done; \
    rm -rf "$src"

# Lightweight Go API providing container healthchecks
COPY healthcheck /root/healthcheck
RUN cd /root/healthcheck && go build -o /usr/local/bin/homelab-healthcheck .

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Setup workspace for dev work
# Existing repos will be cloned into /workspace
RUN mkdir /root/workspace

WORKDIR /root/workspace

# .vscode lives in ./volume (bind-mounted here at runtime), not baked into the image —
# anything COPY'd to /root/workspace would be shadowed by the mount anyway.

# Default vue port for frontend development
EXPOSE 5173
# Default api port
EXPOSE 3000
# Healthcheck API
EXPOSE 55123

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:55123/healthz || exit 1

ENTRYPOINT ["/entrypoint.sh"]
CMD ["bash"]
