# Homelab deployment architecture — decision analysis

**Date:** 2026-08-29 · **Scope:** the CI/CD + hosting model decided and built this session
(PRs #1 `ci-cd-pipeline`, #2 `deploy-hardening`, both merged to `main`).

> This file is in `.claude/tmp/` (gitignored). If it should be a durable team reference,
> promote it to `docs/architecture/deployment.md`.

---

## 1. The system, as a mental model

```
laptop (Apple Silicon, arm64)          GitHub                          coleman-box (x86_64, Ubuntu, always-on)
─────────────────────────────          ──────                          ──────────────────────────────────────
edit code, `make build` locally  ──►   PR ──► ci.yml (hosted)          also runs: Pritunl VPN (vpn.colemansperando.com)
never pulls the image                  merge ─► deploy.yml:            behind: Netgear RAX10 (consumer NAT, 192.168.1.1)
                                         build-push (hosted) ─► GHCR
                                         deploy (self-hosted) ──────►  runner (systemd svc) → ~/homelab → docker compose
                                                                       stack: homelab + postgres/redis/nginx/ollama
```

- **Registry:** `ghcr.io/csperando/homelab`, **private**. Tags: `:latest` (moving) and
  `:sha-<short>` (immutable; every deploy pins to one).
- **Deploy dir:** `~/homelab` (= `/home/coleman/homelab`) on the box — populated entirely
  by the deploy job's `rsync` of tracked files (`docker-compose*.yml`, `Makefile`,
  `services/`, `scripts/`) plus `.env` from a secret. No git checkout on the box.
- **Runner:** self-hosted, one, name `coleman-box`, labels `self-hosted,linux,X64`,
  installed as a systemd service (`actions.runner.csperando-homelab.coleman-box.service`),
  runner binaries in `~/actions-runner`.
- **The box is the "production" target.** The laptop stays a dev/edit machine.

---

## 2. Decisions, rationale, and when to revisit

### D1 — Build once in CI, pull on the server
**Chose:** image built by `build-push` on a GitHub-hosted runner, pushed to GHCR; server
only ever `docker compose pull`s.
**Why:** one reproducible artifact, not a per-machine build; separates "run the
environment" from "change the environment"; the box needs no build toolchain or build time.
**Rejected:** server builds from a git clone (needs private-repo creds on the box, build
time on the box, drift between machines).
**Revisit if:** a second image consumer appears, or you want provenance/SBOM attestations.

### D2 — Multi-arch image (amd64 + arm64) — **PROVISIONAL, still open**
**Chose (at the time):** `platforms: linux/amd64,linux/arm64` so the laptop could
`docker compose pull`.
**Reality found:** the laptop uses `make build` locally and never pulls — so the arm64
image in the registry is **currently unused**.
**Cost:** the arm64 leg builds under QEMU emulation and the Dockerfile does in-image
`go install` of `dlv` + `golangci-lint` → ~30–40 min cold builds (few min warm via
`type=gha` cache).
**Recommendation on the table:** drop to `linux/amd64` only, remove
`docker/setup-qemu-action` → ~3–5 min builds.
**Revisit / keep multi-arch if:** you will actually pull-and-run the image on an ARM
machine (Raspberry Pi, Ampere, an M-series box).

### D3 — Push-based deploy via a persistent self-hosted runner
**Chose:** GitHub Actions self-hosted runner on the box, systemd service, always on;
`deploy.yml`'s `deploy` job runs there.
**Why:**
- The runner makes an **outbound long-poll** to GitHub → **no inbound port, no router
  forwarding**. Decisive given consumer NAT + an already-sensitive Pritunl endpoint.
- Reuses the deploy logic we built (pull, `up -d`, health poll, step summary, rollback
  hint) with Actions UI for logs and secrets.
- Idle cost is negligible on an always-on box.
**Rejected:**
- *Watchtower / cron pull* — deploy latency, no build step, no unified per-deploy log.
- *Webhook listener on the box* — needs an inbound endpoint; reimplements deploy logic.
- *Ephemeral / JIT runners* — needs an orchestrator (ARC/k8s, cloud autoscaler); overkill
  for one box.
**Security:** the `deploy` job's `if:` gates it to `push` / `workflow_dispatch` on
`csperando/homelab`'s `main` — **never PRs**. A self-hosted runner executes repo code as
the runner user; a PR-triggered job would be arbitrary code execution on the box.
**Revisit if:** more repos need runners (→ org-level runner or ARC), or the trust boundary
changes (external contributors).

