# Object Stores

## What this is

An **object store** is a named, S3-compatible endpoint FlatRun knows about. One
abstraction covers every store, whoever runs it, so backups, deployments, and the
UI treat them all the same: each resolves to `(endpoint, region, bucket, credential)`
and is reached through one S3 client.

This supersedes the earlier standalone "backup destination": a backup destination
is now just an object store used as a backup target.

## Two kinds

A store's `kind` records **who runs the store**, nothing else:

- **external**: a store running somewhere else that FlatRun only connects to. You
  provide an endpoint and credentials. FlatRun does not manage its lifecycle.
  Examples: AWS S3, Cloudflare R2, Backblaze B2, an existing MinIO.
- **managed**: a store FlatRun runs itself, deployed from a template as a container
  on the host (MinIO, SeaweedFS, Garage). Because it is a normal FlatRun deployment,
  FlatRun knows its endpoint, issues its credentials, and can start, stop, and back
  it up like any other deployment. Its data lives in a flat-file bind mount.

Everything downstream (browsing objects, using a store as a backup target, mounting
it into a deployment) is identical across kinds.

## Data model

```
ObjectStore
  id            string
  name          string
  kind          "external" | "managed"
  endpoint      string          # empty means AWS default
  region        string
  bucket        string
  prefix        string          # optional key prefix
  credential_id string          # references an s3 credential in the credential manager
  use_path_style bool
  deployment    string          # managed only: the deployment that runs the container
  enabled       *bool           # nil means enabled
  backup_target bool            # whether backups mirror to this store
```

Secrets never live on the store record. `credential_id` points at an S3 credential
held by the credential manager (written 0600, masked in API responses). For a managed
store, the credential is the one issued to its container at deploy time.

## How backups use it

The backup manager mirrors each new archive to every enabled store with
`backup_target = true`, on top of the always-local copy. List, download, and restore
fall back to a store when the local archive has been pruned. This is the existing
mirror mechanism; only the source of the target list changes from "destinations" to
"object stores marked as backup targets".

## Managed stores via templates

A managed store is created through the normal deploy flow using an object-store
template. A template is a directory under `agent/templates/<name>/` with
`metadata.yml` and `docker-compose.yml`, auto-discovered from the embedded set.

Deploying one:
1. User picks an object-store template (MinIO, SeaweedFS) and deploys it. The
   template generates a root credential per deployment.
2. FlatRun registers a `managed` object store linked to that deployment, deriving
   the endpoint from the container and storing the issued credential.
3. The store then appears alongside external stores and can be used as a backup
   target or, later, mounted into other deployments.

Data stays in the deployment's flat-file `./data` mount, consistent with the
no-hidden-volumes philosophy.

## Slices

1. **Abstraction + managed template** (this cycle): the store model with `kind`,
   a MinIO template (SeaweedFS and Garage to follow), and folding backup
   destinations into stores. The Object Stores UI lists external and managed stores
   and can add an external store or deploy a managed one.
2. **Object browser**: list and manage buckets and objects in a store from the UI
   (upload, download, delete), the "visualization" half of the feature.
3. **Deployment consumption**: inject a store's endpoint and issued credentials into
   another deployment's environment so apps can use it directly.
4. **Replication**: sync one store to another (managed to external for offsite, or
   external to managed for local cache), on a schedule.

## API sketch

- `GET /object-stores` list stores (external and managed), non-secret.
- `POST /object-stores` register an external store.
- `PUT /object-stores/:id`, `DELETE /object-stores/:id`.
- `POST /object-stores/:id/test` connectivity probe (write and delete).
- `POST /object-stores/managed` deploy a managed store from a template, returning the
  new store plus the deploy job.
- S3 credentials continue through `/storage-credentials`.
