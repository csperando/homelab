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
- **`vpn` profile** — `wireguard` (wg-easy v14), `wg-ddns`, and `cloudflared` carry
  `profiles: ["vpn"]`, so `docker compose up` in local dev never starts them; the server
  opts in with `COMPOSE_PROFILES=vpn` in its `.env`. `wireguard` is the internet-facing
  WireGuard endpoint (full tunnel, IPv4 only; `WG_POST_UP` drops forwarded `wg0` traffic
  to all RFC1918 so a client is a bare internet exit). `wg-ddns` (defined in the same
  `services/wireguard/compose.yml`) is a tiny alpine loop running
  `services/wireguard/cloudflare-ddns.sh` to keep `WG_HOST`'s A record — a DNS-only,
  unproxied record, separate from the UI hostname — pointed at the changing home public
  IP. `cloudflared` runs a Cloudflare Tunnel (dials out, no port) that serves the wg-easy
  admin UI behind Cloudflare Access. CI validates all three with
  `docker compose --profile vpn config`. Full runbook: `docs/vpn.md`.
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

- `.github/workflows/ci.yml` — on every PR to `main`: `go test` (against a `postgres:17-
  alpine` service container, so Postgres-backed tests run for real rather than skipping),
  `docker compose config` validation, and a no-push image build. GitHub-hosted;
  `contents: read` only.
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
  `HEALTHCHECK` polls — deliberately avoids shelling out, scanning repos, or touching
  Postgres), `/api/status` (full JSON status), `/` (HTML dashboard, template embedded via
  `go:embed`), `/workspaces` (the workspaces tab, formerly "repos" — see below),
  `/api/workspaces/clone` and `/api/workspaces/status` (workspaces tab backing endpoints,
  see below), `/agents` (the agents tab: start/observe/stop a headless `claude` run
  against a workspace — see `agentruns.go`/`agentprocess.go` below), `/api/agents/start`,
  `/api/agents/status` (per-run, `?run=<id>`, includes `pending_approval` when the run is
  `awaiting_approval`), `/api/agents/stop`, `/api/agents/hooks/pre-tool-use` (the
  `PreToolUse` callback target a headless run's own `claude` subprocess posts to —
  deliberately *not* behind `withAuth`, since the subprocess has no dashboard
  credentials; authenticated via `agentHookSecret` instead — see `approvals.go`/
  `approvalgate.go` below), and `/api/agents/approvals/decide` (operator-facing, behind
  `withAuth`, unlike the hook endpoint), `/api/workspaces/polling` (the Workspace tab's
  per-workspace GitHub issue polling opt-in checkbox, backed by
  `SetWorkspaceGitHubIssuePolling` — see the GitHub issue polling paragraph below), and
  `/files/` (a file server restricted to serving only paths that pass through a directory
  literally named `coverage` — see `isCoveragePath`/`coverageOnly` — not general
  workspace file access).
- `status.go` — gathers the status payload: tool versions, workspace disk usage, memory,
  load average, and a one-level-deep scan of `/root/workspace` for git repos (branch,
  dirty state).
- `coverage.go` — for each repo found, walks it looking for directories literally named
  `coverage` (supporting monorepos with several), and tries to parse a line-coverage
  percentage from either an Istanbul `coverage-summary.json` or an `lcov.info`, plus
  locating an HTML report (`lcov-report/index.html` or `index.html`) to link to via
  `/files/`.