### D4 — Deploy dir is `~/homelab`, not `/opt/homelab`
**Chose:** the runner user's home dir.
**Why:** the deploy job runs as an **unprivileged user with no `sudo`**. `/opt` is
root-owned → `mkdir` fails (this actually broke the first real deploy). `$HOME` is
runner-owned.
**Consequence:** the entire deploy path is sudo-free. The *only* `sudo` on the box is the
one-time bootstrap.
**Loose end:** the earlier `/opt/homelab` on the server is orphaned and contains a
mode-600 `.env` with real secrets → `sudo rm -rf /opt/homelab`.

### D5 — No host `make` dependency in CI
**Chose:** `deploy.yml` runs `docker compose pull` + `up -d --remove-orphans` inline
instead of `make -C ~/homelab deploy`.
**Why:** fewer host prerequisites, clearer failure surface. `make deploy` stays in the
`Makefile` for humans doing manual rollback on the box.

### D6 — Preflight step, fail-fast principle
**Chose:** the `deploy` job's first step checks docker access, `docker compose`, and dir
writability, and exits with `::error::run scripts/bootstrap-server.sh` on failure.
**Principle established:** the deploy job assumes nothing about host state and must
diagnose a broken box in one line, not fail cryptically three steps in (which is exactly
what happened before this step existed).

### D7 — PR flow is a convention, **not enforced**
**Chose:** `main` has **no branch protection**. `ci.yml` runs on PRs but is not a required
check. Direct pushes to `main` are allowed.
**Why:** user's explicit call — "if I want to work on main, I'll work on main." The
original goal ("stop working on main for too long") is served by *having* a PR path, not
by locking it.
**Revisit if:** a second contributor appears, or you want the `test` gate to actually
block merges (then: Settings → Branches → require PR + require `test`).

### D8 — Reboot / crash resilience
**Chose:** `restart: unless-stopped` on all infra services **in the prod override only**
(local `docker-compose.yml` unchanged); `docker.service` enabled on boot (bootstrap does
this); runner is a systemd service.
**Result:** full host reboot → the stack **and** the runner come back with zero human
action. `unless-stopped` (not `always`) still lets you deliberately `docker compose stop`
a service and have it stay stopped.

### D9 — Secrets / trust model (consolidated)
| Credential | Type | Where | Used for | Notes |
|---|---|---|---|---|
| `ENV` | repo secret | GitHub → `~/homelab/.env` (mode 600) every deploy | Postgres creds + `DOCKER_SOCK_PATH` | single source of truth for server config |
| `GITHUB_TOKEN` | automatic | deploy job | `docker pull` from GHCR (`packages: read`) | **no PAT on the box for the pipeline** |
| `GHCR_CLEANUP_TOKEN` | **classic** PAT | repo secret | `registry-cleanup.yml` only | needs `write:packages` **+** `delete:packages`; **fine-grained PATs have no packages write/delete permission** (confirmed in the UI) |
| runner registration token | short-lived | GitHub UI, one-time | `config.sh` | expires ~1 h; no long-lived admin cred lands on the box |
| (optional) `read:packages` classic PAT | — | `docker login ghcr.io` on the box | manual rollback pulls only | not needed for automation |

The box holds: the runner's own GitHub connection creds (managed by the runner) and
`~/homelab/.env`. Nothing else.

### D10 — Registry retention
**Chose:** `registry-cleanup.yml`, weekly, keep `:latest` + the 15 newest `sha-*`
(`image-tags: "!latest"`, `keep-n-most-recent: 15`, `cut-off: 1w`). Verified via dry-run.
**Why:** preventive — private-package storage bills above 2 GB; this image is large and
`sha-*` tags accumulate forever otherwise. Not urgent (months of runway).

### D11 — Skill staleness fix (`entrypoint.sh`)
**Root cause of "skills don't work as well in the container":** the `cp -rn` seed never
overwrites, so an existing `volume/infra/claude` kept its `.claude/skills/` **frozen at
whatever the first image ever shipped** — pulling new images never updated them.
**Fix:** force-refresh `/root/.claude/skills/` (rm + cp) on every boot; credentials /
sessions / settings still governed by the no-clobber seed.
**New invariant:** skills are **image-managed**. Changing a skill requires a rebuilt image
(and a redeploy) to take effect on the server.

### D12 — Published image carries no machine-specific state
`.dockerignore` excludes `.git`, `volume/`, `.claude/tmp/`. `settings.local.json` is
untracked → absent from the CI checkout by construction, so CI images ship only
`.claude/skills/`. (Local `make build` still bakes `settings.local.json` — existing,
intentional.)

---

## 3. Fresh-server onboarding — the automation contract

