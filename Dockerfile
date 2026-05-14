FROM ubuntu:latest

# Server setup
RUN apt-get update && apt-get install -y \
    golang-go \
    git \
    curl \
    build-essential \
    sqlite3 \
    nodejs \
    npm 

# Go tools
# Go will be used for a lightweight api that can provide healthchecks on the dev container
RUN go install github.com/go-delve/delve/cmd/dlv@latest \
 && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

ENV PATH=$PATH:/go/bin

RUN npm install -g pnpm

# Setup workspace for dev work
# Existing repos will be cloned into /workspace
RUN mkdir /root/workspace

WORKDIR /root/workspace

COPY .vscode .vscode

# Default vue port for frontend development
EXPOSE 5173
