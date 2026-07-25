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
COPY .claude /root/.claude

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
# Healthcheck API
EXPOSE 55123

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:55123/healthz || exit 1

ENTRYPOINT ["/entrypoint.sh"]
CMD ["bash"]
