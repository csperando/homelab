FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# Server setup
RUN apt-get update && apt-get install -y \
    golang-go \
    git \
    curl \
    build-essential \
    sqlite3 \
    nodejs \
    npm \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

# Go tools
RUN go install github.com/go-delve/delve/cmd/dlv@latest \
 && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

ENV PATH=$PATH:/go/bin

RUN npm install -g pnpm

# Claude
RUN npm install -g @anthropic-ai/claude-code

# Lightweight Go API providing container healthchecks
COPY healthcheck /root/healthcheck
RUN cd /root/healthcheck && go build -o /usr/local/bin/homelab-healthcheck .

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Setup workspace for dev work
# Existing repos will be cloned into /workspace
RUN mkdir /root/workspace

WORKDIR /root/workspace

COPY .vscode .vscode

# Default vue port for frontend development
EXPOSE 5173
# Healthcheck API
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/healthz || exit 1

ENTRYPOINT ["/entrypoint.sh"]
CMD ["bash"]
