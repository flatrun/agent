# GitHub Actions runner performance

## Runner image

Build `images/runner/Dockerfile` for Linux amd64. It includes Go 1.24 and 1.25, Node 22, Chrome, PostgreSQL 16, Poppler PDF tools, compilers, hadolint and Docker CLI plugins. Language setup actions select the requested version from the seeded tool cache. The image does not require a Docker daemon for native database or browser tests.

```sh
docker build -t flatrun-runner:local images/runner
bash images/runner/test.sh flatrun-runner:local
```

The image workflow is manual. It builds, tests and publishes a commit-tagged image. Warm pools require an immutable digest or local image ID. Runner images carry `dev.flatrun.retain=true` so maintenance can preserve them while removing unused build images.

## Docker subscriptions

```yaml
runs:
  workers: 2
events:
  subscriptions:
    - name: ci
      source: github/ci
      action: github_runner
      labels: [self-hosted, flatrun]
      attributes:
        subject: example/project
      github_runner:
        runtime: docker
        image: ghcr.io/flatrun/runner@sha256:<published-digest>
        timeout_seconds: 3600
        cpus: 2
        memory_bytes: 4294967296
        cache: true
        allow_privilege_escalation: false
```

Configure the endpoint and GitHub App as described in [Events](EVENTS.md). Subscription and worker changes require an agent restart. The events API lists subscriptions but does not edit them.

Runner subscriptions default to two CPUs and 4 GiB of memory. Explicit CPU allocations also bound `GOMAXPROCS` and Go package-build parallelism unless subscription environment values override them. The global worker count remains an operator decision. Reserve resources for other host workloads before increasing it.

`allow_privilege_escalation` applies only to Docker. It defaults to false. Enabling it requires a fixed repository and removes `no-new-privileges`; sudo then follows the image's own user and sudoers configuration. It does not make the container privileged, attach a host socket or create a Docker daemon. Enable it only for trusted repository code.

## Persistent caches

`cache: true` creates a separate namespace for each event source, subscription, repository and branch. Missing repository or branch information disables caching for that job. Each concurrent Docker job leases a separate named-volume slot for language installations. Released slots are reused by later jobs. Go build outputs, Go modules and npm downloads share an additional volume within the same branch namespace, so workers can reuse one another's dependencies. These caches support concurrent writers. npm prefers cached downloads and fetches missing packages from its registry.

Managed cache volumes carry `dev.flatrun.cache=true` and the runtime's scope label. The runner image records its last use in `.flatrun-last-used`. A host maintenance policy can remove unused managed caches after an idle retention period. Caches without a valid marker must be retained for inspection.

The tool cache, Go module cache, Go build cache and npm download cache are stored below `/flatrun-cache`. Docker mounts the shared dependency volume at `/flatrun-cache/shared`; tool installations stay in the exclusive slot. Checkouts are disposable. Cache contents are writable by repository code; do not place credentials in them or treat them as an isolation boundary for mutually untrusted contributors.

### Prepared setup snapshots

`flatrun-snapshot --key KEY --path RELATIVE_PATH [--path RELATIVE_PATH ...] -- COMMAND ...` saves declared setup outputs after a successful command and restores them when the same key, paths and command recur. Outputs must be absent before restoration; otherwise the command runs normally. The command runs without snapshot caching when the scoped cache is unavailable. A per-key lock prevents concurrent jobs from publishing partial snapshots. Restores preserve ordinary file permissions, remove special permission bits and use the runner user as owner. Links that escape the checkout and special files are rejected.

The caller must include every relevant input in the key, including source and dependency files, tool versions, platform, setup flags and environment settings. Declare every output needed by later steps. A cache hit does not rerun setup, export environment changes or reproduce undeclared side effects. Do not cache credentials, secrets or outputs shared between untrusted jobs. Use only when the setup is reproducible from its declared inputs. Cache keys and output paths are opt-in for each workload, not specific to a language.

