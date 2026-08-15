# GitLens

A Go-based web application for monitoring and visualizing GitHub repository activity across multiple repositories at a glance. Tracks commits, releases, workflow status, and DORA metrics.

## Features

- **Multi-repo dashboard** — View commits, releases, CI status, and pull requests for all your GitHub repos in one place
- **Pull request management** — View open PRs, merge individually or merge all with one click
- **Release status tracking** — See whether the latest release's CI passed or failed
- **Automated syncing** — Per-user configurable sync intervals (1 min to 24 hours)
- **DORA metrics** — Commit type breakdown (feat/fix/docs/chore), workflow pass rate, lead time, release frequency
- **GitHub webhook** — Real-time updates on push events
- **WebSocket live updates** — Repo cards refresh automatically when data changes
- **GitHub OAuth** — Secure authentication via GitHub
- **Forgejo support** — Connect your Forgejo/Gitea instance as an additional provider alongside GitHub

## Architecture

```
Browser (HTMX + WebSocket)
    |
Gin HTTP Server (:6270)
    |
    +-- Auth routes      --> GitHub + Forgejo OAuth
    +-- Dashboard routes --> HTML + HTMX partials
    +-- Settings routes  --> HTML forms
    +-- Webhook route    --> GitHub push events
    +-- WebSocket route  --> Real-time updates
    |
internal/github.Client    --> GitHub REST API v3
internal/forgejo.Client   --> Forgejo/Gitea REST API v1
    |
internal/sync.Syncer      --> Periodic + on-demand sync
    |
ent.Client (ORM)       --> SQLite
```

## Prerequisites

