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
`PUT /api/deployments/:name/domains/:domainId`.

| Field | Type | Default | What it does |
|-------|------|---------|--------------|
| `proxy_timeout` | seconds | 60 | Proxy read/send timeout. Raise it for a domain that proxies long-lived WebSocket connections so an idle socket is not closed mid-connection. |
| `static_cache` | bool | off | Opt the domain into a long browser cache for static assets (css, js, images, fonts). It applies only to responses whose request path has a static extension; dynamic responses keep the app's own cache headers. |

Both are opt-in per domain: a domain that never sets them keeps the stock proxy
behaviour. Because the fields live in `service.yml`, the same values move with
the deployment directory.

## Container resource limits

Memory and CPU caps are applied to the running container, not the compose file,
so they take effect without a rewrite or a restart. Set them from the UI or over
the API:

- `GET /api/deployments/:name/resources` reads the current limits.
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