For example, a job can calculate a key from its dependency manifest, lockfile, runtime version and operating system, then call `flatrun-snapshot --key "$KEY" --path node_modules -- npm ci`. Include any additional workspace dependency directories as separate `--path` arguments. Cache volumes retain the existing repository and branch scope and expiration policy.

Other build tools can persist data by configuring their cache directory below `/flatrun-cache`, for example `/flatrun-cache/custom-tool`. Docker keeps that directory in the exclusively leased slot. To share it across Docker workers, use `/flatrun-cache/shared/custom-tool` only when the tool supports concurrent writers. Installing another language does not automatically configure its cache. Branch scoping and retention apply to these directories too.

Configured runner images can be pulled with matching stored registry credentials. Ad hoc run images do not receive those credentials. Authentication is used only for image pulling and is not placed in the run record or job environment.

## Warm Firecracker VMs

On a KVM-capable host, configure a fixed branch and an explicit idle count:

```yaml
attributes:
  subject: example/project
  branch: main
github_runner:
  runtime: firecracker
  image: ghcr.io/flatrun/runner@sha256:<published-digest>
  timeout_seconds: 3600
  cpus: 2
  memory_bytes: 4294967296
  cache: true
  warm_idle: 1
```

Zero is the default and starts every VM on demand. A positive value keeps that many clean, booted VMs ready and replenishes the pool after allocation. The image, CPU allocation, memory, network setting and cache scope must match. Job commands and credentials are configured only after allocation. Unhealthy VMs are destroyed. Used VMs are destroyed, not returned to the pool.

Warm VMs consume memory in addition to running jobs. Each subscription can request at most 32 idle VMs. Firecracker cache slots use exclusively leased 10 GiB sparse disks, with a maximum of 64 slots per scope. Disks remain separate from disposable roots. A remaining VM hard link prevents reuse of its disk after failed cleanup. Disk creation is atomic so an interrupted format cannot become a valid cache.

The existing runtime availability API also reports warm-pool target, ready and starting counts and preparation errors. Run read permissions still apply. Closing the run manager cancels preparation and destroys idle VMs. Startup recovery removes leftover instances before warm pools start.

## Access and persistence

Only administrator-owned subscription configuration can enable caches and privilege escalation. The public run request cannot select those execution options. Existing run ownership, log access and artifact permissions remain unchanged. The runner image and subscription settings have no separate UI editor or export format.

Run records and logs retain their normal persistence. Named cache volumes and Firecracker cache disks survive job destruction. Secrets still enter through the in-memory environment; warm instances are prepared without job credentials.

## Acceptance

- `TestEventHTTPRunner` drives a captured signed workflow-job webhook through the production receiver and checks runner policy and secret redaction.
- `TestDockerRunnerCacheAndPrivilegePolicy` runs real Docker instances against the built image and checks cache reuse, concurrent dependency sharing, separate tool installations and both sudo settings.
- `images/runner/test.sh` checks tools, native PostgreSQL, Go/npm cache reuse with offline second downloads, snapshot isolation, concurrent creation and unsafe archive rejection.
- Warm-pool tests cover clean preparation, fresh replacement, health failure, cache scope and shutdown cleanup.
- A real workflow must pass on the same revision on hosted and self-hosted runners. Compare queue and execution times separately; repeat the self-hosted run to measure cache reuse.

## Limitations

The image is Linux amd64. Native PostgreSQL runs as the non-root image user in Docker. Firecracker currently executes commands as root inside the guest; it does not use the Docker sudo option. Hosts without the KVM device cannot use Firecracker or warm microVMs.

This change does not provide a privileged per-job Docker daemon. Docker-based subscriptions need workflows that use native services and CLI checks, or a separately configured Docker execution environment for container actions and image building.

Warm-pool lifecycle tests do not establish real VM startup performance. Host integration must be exercised on a KVM-capable machine. Docker cache volumes have no size quota. Firecracker cache disks need a host retention policy. Reserve capacity and monitor disk usage.
