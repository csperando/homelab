# homelab

A local Docker dev environment: Go, Node/pnpm, and Claude Code preinstalled, with a
persistent workspace and a built-in health API.

## Usage

Via `make`:

### Primary 

```sh
make start   # clean build run
```

### Additional Commands

```sh
make build   # build the image
make run     # start (or resume) the container
make shell   # open a shell in the running container
make stop    # stop the container
make restart # stop + run
make logs    # follow container logs
make clean   # remove the container
```

Via Docker Compose:

```sh
cp .env.sample .env   # first time only — make run does this automatically
docker compose build
docker compose up -d
docker compose exec homelab bash
docker compose down
```

`docker compose up -d` also starts the infra services (Postgres) via `include:` — no
`-f` flags needed, and no separate step to bring them up.

Both paths mount `./volume` (in this repo) to `/root/workspace` in the container, so
cloned repos and work survive container restarts/rebuilds. Its contents are gitignored.
Override the host path with a `WORKSPACE` variable, e.g. `WORKSPACE=~/code make run` or
`WORKSPACE=~/code docker compose up -d`.

Claude Code's login/session state is separately persisted via `./volume/infra/claude`,
bind-mounted to `/root/.claude` — so `claude login` only needs to happen once per machine,
not once per container recreation.

## Infrastructure Services

Homelab owns a shared Docker network, `homelab-net`, connecting the Homelab container to
infra service containers it manages. Services are never published to the host — reach
them from inside the Homelab container only, by their service name (DNS hostname).

**Postgres** — `postgres:5432`

```sh
PGPASSWORD=$POSTGRES_PASSWORD psql -h postgres -U $POSTGRES_USER -d $POSTGRES_DB
```

Default dev credentials (`homelab`/`homelab`/`homelab`) live in `.env.sample`; override
by setting `POSTGRES_USER`/`POSTGRES_PASSWORD`/`POSTGRES_DB` in your `.env`. This is a
single shared instance — each project should create/use its own database or schema
rather than assuming exclusive use of `postgres`. Data lives in `./volume/infra/postgres`
and persists independently of the Homelab container (survives `make stop`/`make clean`).

**Adding a service**

1. Create `services/<name>/compose.yml`, following `services/postgres/compose.yml` as a
   template: attach to `networks: [homelab-net]`, use `expose:` (never `ports:`), and
   bind-mount any persistent data under `./volume/infra/<name>`.
2. Add one line to the root `docker-compose.yml`'s `include:` list.

No changes to the network or to other services' definitions are needed.

## Health API

The container runs a small Go service on port `55123`:

```sh
curl localhost:55123/healthz
```

`/healthz` is a cheap liveness check — Docker's own `HEALTHCHECK` polls it, so `docker ps`
/ `docker compose ps` reflect container health directly.

`/` serves an HTML dashboard, and `/api/status` returns the same data as JSON: status,
uptime, installed tool versions (Go, Node, npm, pnpm, git, Claude Code), memory and load
average, disk usage for `/root/workspace`, and, for each git repo found one level deep
under the workspace, its branch/dirty state plus any test-coverage percentage found
(parsed from Istanbul `coverage-summary.json` or `lcov.info`). Coverage HTML reports are
served read-only under `/files/`.

## Ports

- `5173` — frontend dev server (e.g. Vite/Vue)
- `3000` — API dev server
- `55123` — dashboard/health API
