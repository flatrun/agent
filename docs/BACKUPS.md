# Backups

## What is backed up

A backup captures a deployment as a self-contained archive. Each component is
best-effort: a component that cannot be read is logged and skipped, and the rest
of the backup still completes.

| Component | Source | Included |
|-----------|--------|----------|
| Compose file | `docker-compose.yml` or `compose.yml` | Always |
| Env files | `.env`, `.env.flatrun` | Always |
| Deployment metadata | `.flatrun.yml` | Always |
| Mounted data | `data/`, `uploads/`, `storage/`, `config/`, `logs/` under the deployment | Always, when present |
| Container paths | Paths declared in the deployment's backup spec | When configured |
| Databases | MySQL (`mysqldump`) / PostgreSQL (`pg_dump`) dumps of declared services | When configured |

Containers keep running during a backup. Optional pre and post hooks run inside
a service container around the capture.

## How it is stored

Components are staged, described by a `backup.json` manifest, and written as a
single gzip-compressed tar archive named `<deployment>_<timestamp>.tar.gz`.

## Where it is stored

The primary copy is always local:

```
<deployments_path>/.flatrun/backups/<deployment>/<deployment>_<timestamp>.tar.gz
```

If one or more remote destinations are configured, the archive is also uploaded
to each after the local write. A remote upload failure is logged and never fails
the backup, whose local copy already succeeded. A backup's `locations` field
reports where it currently exists (`local` and/or destination names). Local
retention prunes only the on-disk copies; remote copies are governed by the
bucket's own lifecycle policy.

## Remote (S3-compatible) destinations

Any S3-compatible service works (AWS S3, Cloudflare R2, Backblaze B2, MinIO).

1. Store the access key and secret as an S3 credential
   (`POST /api/v1/storage-credentials`, `kind: s3`). Secrets are held by the
   credential manager, written to a `0600` file, and never returned by the API
   or stored in the agent config.
2. Add a destination under `backup.destinations` referencing that credential by
   id (via the config API or `config.yml`). See `config.example.yml` for the
   shape.
3. Validate reachability with `POST /api/v1/backup-destinations/test` before
   relying on it; it performs a probe write and delete.

Restore and download read from the local copy when present, otherwise stream the
archive from the first remote that holds it, so a backup remains usable after
its local copy has been pruned.
