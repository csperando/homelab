# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Dockerized local dev environment ("homelab"): one Ubuntu container with Go, Node/pnpm,
and Claude Code preinstalled, a persistent workspace, a small Go health/dashboard API, and
Postgres available as a managed infra service. Not an application — there's no product
code here, just the environment definition itself (Dockerfile, compose files, the
healthcheck service, and Claude Code skills used to develop this repo).

## Commands

Via `make` (preferred):

```sh
make start    # clean build run — full reset
make build    # docker compose build
make run      # start/resume the container (seeds .env from .env.sample if missing)
make shell    # docker exec -it homelab bash
make stop     # docker compose stop
make restart  # stop + run
make logs     # follow container logs
make clean    # docker compose down
```

Equivalent raw `docker compose` commands work too; `docker compose up -d` also brings up
`include:`d infra services (Postgres) — no `-f` flags or separate steps needed.

Override the workspace host path with `WORKSPACE`, e.g. `WORKSPACE=~/code make run`.

**healthcheck service** (`healthcheck/`, Go module `homelab/healthcheck`, embedded in the
image at build time):

```sh
cd healthcheck && go build ./...   # verify it compiles
```

There are no automated tests in this repo currently.

## Architecture

### Container build (`Dockerfile`)

Single-stage Ubuntu 24.04 image. Order matters for layer caching: apt packages → Node (via
NodeSource, since Ubuntu's packaged Node is too old for current pnpm/tooling) → Go tools
(`dlv`, `golangci-lint`) → `pnpm` → Claude Code CLI → the healthcheck binary (built
in-image from `healthcheck/`) → `entrypoint.sh`.

Claude Code's tracked defaults (`.claude/settings.local.json`, `.claude/skills/`) are
`COPY`'d to `/opt/claude-defaults`, *not* directly to `/root/.claude` — that path is
bind-mounted at runtime (see below), so anything placed there at build time would be
shadowed by the mount.

### Runtime composition (`docker-compose.yml` + `services/*/compose.yml`)

- All containers share one Docker network, `homelab-net`. Infra services (e.g. Postgres)
  use `expose:`, never `ports:` — they're reachable only from inside the `homelab`
  container, by service-name DNS (e.g. `postgres:5432`), never published to the host.
- Infra services are wired in via the root compose file's `include:` list, each living in
  its own `services/<name>/compose.yml`. Adding one is: create that file following
  `services/postgres/compose.yml` as a template (attach `homelab-net`, use `expose:`,
  bind-mount persistent state under `./volume/infra/<name>`), then add one `include:` line
  to the root file. No other service definitions need to change.
- Persistent state lives under `./volume` (gitignored via `/volume/*` in `.gitignore`),
  bind-mounted into the container at three points:
  - `${WORKSPACE:-./volume}` → `/root/workspace` — cloned repos / dev work. Overridable via
    the `WORKSPACE` env var independent of where infra state lives.
  - `./volume/infra/<name>` → each infra service's data dir (e.g. Postgres).
  - `./volume/infra/claude` → `/root/.claude` — persists Claude Code's login/session state
    across container recreation (previously lost on every rebuild/recreate). `.env` /
    `.env.sample` hold Postgres credentials only, loaded via `env_file:`.

### entrypoint.sh

Runs before the container's `CMD`. Responsibilities, in order:
1. Seed `/root/.claude` from the baked-in `/opt/claude-defaults` using `cp -rn` (no
   clobber) — populates tracked settings/skills on first boot without ever overwriting
   runtime state (credentials, sessions) already present in the mounted volume.
2. Ensure `/root/.claude/claude.json` exists and is valid JSON (`{}` minimum — an empty
   file causes a JSON parse error at Claude Code startup), then symlink the root-level
   `/root/.claude.json` to it, since that file lives outside `/root/.claude` but needs the
   same persistence.
3. Start `homelab-healthcheck` in the background, then `exec` the container's `CMD`.

### Health/dashboard API (`healthcheck/`, port `55123`)

A small embedded Go HTTP service, split by concern:
- `main.go` — HTTP handlers and routing: `/healthz` (cheap liveness check Docker's
  `HEALTHCHECK` polls — deliberately avoids shelling out or scanning repos), `/api/status`
  (full JSON status), `/` (HTML dashboard, template embedded via `go:embed`), and
  `/files/` (a file server restricted to serving only paths that pass through a directory
  literally named `coverage` — see `isCoveragePath`/`coverageOnly` — not general workspace
  file access).
- `status.go` — gathers the status payload: tool versions, workspace disk usage, memory,
  load average, and a one-level-deep scan of `/root/workspace` for git repos (branch,
  dirty state).
- `coverage.go` — for each repo found, walks it looking for directories literally named
  `coverage` (supporting monorepos with several), and tries to parse a line-coverage
  percentage from either an Istanbul `coverage-summary.json` or an `lcov.info`, plus
  locating an HTML report (`lcov-report/index.html` or `index.html`) to link to via
  `/files/`.

### Claude Code skills (`.claude/skills/`)

This repo's own Claude Code workflow for developing *itself*, driven by two ephemeral
files under `.claude/tmp/` (gitignored):
- `set-goal` → writes `.claude/tmp/goal.md`: terse technical bullets (problem, scope,
  success criteria), never leaking specifics from gitignored/private paths (e.g. the
  workspace mount) into this tracked-repo file.
- `set-plan` → writes `.claude/tmp/plan.md`: a bare numbered list only (no headers, no
  per-item titles), one actionable, PR-sized task per line.
- `tdd` → executes `plan.md` one item at a time — write test first (API/jest/supertest
  level only, skipped for frontend work), implement, run the full test suite (up to 3 fix
  attempts), one git commit per completed item, then delete that item from `plan.md`
  immediately after committing.

### Ports

- `5173` — frontend dev server (e.g. Vite/Vue)
- `3000` — API dev server
- `55123` — health/dashboard API
