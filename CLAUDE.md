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
make deploy   # SERVER ONLY: pull the published image and (re)start — never build locally
```

Equivalent raw `docker compose` commands work too; `docker compose up -d` also brings up
`include:`d infra services (Postgres) — no `-f` flags or separate steps needed.

Override the workspace host path with `WORKSPACE`, e.g. `WORKSPACE=~/code make run`.

**healthcheck service** (`healthcheck/`, Go module `homelab/healthcheck`, embedded in the
image at build time):

```sh
cd healthcheck && go build ./...   # verify it compiles
cd healthcheck && go test ./...    # unit tests (also run by CI on every PR)
```

## Architecture

### Container build (`Dockerfile`)

Single-stage Ubuntu 24.04 image. Order matters for layer caching: apt packages → Node (via
NodeSource, since Ubuntu's packaged Node is too old for current pnpm/tooling) → Go tools
(`dlv`, `golangci-lint`) → `pnpm` → Claude Code CLI → the healthcheck binary (built
in-image from `healthcheck/`) → `entrypoint.sh`.

Claude Code's defaults (`.claude/skills/`, and `.claude/settings.local.json` if present —
that file is untracked, blanket-ignored by convention since it can hold machine-specific
config like absolute paths, but still gets baked in from whatever's on disk at build time
on the machine running `docker build`) are `COPY`'d to `/opt/claude-defaults`, *not*
directly to `/root/.claude` — that path is bind-mounted at runtime (see below), so
anything placed there at build time would be shadowed by the mount.

### Runtime composition (`docker-compose.yml` + `services/*/compose.yml`)

- All containers share one Docker network, `homelab-net`. Infra services (Postgres, nginx,
  redis, ollama, ...) use `expose:`, never `ports:` — they're reachable only from inside
  the `homelab` container, by service-name DNS (e.g. `postgres:5432`), never published to
  the host. **Exception:** `wireguard` (see below) publishes one UDP port and takes
  `cap_add`/`sysctls`/`devices` — a WireGuard endpoint has to be reachable from the
  internet.
- Infra services are wired in via the root compose file's `include:` list, each living in
  its own `services/<name>/compose.yml`. Adding one is: create that file following
  `services/postgres/compose.yml` as a template (attach `homelab-net`, use `expose:`,
  bind-mount persistent state under `./volume/infra/<name>`), then add one `include:` line
  to the root file. No other service definitions need to change.
- **`vpn` profile** — `wireguard` (wg-easy v14) and `cloudflared` carry
  `profiles: ["vpn"]`, so `docker compose up` in local dev never starts them; the server
  opts in with `COMPOSE_PROFILES=vpn` in its `.env`. `wireguard` is the internet-facing
  WireGuard endpoint (full tunnel; `WG_POST_UP` drops forwarded `wg0` traffic to all
  RFC1918 so a client is a bare internet exit). `cloudflared` runs a Cloudflare Tunnel
  (dials out, no port) that serves the wg-easy admin UI behind Cloudflare Access. CI
  validates both with `docker compose --profile vpn config`. Full runbook: `docs/vpn.md`.
- Persistent state lives under `./volume` (gitignored via `/volume/*` in `.gitignore`),
  bind-mounted into the container at three points:
  - `${WORKSPACE:-./volume}` → `/root/workspace` — cloned repos / dev work. Overridable via
    the `WORKSPACE` env var independent of where infra state lives.
  - `./volume/infra/<name>` → each infra service's data dir (e.g. Postgres).
  - `./volume/infra/claude` → `/root/.claude` — persists Claude Code's login/session state
    across container recreation (previously lost on every rebuild/recreate). `.env` /
    `.env.sample` hold Postgres credentials only, loaded via `env_file:`.
- `docker-compose.prod.yml` is a server-only override: it swaps the `homelab` service's
  `build:` for `image: ghcr.io/csperando/homelab:${HOMELAB_TAG:-latest}` and adds
  `restart: unless-stopped` to the infra services. Applied via `make deploy` (which passes
  both `-f` files). Local dev never uses it.

### CI/CD and server deployment

- `.github/workflows/ci.yml` — on every PR to `main`: `go test`, `docker compose config`
  validation, and a no-push image build. GitHub-hosted; `contents: read` only.
- `.github/workflows/deploy.yml` — on push to `main` (or `workflow_dispatch`): a
  GitHub-hosted `build-push` job builds the multi-arch image and pushes `:latest` +
  `:sha-<short>` to GHCR, then a `deploy` job **on a self-hosted runner on the dev server**
  preflight-checks the host, syncs the compose files into `~/homelab` (the runner user's
  home — no `sudo`), writes `.env` from the `ENV` repo secret, runs `docker compose pull` +
  `up -d` pinned to the new `sha-<short>`, and health-checks it. The self-hosted job is
  gated to `push`/`dispatch` on this repo — never PRs (RCE risk). Host bootstrap (the one
  `sudo` step + runner install) is `scripts/bootstrap-server.sh`.
- `.github/workflows/registry-cleanup.yml` — weekly; trims old `sha-*` tags (needs a
  classic `GHCR_CLEANUP_TOKEN` PAT with `write:packages`+`delete:packages`, inert without it).
- All action refs are pinned to commit SHAs. Full setup + rollback runbook:
  `docs/deploy.md`; server bootstrap: `scripts/bootstrap-server.sh`.

### entrypoint.sh

Runs before the container's `CMD`. Responsibilities, in order:
1. Seed `/root/.claude` from the baked-in `/opt/claude-defaults` using `cp -rn` (no
   clobber) — populates default settings/skills on first boot without ever overwriting
   runtime state (credentials, sessions) already present in the mounted volume.
2. Force-refresh only `/root/.claude/skills/` from the image (`rm -rf` + `cp -r`). Skills
   are image-managed, not runtime state; the `cp -rn` above would otherwise leave a stale
   copy from an earlier image in the mounted volume, so a freshly pulled image never
   updated them. Credentials / sessions / settings are untouched.
3. Ensure `/root/.claude/claude.json` exists and is valid JSON (`{}` minimum — an empty
   file causes a JSON parse error at Claude Code startup), then symlink the root-level
   `/root/.claude.json` to it, since that file lives outside `/root/.claude` but needs the
   same persistence.
4. Start `homelab-healthcheck` in the background, then `exec` the container's `CMD`.

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

This repo's own Claude Code workflow for developing *itself*, driven by ephemeral files
under `.claude/tmp/` (gitignored):
- `set-goal` → writes `.claude/tmp/goal.md`: terse technical bullets (problem, scope,
  success criteria), never leaking specifics from gitignored/private paths (e.g. the
  workspace mount) into this tracked-repo file.
- `set-plan` → writes `.claude/tmp/plan.md`: a bare numbered list only (no headers, no
  per-item titles), one actionable, PR-sized task per line.
- `tdd` → executes `plan.md` one item at a time — write test first (API/jest/supertest
  level only, skipped for frontend work), implement, run the full test suite (up to 3 fix
  attempts), one git commit per completed item, then delete that item from `plan.md`
  immediately after committing. Records the starting commit SHA in
  `.claude/tmp/tdd-start-sha` on first run (deleted once `plan.md` empties out) to mark the
  range of commits made during this plan.
- `fix-plan` → standalone recovery skill for when tdd execution hits an unexpected issue.
  Given a description of the problem plus goal.md/plan.md and the git history bounded by
  `tdd-start-sha`, diagnoses whether the remaining plan can route around it or whether
  already-committed work needs rolling back, then rewrites `plan.md` accordingly. Never
  runs git itself — only recommends commands for the user to run.

### Ports

- `5173` — frontend dev server (e.g. Vite/Vue)
- `3000` — API dev server
- `55123` — health/dashboard API
- `51820/udp` — WireGuard endpoint (`wireguard`, `vpn` profile; `WG_UDP_PORT`, the one
  host-published infra port). wg-easy's UI port stays `expose:`-only, reached via the
  `cloudflared` tunnel.