**Irreducibly manual** (true for *any* self-hosted runner):
1. Get a runner registration token from the GitHub UI (Settings → Actions → Runners → New).
2. One `sudo` bootstrap run on the box.
3. Log out / back in so the `docker` group applies; `sudo systemctl restart 'actions.runner.*'`.

**Automated by `scripts/bootstrap-server.sh`** (the single versioned artifact):
`apt install rsync make curl` · `usermod -aG docker` · `systemctl enable --now docker` ·
`mkdir ~/homelab` · download + `config.sh --unattended --replace` + `svc.sh install/start`
the runner.

The script reaches a fresh box via `scp scripts/bootstrap-server.sh box:~` (private repo →
no clone). After the first deploy it also lives at `~/homelab/scripts/` for re-runs.

---

## 4. Failure modes and recovery

| Failure | Behaviour | Recovery |
|---|---|---|
| Host reboot | stack + runner auto-start (`restart: unless-stopped`, enabled services) | none needed |
| Single container crash | Docker restarts it | none needed |
| Runner offline | `deploy` job queues (waits for a matching runner, ~24 h then fails) | start the service; the queued job picks up |
| `build-push` OK, `deploy` fails | image is in GHCR, server not updated | "Re-run failed jobs", or `workflow_dispatch` with `tag=sha-<short>`, or `make -C ~/homelab deploy HOMELAB_TAG=…` |
| Bad image deployed | old container ran until `pull`; health poll fails the job | `workflow_dispatch` with an older `tag`; job summary + `::error::` carry the previous tag and command |
| Host prep missing / wrong | `preflight` step fails with a pointed message | (re-)run `scripts/bootstrap-server.sh`, restart the runner |

Rollback is **deliberately manual** (visible failures > silent auto-revert). `PRIOR_IMAGE`
is recorded each run to make it one command.

---

## 5. Open items and known debt

- **[OPEN] arm64** — keep multi-arch or drop to amd64-only (see D2). Every merge pays
  ~30 min until decided.
- **[CLEANUP] `/opt/homelab`** on the server — orphaned, holds a secrets file. `sudo rm -rf`.
- **[DEFERRED] branch protection** — off by choice (D7).
- **[MONITOR] Actions minutes** — GitHub Pro = 3000 Linux-min/month; est. ~600 used;
  the self-hosted `deploy` job is free. Watch Settings → Billing if activity grows or if
  multi-arch stays.
- **[NOT STARTED] `home.lab` LAN DNS** — separate earlier thread; would be a `dnsmasq`
  infra service (`services/dnsmasq/compose.yml`, published port 53) + pointing the
  Netgear's DHCP DNS at the box. Independent of everything above.
- **[TRAP LOG] don't repeat:**
  - Any path the CI writes must be runner-owned — no `/opt`, no `sudo` in the deploy path.
  - Sanitize before `>> "$GITHUB_ENV"` — `docker inspect` on a missing object emits a
    stray newline → multi-line write → `Invalid format`. Use `head -n1` + a plain sentinel.
  - GitHub's "New self-hosted runner" UI gives you `./run.sh` (foreground, dies on
    logout). `svc.sh install` is the real step; `bootstrap-server.sh` does it.
  - Fine-grained PATs can't write/delete GHCR packages — classic PAT for anything
    registry-write.
  - `snok/container-retention-policy` + `GITHUB_TOKEN` can't use tag filters (temporal
    token limitation) — hence the classic PAT.
  - `!reset` in a compose override needs Compose ≥ v2.24 (box has v5.3.1).

---

## 6. Extension points — how this architecture wants to grow

- **New infra service:** `services/<name>/compose.yml` + one `include:` line; add
  `restart: unless-stopped` in `docker-compose.prod.yml` if it must survive reboot. The
  deploy job `rsync`s `services/` wholesale — nothing else changes.
- **Second deploy target (staging / another box):** parameterise `DEPLOY_DIR`, runner
  labels, and the `ENV` secret per environment; turn `deploy.yml` into a matrix or a
  reusable workflow. `HOMELAB_TAG` pinning already supports "promote the tag that passed
  staging" flows.
- **Auto-rollback:** capture `PRIOR_IMAGE`, redeploy it if the health poll fails.
  Deliberately not built (keep failures loud) — but the hook is there.
- **Faster / cleaner multi-arch:** split the Dockerfile so Go tools cross-compile instead
  of emulate, or build the arm64 leg on a native ARM runner.
- **Image provenance:** add `provenance: true` / SBOM to `build-push` if supply-chain
  attestation becomes relevant.
- **Skills as a faster-moving artifact:** if rebuilding the whole image to ship a skill
  tweak is too slow, skills could move to a separate lightweight image or a mounted volume
  synced independently — but that reopens the staleness problem D11 solved.