- `github.go` — a stdlib-only GitHub API client (`GITHUB_TOKEN` env var, a classic PAT)
  that lists the token's repos for the workspaces tab, degrading to a disabled/reason
  state (mirroring `dockerStatus` in `docker.go`) rather than erroring when the token is
  unset or the GitHub API call fails. Also `listGitHubIssues` (filters out entries with a
  non-null `pull_request` field — GitHub's issues-list API returns both) and
  `postGitHubIssueComment`, the source and sink the GitHub issue poller uses; both reuse
  the same `githubHTTPClient`/token, no new scope needed.
- `githubremote.go` — `githubRemoteOwnerRepo(path)`, resolving the GitHub owner/repo a
  workspace directory points at by shelling out to `git -C <path> remote get-url origin`
  and parsing both the HTTPS and SSH remote URL forms. Deliberately not the
  `workspaces.repo_url` column: every workspace `backfillWorkspaces` creates has an empty
  `repo_url` (it has none to offer), which would make the poller silently inert against
  existing workspaces if it relied on the stored column instead.
- `clone.go` — an in-memory, mutex-guarded background job store for `git clone`
  operations triggered from the workspaces tab (`handleAPIWorkspacesClone`/
  `handleAPIWorkspacesStatus` in `main.go`, polled by the tab's own JS). Validates the
  destination via `isSafeRepoName` before touching the filesystem, supplies the GitHub
  token to git via a `GIT_ASKPASS` helper (never embedded in the clone URL or process
  argv), removes the destination directory if a clone fails, and on success persists a
  `workspaces` row (best-effort — a Postgres hiccup never fails an otherwise-successful
  clone).
- `db.go` — opens the process-wide Postgres connection pool
  (`POSTGRES_HOST`/`POSTGRES_PORT`/`POSTGRES_USER`/`POSTGRES_PASSWORD`/
  `POSTGRES_DATABASE`, via `pgx/v5/stdlib`, pinned to v5.7.4 — the newest release still
  compatible with Ubuntu 24.04's packaged Go 1.22, since pgx v5.7.5+ requires Go 1.23+).
  Connecting is best-effort at startup (retried, then a warning and DB-backed features
  disabled — never crashes the dashboard). The package-level `db` (`*sql.DB`, possibly
  nil) is passed explicitly to callers rather than referenced implicitly, unlike
  `githubHTTPClient`/`dockerHTTPClient`.
- `migrations.go` + `migrations/*.sql` — an embedded (`go:embed`) SQL migration runner,
  applied idempotently at startup (tracked in a `schema_migrations` table) before the HTTP
  server starts serving. A migration failure is fatal (`log.Fatalf`) — distinct from
  Postgres simply being unreachable, which degrades instead.
- `workspaces.go` — the `workspaces` table's repository layer (`UpsertWorkspace`,
  `ListWorkspaces`, `GetWorkspaceByPath`, `GetWorkspaceByID`, `SetWorkspaceGitHubIssuePolling`)
  plus `backfillWorkspaces` (fills in any repo already on disk under `workspaceDir` that
  predates this feature) and `mergeWorkspaceRows` (joins persisted workspaces with
  `scanWorkspaceRepos`' live branch/dirty state by path at request time — branch/dirty
  are intentionally never persisted, so they can't go stale). A workspace's
  `github_issue_polling_enabled` column (default `false`) is the per-workspace opt-in the
  GitHub issue poller reads every cycle.
- `agentruns.go` — the `agent_sessions`/`tasks`/`agent_runs` repository layer
  (`CreateAgentSession`, `GetAgentSession`, `CreateTask`, `GetTask`, `CreateAgentRun`,
  `SetAgentRunClaudeSessionID`, `UpdateAgentRunState`, `GetAgentRun`, `ListAgentRuns`),
  plus `ReconcileAgentRuns` (run once at startup, alongside `backfillWorkspaces`: marks
  any run still `pending`/`running` as `interrupted`, since the in-memory live-run store
  below is always empty on a fresh process start — a row in one of those states means a
  previous `healthcheck` process was killed/restarted mid-run). An `agent_runs.state` of
  `awaiting_approval` means a gated action was denied and a decision is pending — see
  `approvals.go`. An `agent_runs.transcript` is only ever written once, when the run
  finishes — never incrementally (see `agentprocess.go`). A task's nullable
  `github_issue_number` column is set only for tasks the GitHub issue poller creates;
  `CreateTask` takes it as an explicit `*int` parameter (`nil` for every manually-started
  task), and `githubIssueHasTask` is the poller's dedup check (does a task already exist
  for this workspace/issue number). The approve flow
  (`handleAPIAgentsApprovalsDecide` in `main.go`) copies the original task's
  `github_issue_number` into the continuation task it creates — otherwise a Job-created
  run that pauses for approval would lose track of which issue to comment on once it's
  later resumed and finishes.
- `githubissuepoller.go` — this repo's first periodic background loop:
  `startGitHubIssuePoller`, a `time.NewTicker`-driven goroutine
  (`GITHUB_ISSUE_POLL_INTERVAL`, default 300s; ticks are consumed one at a time by a
  single goroutine, so a slow poll cycle delays the next tick rather than ever running
  two cycles concurrently) started from `main()` only when both Postgres and
  `GITHUB_TOKEN` are available. Each cycle (`pollGitHubIssuesOnce`) walks every
  opted-in workspace, resolves its owner/repo via `githubRemoteOwnerRepo`, lists open
  issues (`listGitHubIssues`), and — for the first issue without an existing task — runs
  the exact same `CreateAgentSession` → `CreateTask` → `CreateAgentRun` → `startAgentRun`
  chain `handleAPIAgentsStart` uses for a manual run, so Approval gating applies
  identically (the real safeguard against issue-text prompt injection driving an
  unattended `git push`/`rm -rf`, not just pipeline reuse for its own sake). Deliberately
  starts at most **one** new run per workspace per cycle: `startAgentRun` registers a run
  in the live-run store asynchronously, inside its own goroutine, only after the `claude`
  subprocess's `cmd.Start()` returns, so re-checking in-flight state within the same loop
  iteration can't be trusted to see it yet — capping each cycle to one new run per
  workspace is what actually prevents two subprocesses starting concurrently against the
  same working directory, not the check itself. `workspaceRunInProgress` (in
  `agentprocess.go`) is that in-flight check, shared by this loop and
  `handleAPIAgentsStart`. A workspace-scoped failure (bad/missing remote, GitHub API
  error, ...) is logged and skipped, never aborting the rest of the cycle.
- `approvals.go` — the `approvals` table's repository layer (`CreateApproval`,
  `GetApproval`, `ListApprovalsForRun`, `UpdateApprovalState`). One row per gated tool
  call the `PreToolUse` hook denies; always created `pending`, resolved by the operator
  via `/api/agents/approvals/decide`, never by the run itself (see Approvals below).
- `approvalgate.go` — `isGatedCommand`, a pure substring match (not anchored to the
  command's start, so a gated action embedded in a compound command like
  `cd repo && git push` is still caught) against a fixed, hardcoded list (`git commit`,
  `git push`, `rm -rf`/`-fr`) — a practical gate for cooperative agent behavior, not an
  adversarial-proof sandbox, since `Bash` is general-purpose.
- `agentprocess.go` — the headless `claude` invocation and its in-memory live-run store
  (`liveRuns`, mirroring `clone.go`'s `cloneJobs` map but keyed by run ID, with a
  `ClaudeSessionID` field and `liveRunBySessionID`/`workspaceHasLiveRun` linear-scan
  helpers for the two places that need to search by something other than run ID — fine
  at this scale). `runAgent` spawns `claude --print --verbose --output-format
  stream-json --permission-prompts none --restricted --tools Read,Write,Edit,Bash
  --max-budget-usd <cap>` (never `--dangerously-skip-permissions` — that flag is scoped
  by Anthropic to network-isolated sandboxes, which this container is not) via
  `exec.Command` (`agentRunner` is a swappable package var, mirroring `cloneRunner`, so
  tests exercise the real JSONL-parsing logic against a cheap `sh -c` fake instead of the
  real, paid `claude` CLI) — `--verbose` is required alongside `--print
  --output-format=stream-json` or `claude` refuses to start; this was a real bug in
  already-shipped code with no automated test catching it, since every test uses the
  fake, never the real binary. Generates its own UUID and passes `--session-id` at spawn
  time (rather than reading whatever `claude` would auto-assign back out of the stream)
  so the approval hook — which fires mid-run, synchronously — can always resolve a
  callback's `session_id` straight back to a run; `startResumedAgentRun`/`runAgent`'s
  `resumeSessionID` param takes the opposite path for the approve action, passing
  `--resume <existing session>` instead, genuinely continuing the same conversation
  rather than starting fresh (live-verified: a fact stated in one `claude` invocation was
  correctly recalled by a second, independent invocation resuming that session).

  Streams stdout line-by-line into the live-run store, and persists the final outcome via
  `finishAgentRun` — which checks the run isn't already terminal before writing, guarding
  against a race with the stop action. `requestStop`/`escalateStopAfterGracePeriod`
  implement stop: `SIGTERM`, escalating to `SIGKILL` after `stopGracePeriod` if the
  process hasn't exited; the stop-requested flag is only set once the signal is confirmed
  delivered, so a run that happens to finish naturally at the same moment is never
  mis-reported as stopped. `findPendingApproval`/`hasPendingApproval` override a run's
  completion state to `awaiting_approval` (alongside the `stopped` override) when the
  `PreToolUse` hook recorded one — a real run whose gated command was denied still
  reports a normal successful transcript (the model gracefully explains it and ends the
  turn), so without this override it would be mis-reported as succeeded.

  Also owns the approval hook's plumbing: `agentHookSecret` (env var
  `AGENT_HOOK_SECRET`, read once via `os.Getenv` — **never generated or set by this
  program**, provisioning it is a manual step, see `.env.sample`) and
  `ensureAgentHookSettingsFile` (mirrors `clone.go`'s `ensureAskpassScript`: writes a
  static `settings.json` to `os.TempDir()` each run, since HTTP hooks can only be
  configured via a real settings file, not an inline `--settings` JSON string —
  confirmed against Anthropic's docs). `buildAgentArgs` only attaches `--settings <path>`
  when the secret is configured; if it's unset, the hook is never attached at all
  (Approval gating degrades off, same as Phase 4's fully-unattended behavior) rather than
  attaching a hook nothing could ever authenticate against, and `agentHookSecretWarning`
  logs once at startup, mirroring `githubExposureWarning`.

  `handleAPIAgentsPreToolUseHook` (`main.go`) is that settings file's `PreToolUse` HTTP
  hook target (`matcher: "Bash"`): validates the shared secret with
  `subtle.ConstantTimeCompare` (an unset configured secret is never treated as valid,
  even against an equally-empty header), and for a `Bash` call whose command matches
  `isGatedCommand` (`approvalgate.go`) creates a pending `Approval`
  (`approvals.go`) and responds with an explicit `permissionDecision: "deny"` —
  it never waits and never live-approves anything. This is deliberate, not a shortcut:
  live-testing found that a `PreToolUse` hook fails *open* on timeout (Anthropic's docs:
  a timed-out hook "doesn't block the tool call... don't count on a stalled hook to act
  as a gate"), so a bounded-wait-then-deny design would have been a silent bypass: real
  indefinite blocking on a tool-call decision is a Claude Agent SDK feature
  (`canUseTool`), not something the CLI supports, and adopting the SDK for this alone
  would reverse the "not the Agent SDK, stays inside `healthcheck/`" decision from
  Phase 4. `handleAPIAgentsApprovalsDecide` (`main.go`, behind `withAuth` unlike the hook
  endpoint) is where the operator actually decides: deny marks the `Approval` denied and
  the original run `failed`; approve marks it approved and starts a **new** Agent Run
  (`startResumedAgentRun`) that resumes the original `claude` session — the pending
  Approval's tool call is never retried in place.

  After `finishAgentRun` persists a run's outcome, `writeBackGitHubIssueComment` checks
  whether the run's task has a `github_issue_number` and, if the final state is
  genuinely terminal (never `awaiting_approval` — that isn't actually done), posts the
  run's result as a comment on the originating GitHub issue via `postGitHubIssueComment`.
  Result text comes from the last `type:"result"` line in the transcript
  (`extractResultText`, backed by a `Result` field on `claudeJSONLine`), falling back to
  the run's error message, then a generic note, if neither is available. Best-effort
  throughout — any failure here is only logged, never affects the run's own
  already-persisted outcome — and deliberately skipped for a run `ReconcileAgentRuns`
  marks `interrupted` after a restart (nothing calls it for that path; a disclosed trim,
  not an oversight).

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
