FROM ubuntu:latest

RUN apt-get update && apt-get install -y \
    golang-go \
    git \
    curl \
    build-essential \
    sqlite3

# Go tools
RUN go install github.com/go-delve/delve/cmd/dlv@latest \
 && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

ENV PATH=$PATH:/go/bin

WORKDIR /workspace
