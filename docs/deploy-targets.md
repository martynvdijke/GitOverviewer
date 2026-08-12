# Deploy Targets — Release-Triggered Container Updates

GitLens can act as a lightweight CD agent: **when one of your projects publishes a
new release on GitHub, GitLens pulls the latest container image and recreates the
matching container** so it runs the new version. No manual `docker pull` +
`docker compose up` required.

This document covers the concept, the two ways to configure deploy targets, and a
full end-to-end setup.

## The idea in one diagram

```
GitHub repo publishes a release (e.g. v1.2.3)
        │
        │  webhook  (release event)
        ▼
GitHub webhook ──► POST /webhook/github  (GitLens)
        │
        │  1. look up deploy target for "owner/repo"
        │  2. normalize tag: v1.2.3 ─► 1.2.3 (or "latest")
        ▼
docker pull ghcr.io/owner/repo:1.2.3
        ▼
stop old container  ──►  rm old container  ──►  create new container  ──►  start
        │
        └──► optional Gotify notification: "deploy succeeded/failed"
```

The mapping between a GitHub repository and a container is called a **deploy
target**. It answers three questions:

| Question | Field |
|---|---|
| Which repo's release triggers the deploy? | `repository` (GitHub `owner/repo`) |
| Which Docker image do we pull? | `image` |
| Which container do we recreate? | `container` |
| How is the release tag turned into an image tag? | `tag_strategy` |

## Prerequisites

- GitLens must be running somewhere with access to Docker:
  - the **Docker CLI** installed on the host, and
  - the Docker socket mounted/accessible to the GitLens process
    (e.g. `-v /var/run/docker.sock:/var/run/docker.sock`).
- The GitHub repository must have a webhook pointing at GitLens that includes the
  **`release`** event. If you already have a `push` webhook for real-time sync, just
  add the `release` event to the same webhook — no second endpoint needed.
- The image tag that maps to a release must actually exist in the registry
  (e.g. publishing GitHub release `v1.2.3` implies `ghcr.io/owner/repo:1.2.3` was
  built and pushed, usually by CI).

## Two ways to define targets

### Option A — Explicit configuration (env var or file)

Set `DEPLOY_TARGETS` as a JSON array, or point `DEPLOY_TARGETS_FILE` at a JSON file.
The file is read when `DEPLOY_TARGETS` is unset.

```env
DEPLOY_TARGETS=[{"repository":"owner/repo","image":"ghcr.io/owner/repo","container":"myapp","tag_strategy":"release_tag"}]
```

```json
// /etc/gitlens/targets.json
[
  {
    "repository": "owner/repo",
    "image": "ghcr.io/owner/repo",
    "container": "myapp",
    "tag_strategy": "release_tag"
  },
  {
    "repository": "other/project",
    "image": "docker.io/library/other",
    "container": "other-app",
    "tag_strategy": "latest"
  }
]
```

```env
DEPLOY_TARGETS_FILE=/etc/gitlens/targets.json
```

### Option B — Container label auto-discovery

Any running container with the label `gitlens.deploy.target=owner/repo` is
automatically added as a deploy target. On startup GitLens inspects Docker, finds
labeled containers, and **infers the image name, container name, and tag strategy
from the container's runtime config** — so there is nothing else to configure.

```yaml
# docker-compose.yml snippet
services:
  myapp:
    image: ghcr.io/owner/repo:latest
    labels:
      gitlens.deploy.target: "owner/repo"
```

Notes:

- The label value must be in `owner/repo` format (exactly one `/`).
- If the container's image tag is `latest`, the target uses the `latest` strategy;
  otherwise it uses `release_tag`.
- Explicit `DEPLOY_TARGETS` and discovered labels are **merged**. If the same
  repository appears in both, the explicit target wins.

## Target fields

| Field | Required | Description |
|---|---|---|
| `repository` | yes | GitHub full name, `owner/repo`. The allowlist — releases from any other repo are ignored. |
| `image` | yes | Docker image to pull, without a tag (e.g. `ghcr.io/owner/repo`). |
| `container` | yes | Name of the Docker container to recreate. |
| `tag_strategy` | no | `release_tag` (default) or `latest`. See below. |

### Tag strategies

| Strategy | Behavior | Use when |
|---|---|---|
| `release_tag` (default) | Uses the release tag, stripping a leading `v` — `v1.2.3` → pulls `:1.2.3` | Your CI tags images with the version number |
| `latest` | Always pulls `:latest` | Your image is only ever tagged `latest` |

## Full setup, step by step

1. **Mount the Docker socket into GitLens** and make sure the Docker CLI is
   installed on the host:

   ```yaml
   # docker-compose.yml (gitlens service)
   volumes:
     - /var/run/docker.sock:/var/run/docker.sock
   ```

2. **Define your deploy targets** using Option A, Option B, or both (see above).
   You can verify them at any time on the Deploy Targets page: open GitLens and go
   to the **Deploy** tab (or `/deploy`) — it lists every target, the backend in
   use, and whether Gotify notifications are configured.

