# homelab

A local Docker dev environment: Go, Node/pnpm, and Claude Code preinstalled, with a
persistent workspace and a built-in health API.

## Usage

Via `make`:

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

Returns JSON with status, uptime, installed tool versions (Go, Node, npm, pnpm, git), and
disk usage for `/root/workspace`. Docker's own `HEALTHCHECK` polls this endpoint, so
`docker ps` / `docker compose ps` reflect container health directly.

## Ports

- `5173` — frontend dev server (e.g. Vite/Vue)
- `55123` — dashboard/health API
