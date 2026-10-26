# Per-deployment tuning

Knobs an operator can turn to change how a single deployment serves traffic and
uses host resources. These are distinct from FlatRun's own dashboard/API speed;
they affect how fast and how reliably a deployed app answers its end users.

Everything here is a flat-file field or a container-level limit, so it stays
readable and portable: the serving knobs live in the deployment's `service.yml`
metadata, and the resource limits are applied to the running container.

## Reverse-proxy serving (per domain)

Each entry under `domains` in a deployment's `service.yml` carries its own
serving knobs. Set them from the UI's domain form, or over the API with
`PUT /api/projects/:name/domains/:domainId` (the whole domain) or
`PATCH /api/projects/:name/domains/:domainId` (only the fields sent).

| Field | Type | Default | What it does |
|-------|------|---------|--------------|
| `proxy_timeout` | seconds | 60 | Proxy read/send timeout. Raise it for a domain that proxies long-lived WebSocket connections so an idle socket is not closed mid-connection. |
| `static_cache` | bool | off | Opt the domain into a long browser cache for static assets (css, js, images, fonts). It applies only to responses whose request path has a static extension; dynamic responses keep the app's own cache headers. |
| `client_max_body_size` | size (`512k`, `64m`, `1g`, `0` for none) | agent default, else nginx's `1m` | Largest request the domain accepts. Larger requests are refused with HTTP 413 before they reach the app. |
| `upload_limit_page` | bool | off | Answer a refused request with a page stating the limit (or `application/problem+json` for API clients) instead of nginx's plain 413 body. A 413 sent by the app itself is never changed. |
| `proxy_buffering` | bool | on | Set to `false` to send the app's response to the client as it is written. Needed for server-sent events and other streaming responses. |
| `response_headers` | map of name to value | none | Headers added to every response. A name matching a default security header (`X-Frame-Options`, `X-Content-Type-Options`, `X-XSS-Protection`, `Referrer-Policy`, and `Strict-Transport-Security` on HTTPS) replaces it; the other defaults stay. Values cannot contain `$`. |
| `canonical_redirect` | `""` or `primary` | `""` | With `primary`, requests for the domain's `aliases` get a permanent redirect to the primary hostname instead of the app. |
| `basic_auth` | `{enabled, users: [{username, password}]}` | off | Ask for a username and password before the request reaches the app. |
| `allow_ips` | list of IPs and CIDR ranges | everyone | Only these clients reach the domain; others get 403. Addresses blocked by the deployment's security settings stay blocked. |

These are opt-in per domain: a domain that never sets them keeps the stock proxy
behaviour. Because the fields live in `service.yml`, the same values move with
the deployment directory.

### Basic auth passwords

The agent hashes each password with SHA-512 crypt before saving and keeps
only the hash (`password_hash` in `service.yml`). A plan made for the change
holds the hash too. To keep a user's password while changing other fields,
send the user with only `username`. The hashes are written to
`conf.d/htpasswd/<deployment>/<domain id>`, which nginx reads from the
`conf.d` mount it already has. SHA-512 crypt is used because the Alpine-based
proxy image does not accept bcrypt hashes.

### Sizing the upload limit

`client_max_body_size` bounds the whole HTTP request, not each file. A
multipart upload wraps every file in boundaries and part headers, and forms
send other fields with it, so the request is always somewhat larger than the
file. Set the limit above the application's own per-file limit: the per-file
limit plus about 10% is a safe starting point (for a 100 MB file limit, use
`110m`). PHP apps enforce `upload_max_filesize` and `post_max_size` as well,
so raise those too.

The agent-wide default is the `nginx.client_max_body_size` config key
(`PUT /api/config/nginx.client_max_body_size`). Changing it re-renders every
installed virtual host.

Every request nginx refuses for its size is counted per domain and listed at
`GET /api/projects/:name/upload-rejections`, and the first one in each
15-minute window is published as a `proxy`/`request_too_large` notification
event.

Each change is checked with `nginx -t` before nginx reloads. If the check or
the reload fails, the previous virtual host stays in place and the API returns
the nginx error.

## Per-service settings

Each compose service can carry settings that FlatRun keeps under
`service_settings` in `service.yml`. Set them from the service panel's
Settings tab, or with
`PUT /api/projects/:name/services/:service/settings` (add `?plan=true` to
review the change first).

| Field | Example | What it does |
|-------|---------|--------------|
| `resources.cpus` | `"0.5"` | CPU limit in cores. |
| `resources.memory` | `512m` | Memory limit; at least `6m`. |
| `restart` | `unless-stopped` | `no`, `always`, `unless-stopped` or `on-failure[:N]`. |
| `logging.max_size` / `logging.max_files` | `10m` / `3` | Rotate the container's json-file log. |
| `environment` | `{"APP_ENV": "production"}` | Variables merged over the compose file's own. Values are literal; `$` is not expanded. |
| `command` / `entrypoint` | `["php-fpm", "-F"]` | Replace the image's command or entrypoint. |

The agent renders the settings into `docker-compose.flatrun.yml` next to the
deployment's compose file and passes it to compose after that file, so the
compose file itself is never rewritten and a redeploy from a repository
keeps the settings. To run the deployment without FlatRun, pass both files:
`docker compose -f docker-compose.yml -f docker-compose.flatrun.yml up -d`.

Every change is checked with `docker compose config` first. If compose
refuses it, the API returns 422 with compose's message and the installed
overlay stays as it was; the overlay it replaces is kept as
`docker-compose.flatrun.yml.bak`. The running container keeps its current
settings until the service is recreated, which the service panel offers
after saving.

## Per-service sleep

A single service can stop after it has been idle and start again on the
next request, while the rest of the deployment keeps running. Turn it on
from the service panel, or with
`PUT /api/projects/:name/services/:service/sleep` and a body of
`{"enabled": true, "idle_minutes": 15}` (at least 5 minutes). It is saved
under `service_sleep` in `service.yml`.

- A service counts as busy while requests are routed to it or its
  containers receive network traffic.
- A service that another running, awake service lists in `depends_on`
  stays up. A database therefore sleeps only after everything that uses it
  is asleep.
- A request to a sleeping service's domain or path shows a short "waking
  up" page. The service's sleeping dependencies start first, then the
  service.
- Deployment-wide sleep, set on the Deploys tab, takes precedence: while it
  is on, per-service sleep settings are kept but not used.

## Container resource limits

Memory and CPU caps can also be applied to a running container directly,
without a restart:

- `GET /api/projects/:name/resources` reads the current limits.
- `PUT /api/containers/:id/resources` updates them. For a container that
  belongs to a deployment, `cpus` and `memory_limit` are also saved to its
  service's settings, so recreating the container keeps them; the response
  reports this as `persisted`.

| Limit | Unit | What it does |
|-------|------|--------------|
| `memory_limit` | bytes | Hard memory ceiling for the container. |
| `memory_swap` | bytes | Memory + swap ceiling. Applied to the running container only. |
| `cpus` | cores | Fractional CPU cap (e.g. `1.5`). |
| `cpu_shares` | relative weight | Relative CPU priority under contention. Applied to the running container only. |

## App-level worker counts

How many workers or processes an app runs (for example a PHP-FPM pool, a Gunicorn
worker count, or `WEB_CONCURRENCY`) is the app's own setting, not a FlatRun field.
Set it through the service's environment or command in the deployment's compose
file. FlatRun does not override it, so the app's own tuning stays authoritative.