3. **Add the `release` event to your GitHub webhook.** In the GitHub repo:
   Settings → Webhooks → Edit your webhook, then under *Which events would you
   like to trigger this webhook?* select **Let me select individual events** and
   tick **Releases**. Keep *Pushes* ticked if you want real-time dashboard sync.

   The webhook URL stays the same as for pushes: `https://your-gitlens-host/webhook/github`.

4. **Recommended: set a webhook secret.** Generate one (e.g. `openssl rand -hex 32`),
   put it in the GitHub webhook settings *and* as `GITHUB_WEBHOOK_SECRET` in
   GitLens. GitLens verifies the `X-Hub-Signature-256` header on every request.

5. **(Optional) Configure Gotify notifications:**

   ```env
   GOTIFY_URL=http://gotify:8080
   GOTIFY_TOKEN=abc123
   ```

   Success and failure messages are pushed with the release tag and the
   image:tag that was deployed.

6. **Restart GitLens** and check the logs for the deploy subsystem line:

   ```
   Deploy: 2 target(s) configured, backend=api, gotify=true
   ```

   If you see `Deploy: 0 target(s)` or an error, your targets were not loaded —
   see [Troubleshooting](#troubleshooting).

## What happens on a release (flow details)

1. GitHub sends a `release` webhook. GitLens verifies the signature (if a secret is
   set) and checks the event action.
2. **Only `action: published` triggers a deploy** — drafts, edited, deleted, and
   unpublished releases are ignored.
3. The repository must match a deploy target (`repository` field) — this is a strict
   allowlist, so a webhook for any other repo does nothing.
4. **Prereleases are skipped** unless `DEPLOY_ALLOW_PRERELEASE=true`.
5. The tag is normalized per `tag_strategy` (`v1.2.3` → `1.2.3`, or `latest`).
6. The webhook responds `200` immediately and the deploy runs **asynchronously** in
   the background, so GitHub never times out waiting on `docker pull`.
7. Deploys to the same container are serialized (a per-container lock), so two
   releases in quick succession can't race each other.

### Deploy backends

| `DEPLOY_BACKEND` | What runs | Use when |
|---|---|---|
| `api` (default) | `docker pull` → `stop` → `rm` → `create` → `start` | The container was started with plain `docker run` and needs no env/network/volume wiring on recreate |
| `compose` | `docker compose pull` + `docker compose up -d --no-deps <service>` | The container is managed by docker compose — this preserves its compose-defined env, networks, and volumes |

> ⚠️ The `api` backend recreates the container with **no** ports, volumes, env, or
> network from the old one — it is a bare `docker create`. If the target container
> was started via docker compose, set `DEPLOY_BACKEND=compose` or the recreated
> container will lose its configuration.

## Security notes

- **Allowlist by design:** only repositories listed as targets can trigger a deploy,
  regardless of what the webhook payload says.
- **Signature verification** (`GITHUB_WEBHOOK_SECRET`) is strongly recommended for
  production; without it, anyone who can reach the endpoint can publish fake
  releases.
- **No shell from payloads:** nothing from a webhook payload is ever executed as a
  command — deploys only run fixed `docker`/`docker compose` command lines.
- **The Docker socket is root-equivalent** on the host. Only expose GitLens to
  networks you trust and use an auth layer (it already requires a logged-in user for
  the UI).

## Self-update caveat

If GitLens deploys **its own container** (e.g. container name `gitlens`), the deploy
stops and recreates the very process handling the webhook. GitLens acknowledges the
webhook and logs the deploy before the container is replaced, but expect **brief
downtime**. For zero-downtime updates, put GitLens behind a reverse proxy (Caddy,
Nginx, Traefik) running in a separate container.

## Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| Logs say `Deploy: 0 target(s) configured` | No targets loaded — check `DEPLOY_TARGETS` JSON validity, `DEPLOY_TARGETS_FILE` path, or that a labeled container is running and the socket is mounted. |
| `Deploy: container label discovery failed` | Docker socket not mounted or Docker CLI missing. Explicit `DEPLOY_TARGETS` still work. |
| Release published, nothing happens | Webhook missing the `release` event, action isn't `published`, repo isn't in the allowlist, or the release is a prerelease (set `DEPLOY_ALLOW_PRERELEASE=true` to allow). |
| `pull failed` | The tag doesn't exist in the registry (check CI pushed `image:tag`) or the registry needs auth (`docker login` / `gh auth login` on the host). |
| Recreated container has no ports/env/volumes | Container is compose-managed but `DEPLOY_BACKEND=api`. Switch to `compose`. |
| Webhook returns 401 | `X-Hub-Signature-256` mismatch — the secret in GitHub and `GITHUB_WEBHOOK_SECRET` differ. |

Every deploy attempt is logged with a `Deploy:` prefix, and failures include the
full command output, so check GitLens logs first when debugging.
