# App Templates

## What this is

App templates (WordPress, Laravel, Ghost, static sites, and so on) are the
starting points the deploy flow generates a `docker-compose.yml` from. They no
longer ship inside the agent binary: the agent fetches the catalog from an
external source into an on-disk cache and reads it from there. This lets the
catalog change (fix a template, add a new one) without rebuilding or
redistributing the agent.

Infrastructure services (databases, the reverse proxy) and the default welcome
page stay embedded in the binary. They are runtime content locked to the agent
version, not catalog entries, and are unaffected by everything below.

## Where templates come from

The agent resolves the catalog from the first available source, in order:

1. **Marketplace API** (authoritative) when enabled.
2. **GitHub** (`flatrun/templates`) as the fallback.

The chosen catalog is written into the on-disk cache at
`{deployments_path}/.flatrun/templates`. Listing and deploy always read that
cache, so once a sync succeeds the templates persist across restarts and keep
working during an outage.

The marketplace source is off by default. Enabling it makes the marketplace
authoritative ahead of GitHub with no code change.

## Configuration

```yaml
templates:
  # Where synced templates are cached; empty uses {deployments_path}/.flatrun/templates.
  cache_dir: ""
  # Background resync period in seconds; 0 disables it.
  sync_interval: 3600
  github:
    enabled: true
    repo: flatrun/templates
    ref: main
  marketplace:
    # Enable once the marketplace API is ready; it then takes priority over GitHub.
    enabled: false
    url: ""            # empty uses the agent's default marketplace endpoint
```

The initial sync runs in the background at startup, so a slow or unreachable
source never delays the agent. A manual refresh is available through the
templates refresh endpoint.

## Offline and air-gapped hosts

The on-disk cache is the durable store. Two things follow:

- After the first successful sync, deploys keep working with no network.
- On a host that never reaches a source, populate the cache by hand. Each
  template is a directory under `{deployments_path}/.flatrun/templates/` holding a
  `docker-compose.yml` and an optional `metadata.yml` (plus any files the template
  ships):

  ```
  {deployments_path}/.flatrun/templates/
    wordpress/
      metadata.yml
      docker-compose.yml
  ```

  The agent picks these up the same way it reads synced templates. Set
  `sync_interval: 0` (or disable both sources) if you want the cache left entirely
  under manual control.
