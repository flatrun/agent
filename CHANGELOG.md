# Changelog

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
