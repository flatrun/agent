# Changelog

## [0.4.0-beta.3] - 2026-08-05

Third beta of the Albacore release, focused on the logs pillar of observability and a network-alert correctness fix.

### Added
- Structured container logs: each line is parsed into a record (service, timestamp, level, message, and JSON fields) so a viewer can render logs as rows instead of a text block
- Log sources beyond container stdout: known kinds (laravel, wordpress, nginx) expose their conventional log files, and any deployment can point the viewer at a file under its own directory, confined to it; the assistant tools read these sources too

### Fixed
- Network alerts no longer fire on idle containers: network metrics were a container's lifetime byte total, so a "network above X" rule crossed any threshold given enough uptime and stayed firing; the store now records a per-second rate, so alerts and charts reflect current throughput and a counter reset reads as zero
- Log level is read only from real header positions (line start, a channel.LEVEL tag, or a level= field), so a stack frame naming a class like ErrorHandler is no longer flagged as an error
- An all-logs read of a file source is bounded so a very large file cannot exhaust memory, and a stopped container's network baseline is dropped rather than retained
- The "all logs" selection returns all container output: it was passing a zero line count, which reads as no lines for a snapshot and was otherwise capped at a hundred

## [0.4.0-beta.2] - 2026-08-01

Second beta of the Albacore release, continuing the deployment lifecycle and self-healing work on top of beta.1. Some Albacore items remain in progress and are not in this beta.

### Added
- Deploy from a git repository: the source is fetched behind a provider interface with a registry (so upload and webhook delivery can register the same way), fetched into a temporary directory and required to contain a compose file before anything is created, with private repositories authenticating from a token held in the credential manager and scrubbed from logs
- Channel-aware updates: an opt-in prerelease channel so `update` can see betas, reading the full releases list with proper semver ordering, exposed over the API and surfaced as an Administration > Updates view showing the current version, the versions available to install, each version's changelog, and a one-click update that reports the restart
- Host metrics: host CPU, memory and disk collected as alertable time series using working-set memory, so a saturated machine has a signal where before there were only per-container percentages against each container's own limit
- Scheduled agents: an agent file can declare a cron schedule and a permission grant, running unattended under an actor carrying only those permissions, auto-approving the granted tools and denying the rest, failing closed so a scheduled run can never quietly do more than it was trusted to (no grant means read-only)
- Agent run history listed on its own, since each run is already a session tagged with its agent
- Agent governance policy enforcing step budgets and dry runs

