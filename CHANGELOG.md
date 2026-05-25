# Changelog

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
