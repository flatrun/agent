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

These are opt-in per domain: a domain that never sets them keeps the stock proxy
behaviour. Because the fields live in `service.yml`, the same values move with
the deployment directory.

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

## Container resource limits

Memory and CPU caps are applied to the running container, not the compose file,
so they take effect without a rewrite or a restart. Set them from the UI or over
the API:

- `GET /api/projects/:name/resources` reads the current limits.
- `PUT /api/containers/:id/resources` updates them.

| Limit | Unit | What it does |
|-------|------|--------------|
| `memory_limit` | bytes | Hard memory ceiling for the container. |
| `memory_swap` | bytes | Memory + swap ceiling. |
| `cpus` | cores | Fractional CPU cap (e.g. `1.5`). |
| `cpu_shares` | relative weight | Relative CPU priority under contention. |

## App-level worker counts

How many workers or processes an app runs (for example a PHP-FPM pool, a Gunicorn
worker count, or `WEB_CONCURRENCY`) is the app's own setting, not a FlatRun field.
Set it through the service's environment or command in the deployment's compose
file. FlatRun does not override it, so the app's own tuning stays authoritative.