### Changed
- A firing alert carries a snapshot of the containers consuming the most of the resource, so the notification and the stored event name what was responsible; a rule can deliver to a chosen subset of notification targets, and can opt into restarting the offending deployment under the self-heal guardrails (managed deployments only, with a cooldown so it can't flap into a restart loop), notify-only by default
- Compose-file backups are pruned to the five most recent on each successful rewrite, so a frequently-updated deployment stops accumulating timestamped backups while one rollback copy remains

## [0.4.0-beta.1] - 2026-07-29

First beta of the Albacore release: full deployment lifecycle and self-healing operations. Some Albacore items remain in progress and are not in this beta.

### Added
- Observability with a native time-series UI: OTel-semconv container metrics and per-deployment serving metrics (rate, errors, average and p95) drawn from the proxy's own request record, on-disk history folded to per-minute points so the 6h and 24h ranges hold, OTLP export with a scrape endpoint, and log following
- Self-healing for FlatRun-managed deployments: unhealthy containers restart with capped retries and a cooldown so an external container can't trigger a restart loop, and the watcher reports when it gives up
- Notification system with email and webhook targets and test-send; metric thresholds alert through the configured targets
- Plugin framework: a plugin can inject UI sections, contribute settings, and expose tools the AI assistant can call
- S3-compatible remote backups to AWS, R2, B2 or MinIO on top of the always-local copy, best-effort so a remote outage never fails a backup, with object-storage secrets held in the credential manager; seeds an object-store abstraction and a MinIO template
- AI assistant tools to write a deployment file, run a quick action, start/stop/restart a deployment, and summarize a deployment's security events, each requiring deployment write access and honoring protected mode
- MCP server exposing the shared assistant tool set, and a deployment-scoped file editor whose state-changing tools pause for per-call approval even in auto-run sessions
- Agents defined as flat markdown files in `.flatrun/agents/`, run by the runtime through the shared tool set so permission gates, protected mode, secret redaction and the state-change approval pause all carry over; created via the assistant, the panel editor, or by dropping a file
- Listing of saved AI chat sessions, most recent first, each titled from its first message; reopening one restores its transcript and scope
- Firewall enforcement: a saved policy is translated to nftables and applied, re-applied at startup and removed when disabled, touching only FlatRun's own table and always keeping loopback, established connections and the active SSH port open
- Opt-in per-domain long browser cache for static assets, keyed off the request path's extension
- Start/stop/restart run as background jobs whose status and buffered output survive a page reload, streamed over websocket with a poll fallback and serialized per deployment
- Seeding an empty bind mount from image or running-container content, browsing a running service's files onto the host, and unmounting tagged paths
- Routing-only hostnames for externally-fronted proxies, sharing the primary domain's certificate over SNI and kept out of issuance and renewal
- Dashboards screen to create, arrange and manage panels over container and serving metrics

### Changed
- Deployment status comes from a single Engine API query across all deployments instead of per-deployment `docker compose ps`, so listing is flat with the deployment count rather than scaling with it (list ~486ms to ~26ms at 50 deployments)
- Proxy pools connections to upstreams with real keepalive, preserving the restart rediscovery the previous per-request routing provided where the nginx image supports `resolve`; serving also gains HTTP/1.1 to upstreams, buffered access logging, wider gzip and larger proxy buffers
- Certificate auto-renewal defaults on and runs whenever certbot is enabled, with each certificate's own setting winning over the global default; single renew reports success only when something was reissued
- Nginx vhost generation: configurable WebSocket timeouts, conditional `Connection: upgrade` via a map, `ssl_stapling` skipped when the certificate has no OCSP responder, and the target service and port validated before a vhost is saved
- Force-recreate, no-cache rebuild and non-cached pull exposed so updated env vars and images take effect without a manual compose run
- AI assistant loop runs on the shared agents runner with per-call tool approval
- UI refresh: dark mode, design tokens, Iconify iconography, a reworked assistant and global search
- Primary-service detection prefers `app`/`web` before the first service with ports
- Example configuration ships placeholder values only

### Fixed
- General-purpose HTTP clients (curl, wget, uptime monitors, webhooks) were matched as attack scanners and blocked on sight; scanner matching is now limited to self-identifying tools and ordinary clients are held to volumetric thresholds, with every auto-block recording the rule, count and paths that tripped it
- Health-watcher restart no longer holds the read lock across the docker call; notification target URLs, including SMTP passwords, are masked in API responses and stored owner-only; the host-wide auto-restart toggle is gated on settings-write rather than a deployment scope
- Additional-domain proxying routed by bare service name and intermittently served another deployment's container on a shared host; it now routes by the unique deployment name
- Compose validation resolves a relative `env_file` against the deployment directory on the image-set and update paths
- `SaveMetadata` failures during compose updates are surfaced instead of silently discarded, so compose and `service.yml` can't diverge
- ACME challenge locations set `access_log off` and `log_not_found off` so cleaned-up challenge 404s stop burying real errors
- Top-level `--help` lists `update`, `setup` and `version`, and `help` prints usage instead of starting the server

## [0.3.0] - 2026-06-08

### Added
- AI assistant: interactive sessions that investigate before answering, grounded in this installation's context, with inline tool-call approval, suggested actions and any OpenAI-compatible provider configured at runtime
- Server-side plans: preview and apply mutations as reviewable artifacts, with opt-in enforcement per deployment
- Optional app template for image and compose deployments: the user's image or compose content is kept while the template contributes container port, default bind mounts, pre-created directories and ownership
- Template-defined environment file generation: prefers the env example shipped inside the deployed image, falls back to template content, generates per-deployment secrets (e.g. Laravel `APP_KEY`) and fills in database credentials
- Interactive system terminal on a real PTY over websocket, with per-line protected-command enforcement and the global terminal disable honored
- Persisted file manager preference for showing hidden files (default on), exposed through the key-based config API
- Template mounts can declare a host path; single-file mounts (such as `.env`) are kept as files
- Per-service image pulling

### Changed
- Mount ownership is applied recursively so nested directories belong to the container user
- Built-in template copies on disk sync automatically when the agent build changes, so upgrades take effect without a manual refresh
- Laravel template: complete storage subdirectory tree, bootstrap cache mount, env file mounted as a file
- Prompts composed by the product (log analysis, operation diagnosis) are kept out of AI session transcripts while still reaching the model
- Nginx reloads when forwarded-proxy trust settings change

### Fixed
- Forwarded client IPs are only trusted when sent by configured proxies, and trusted-proxy entries are sanitized before injection into the nginx Lua layer
- e2e suite aborts on leftover root-owned state instead of failing unrelated tests

## [0.2.0] - 2026-05-25

### Added
- API key edit endpoint with per-deployment access levels (read, write, admin) capped by the owning user's level
- Deployment protected mode: configurable blocked actions and command rules, with an explicit enable switch
- System terminal endpoint, behind its own protected-mode configuration and a new permission
- System file manager endpoint with listing, file and directory creation, chmod, and rename
- Cheap `files-info` variant (`?usage=false`) so the UI can render the system file manager without waiting for a recursive disk-usage walk

### Fixed
- API key creation by an admin key with no associated user no longer returns 401; the owning admin is resolved automatically
- `apiKeyToResponse` emits `null` instead of the zero time for unset `expires_at` and `last_used_at`

## [0.1.56] - 2026-04-09

- Sync `service.yml` when compose expose is updated
- Add `name` field and fix Nextcloud compose config
- Add `--non-interactive` flag to certbot commands

## [0.1.55] - 2026-04-07

- Migrate from `mattn/go-sqlite3` to `modernc.org/sqlite` for pure-Go builds

## [0.1.54] - 2026-04-04

- Add default server block for the nginx welcome page

## [0.1.53] - 2026-04-03

- Distinguish file from directory bind mounts when materializing compose mounts