- Go 1.26+
- Node.js 24+ (for TypeScript compilation)
- C compiler (for CGO SQLite driver)
- A [GitHub OAuth App](https://github.com/settings/developers) (optional if using Forgejo only)
- A Forgejo/Gitea instance with OAuth2 application (optional if using GitHub only)

## Setup

### 1. Register a GitHub OAuth App

Go to https://github.com/settings/developers and create a new OAuth App:

| Field | Value |
|---|---|
| **Application name** | `GitLens (dev)` (or any name you like) |
| **Homepage URL** | `http://localhost:6270` |
| **Authorization callback URL** | `http://localhost:6270/auth/github/callback` |

After creation, you will receive a **Client ID** and a **Client Secret**. Save these — you'll need them in the next step.

> **For production:** Replace `localhost:6270` with your actual domain in all URLs above, and set `GITHUB_REDIRECT_URL` accordingly.

### 2. Configure environment variables

Copy the example env file and fill in your credentials:

```bash
cp .env.example .env
```

Edit `.env` and set at minimum:

```env
GITHUB_CLIENT_ID=your_client_id_here
GITHUB_CLIENT_SECRET=your_client_secret_here
```

For local development the defaults for `PORT`, `DB_PATH`, and `GITHUB_REDIRECT_URL` are fine. See the [configuration table](#configuration) below for all options.

### (Optional) Register a Forgejo OAuth2 Application

In your Forgejo instance, go to Settings → Applications → OAuth2 Applications and create a new application:

| Field | Value |
|---|---|
| **Application name** | `GitLens` |
| **Redirect URI** | `http://localhost:6270/auth/forgejo/callback` |

After creation, note the **Client ID** and **Client Secret**. Set these as `FORGEJO_CLIENT_ID` and `FORGEJO_CLIENT_SECRET` in your environment.

> For production: Set `FORGEJO_REDIRECT_URL` to your deployed callback URL and `FORGEJO_DEFAULT_URL` to your Forgejo instance base URL (e.g. `https://codeberg.org`) so users don't have to type it on every login.

### 3. Install dependencies

```bash
task install
```

This runs `go mod download`, `npm ci`, installs Playwright browsers, and compiles TypeScript.

### 4. Run

```bash
task run
```

Open http://localhost:6270 and log in with GitHub.

## Configuration

All configuration is done via environment variables. For local development, place them in a `.env` file (see `.env.example`).

| Environment Variable | Default | Description |
|---|---|---|
| `PORT` | `6270` | HTTP listen port |
| `DB_PATH` | `gitlens.db` | SQLite database path |
| `GITHUB_CLIENT_ID` | — | GitHub OAuth App client ID **(required)** |
| `GITHUB_CLIENT_SECRET` | — | GitHub OAuth App client secret **(required)** |
| `GITHUB_REDIRECT_URL` | `http://localhost:6270/auth/github/callback` | OAuth callback URL (must match your OAuth App settings) |
| `GITHUB_WEBHOOK_SECRET` | — | HMAC-SHA256 secret for webhook payload verification |
| `FORGEJO_CLIENT_ID` | — | Forgejo OAuth2 application client ID (optional) |
| `FORGEJO_CLIENT_SECRET` | — | Forgejo OAuth2 application client secret (required if FORGEJO_CLIENT_ID set) |
| `FORGEJO_REDIRECT_URL` | `<APP_URL>/auth/forgejo/callback` | Forgejo OAuth callback URL |
| `FORGEJO_DEFAULT_URL` | — | Default Forgejo instance base URL (e.g. `https://codeberg.org`). If set, users skip the instance URL prompt on login. |
| `DEPLOY_TARGETS` | — | JSON array of deploy targets for release-triggered Docker updates (see [docs/deploy-targets.md](docs/deploy-targets.md)) |
| `DEPLOY_TARGETS_FILE` | — | Path to JSON file containing deploy targets (alternative to DEPLOY_TARGETS) |
| `DEPLOY_BACKEND` | `api` | Deploy backend: `api` (docker pull + recreate) or `compose` (docker compose pull + up) |
| `DEPLOY_ALLOW_PRERELEASE` | `false` | Set to `true` to allow prerelease GitHub releases to trigger deploys |
| `GOTIFY_URL` | — | Gotify server base URL (required for deploy notifications) |
| `GOTIFY_TOKEN` | — | Gotify application token (required for deploy notifications) |
| `KUMA_URL` | — | Uptime Kuma base URL (enables linking labeled containers to monitors) |
| `KUMA_USERNAME` | — | Uptime Kuma login username (required if KUMA_URL set) |
| `KUMA_PASSWORD` | — | Uptime Kuma login password (required if KUMA_URL set; never commit) |
| `NPM_URL` | — | Nginx Proxy Manager base URL (enables linking labeled containers to proxy hosts) |
| `NPM_IDENTITY` | — | NPM login email (required if NPM_URL set) |
| `NPM_SECRET` | — | NPM login password (required if NPM_URL set; never commit) |

## Release → Docker Deploy

GitLens can act as a lightweight CD agent: when one of your projects publishes a new
release on GitHub, it pulls the latest container image and recreates the matching
container so it runs the new version.

The full guide — concept, both configuration methods, worked examples, backend
choice, and troubleshooting — lives in **[docs/deploy-targets.md](docs/deploy-targets.md)**.
The quick version:

1. **Add the `release` event** to your GitHub webhook (the existing push webhook
   works — just add the event; the URL stays `/webhook/github`)
2. **Define deploy targets** one of two ways:
   - **Explicit config** via `DEPLOY_TARGETS` (JSON array) or `DEPLOY_TARGETS_FILE`:
     ```env
     DEPLOY_TARGETS=[{"repository":"martynvdijke/gitlens","image":"ghcr.io/martynvdijke/gitlens","container":"gitlens","tag_strategy":"release_tag"}]
     ```
   - **Container label auto-discovery** (alternative): add the label
     `gitlens.deploy.target=owner/repo` to any running container. GitLens inspects
     Docker on startup and infers image, container, and tag strategy from the runtime.
     Both methods can be combined; explicit targets win for the same repository.
3. **Mount the Docker socket** so GitLens can reach Docker (the official image
   includes the Docker CLI; the repo's `docker-compose.yml` mounts the socket
   already — needed for label discovery and container recreation):
   ```yaml
   volumes:
     - /var/run/docker.sock:/var/run/docker.sock
   ```
4. **(Optional) Configure Gotify** for deploy notifications (`GOTIFY_URL`, `GOTIFY_TOKEN`)

Tag strategy options:
- `release_tag` (default): uses the release tag, stripping the leading `v` prefix (e.g. `v1.2.3` → `1.2.3`)
- `latest`: always pulls `:latest`

> ⚠️ Use `DEPLOY_BACKEND=compose` for compose-managed containers — the default `api`
> backend recreates them without their compose env/network/volume configuration.

## Service Links (Uptime Kuma, NPM, Authelia)

On the **Deploy** tab, every container discovered via the `gitlens.deploy.target`
label can be manually linked to three external services. Links are stored in
SQLite (new `service_links` table, created automatically on startup) and shown
with per-service status badges.

- **Uptime Kuma** — link a container to an existing monitor by ID, or click
  *Add monitor* to auto-create an HTTP monitor from the container's published
  port. Status shows up/down. Enabled by setting `KUMA_URL`, `KUMA_USERNAME`,
  and `KUMA_PASSWORD`.
- **Nginx Proxy Manager (NPM)** — link a container to an existing proxy host by
  hostname, or click *Add proxy host* to auto-create one (WebSocket support on,
  forwarding to the container's hostname and published port). Status shows
  enabled/disabled. Enabled by setting `NPM_URL`, `NPM_IDENTITY`, and
  `NPM_SECRET`.
- **Authelia** — link a container by domain and copy an
  `access_control.rules` YAML snippet (bypass for Authelia's one-time-password
  endpoint, then deny for the domain) to paste into Authelia's `config.yml`.
  No env vars needed.

When a service is not configured, its buttons are disabled and show
"Not configured". The full guide lives in
**[docs/deploy-targets.md](docs/deploy-targets.md)**.

## Docker

### Local build

```bash
docker compose up --build
```

The Docker Compose setup reads `.env` automatically (via the `env_file` directive) and mounts volumes for persistent database and data storage. The app is available on port 6270.

Make sure you have created a `.env` file with your `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET` before running.

### Using the published image

```bash
docker run -p 6270:6270 --env-file .env ghcr.io/martynvdijke/gitlens:latest
```

Pulling from `ghcr.io` requires authentication if the package is private. See [GitHub Packages docs](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry) for setup.

## Development

### Available tasks

| Task | Description |
|---|---|
| `task run` | Run the application |
| `task dev` | Run with hot-reload (Air) |
| `task test` | Run Go unit tests |
| `task test:e2e` | Run Playwright E2E tests |
| `task build` | Compile TypeScript and build Go binary |
| `task fmt` | Format Go code |
| `task generate` | Regenerate Ent ORM code from schema |
| `task tidy` | Tidy Go modules |
| `task docker:build` | Build Docker image |

### Running tests

```bash
# Unit tests
task test

# E2E tests (requires built binary)
task build
task test:e2e
```

## Project Structure

```
.
├── main.go                  # Application entry point
├── internal/
│   ├── forgejo/             # Forgejo/Gitea API client
│   ├── github/              # GitHub API client
│   ├── provider/            # Provider abstraction (GitHub + Forgejo interface)
│   ├── handlers/            # HTTP handlers (auth, dashboard, settings, webhook)
│   ├── middleware/           # Session management middleware
│   ├── sync/                # Data synchronization engine
│   ├── deploy/              # Docker deploy: config, discovery (container labels), pull + recreate / compose
│   ├── gotify/              # Gotify push notification client
│   └── ws/                  # WebSocket hub
├── ent/
│   ├── schema/              # Ent ORM schemas (User, Repository)
│   └── ...                  # Auto-generated Ent code
├── static/                  # Static files served to browser
│   ├── index.html           # Main HTML template (Go templates)
│   ├── style.css            # Stylesheet
│   └── favicon.ico          # Favicon
├── ts/                      # TypeScript source files
├── e2e/                     # Playwright E2E tests
├── Taskfile.yml             # Task runner tasks
├── Dockerfile               # Multi-stage Docker build
├── docker-compose.yml       # Docker Compose config
└── .releaserc.yaml          # Semantic release configuration
```

## Routes

| Route | Method | Description |
|---|---|---|
| `/` | GET | Home / dashboard |
| `/auth/github` | GET | GitHub OAuth login |
| `/auth/github/callback` | GET | GitHub OAuth callback |
| `/auth/forgejo` | GET | Forgejo OAuth login (optional `?instance=`) |
| `/auth/forgejo/callback` | GET | Forgejo OAuth callback |
| `/auth/logout` | POST | Logout |
| `/dashboard` | GET | Dashboard partial |
| `/repos` | GET | Repository list (HTMX partial) |
| `/repos/{id}/sync` | POST | Sync single repo |
| `/repos/import-all` | POST | Import all GitHub repos |
| `/repos/{id}` | DELETE | Remove tracked repo |
| `/settings` | GET | Settings page |
| `/settings/interval` | POST | Update sync interval |
| `/settings/repos/available` | GET | List available repos |
| `/settings/repos/select` | POST | Import selected GitHub repos |
| `/settings/forgejo/disconnect` | POST | Disconnect Forgejo account |
| `/settings/forgejo/available` | GET | List available Forgejo repos |
| `/settings/forgejo/select` | POST | Import selected Forgejo repos |
| `/settings/forgejo/warning/dismiss` | POST | Dismiss cross-provider warning for a repo |
| `/repos/{id}/prs` | GET | List pull requests for a repo |
| `/repos/{id}/prs/{number}/merge` | POST | Merge a single pull request |
| `/repos/{id}/prs/{number}/close` | POST | Close a single pull request |
| `/repos/{id}/prs/merge-all` | POST | Merge all open pull requests |
| `/repos/{id}/prs/close-all` | POST | Close all open pull requests |
| `/prs/merge` | POST | Merge a single PR from the unified queue |
| `/prs/batch-merge` | POST | Merge selected PRs from the unified queue |
| `/prs/close` | POST | Close a single PR from the unified queue |
| `/prs/batch-close` | POST | Close selected PRs from the unified queue |
| `/webhook/github` | POST | GitHub webhook — push (sync) and release (deploy) events |
| `/ws` | GET | WebSocket endpoint |

## License

MIT
