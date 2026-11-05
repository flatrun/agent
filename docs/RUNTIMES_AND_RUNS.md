# Runtimes and runs

## Goal

FlatRun deploys long running Compose services as Docker containers. Two new kinds of work need more than that:

1. **Untrusted, run to completion work.** A CI job clones a repository, builds and starts the application, drives a browser against it and uploads what it produced. The code is not the operator's, so it must not share a kernel with anything else.
2. **Long running services in virtual machines.** A service that should be isolated from its neighbours by a VM boundary, but otherwise deploys, routes and rolls back like any other service.

Both need a VM runtime underneath. They differ in how long the work lives. This plan keeps those two questions apart.

## Model

Three independent choices describe any workload:

| Layer | Question | Options |
| --- | --- | --- |
| Runtime | What isolates the code | Docker container (today), Firecracker microVM (new) |
| Lifecycle | How long it lives and what success means | Service (today), Run (new) |
| Placement | Which node it lands on | Standalone, Swarm, k3s (today, `orchestrator.Provider`) |

A **service** has a desired state: it is kept running, restarted, health checked, routed and rolled back. This is what a deployment is today.

A **run** executes once to completion. It has a hard timeout, collects artifacts before teardown, and is always destroyed afterwards, whatever its outcome.

Every runtime serves every lifecycle:

| | Docker | Firecracker |
| --- | --- | --- |
| Service | Deployments today | Services in VMs |
| Run | Trusted one off work | CI jobs, untrusted code |

Placement is unchanged by this plan. A node advertises which runtimes it has, and placement only offers it work it can run.

## Constraints

- A project stays a directory with a Compose file. A service chooses its runtime with the `runtime` field the Compose spec already defines (`ServiceConfig.Runtime` in compose-go). A project must keep working under plain `docker compose` on any host where that runtime is installed.
- "Job" already names the in-memory action jobs on deployments (`internal/api/jobs.go`), and "task" names scheduler entries. The new lifecycle is called a **run** and lives under `/api/runs`.
- Firecracker needs `/dev/kvm`: bare metal, or a host that exposes nested virtualisation.
- A run may execute untrusted code. Nothing from the host enters it except what its spec lists. Outbound network is denied by default and opened per run. Secrets are passed when the run starts and are never written to the run record.
- Runs get their own permissions (`runs:read`, `runs:write`), following the authorization rules in `AGENTS.md`. Placing runs across the fleet adds a cluster capability (`runs.execute`) in Phase 5.
- Existing deployments, their API and their on-disk layout do not change.

## Interfaces

### Runtime

The runtime knows how to create, run and destroy one isolated instance. It knows nothing about services, runs, routing or schedules. It lives in `internal/runtimes`.

```go
package runtimes

type Kind string

const (
	KindDocker      Kind = "docker"
	KindFirecracker Kind = "firecracker"
)

type Spec struct {
	Image     string // OCI reference; Firecracker builds a root filesystem from it
	Command   []string
	Env       map[string]string
	WorkDir   string
	Resources Resources // CPUs and memory bytes; zero means no limit
	Network   Network   // no network unless Egress is set
	Labels    map[string]string
}

type Runtime interface {
	Kind() Kind
	Available(ctx context.Context) error // explains why not, e.g. no /dev/kvm
	Create(ctx context.Context, spec Spec) (Instance, error)
}

type Instance interface {
	ID() string
	Start(ctx context.Context) error
	Wait(ctx context.Context) (Exit, error)
	Logs(ctx context.Context, follow bool) (io.ReadCloser, error)
	CopyOut(ctx context.Context, path string) (io.ReadCloser, error) // tar stream
	Stop(ctx context.Context, grace time.Duration) error
	Destroy(ctx context.Context) error // removes every resource the instance created
}

// Implemented by runtimes that can find instances a previous process left behind.
type LeftoverCleaner interface {
	Leftovers(ctx context.Context) ([]string, error)
	DestroyByID(ctx context.Context, id string) error
}
```

Every instance carries two labels: its own ID and a scope. The agent sets the scope from a hash of its deployments path, so `Leftovers` and `DestroyByID` only ever touch instances of the agent that created them, even when several agents share one Docker daemon.

Docker networking:

- An instance without egress gets no network at all (`none`).
- Instances with egress share one bridge, `flatrun-runs-egress`, with traffic between containers disabled. Instances cannot reach each other, and the node does not spend an address pool per instance. On a host whose default pools are used up, `runs.egress_subnet` pins the bridge to a free CIDR.

Containers share the host kernel, so the Docker runtime is not a boundary for untrusted code. It serves trusted runs and is the reference the Firecracker runtime must match.

Planned for Phase 3, once Firecracker exists: `Exec`, explicit mounts, and a `Snapshotter` for runtimes that can boot from a saved memory image.

### Run

A run is a runtime instance plus a lifecycle: queued, started, watched, timed out, harvested, destroyed. It lives in `internal/runs`.

```go
package runs

type Spec struct {
	Runtime        runtimes.Kind // docker today
	Image          string
	Command        []string
	Env            map[string]string
	WorkDir        string
	CPUs           float64
	MemoryBytes    uint64
	Egress         bool
	TimeoutSeconds int      // whole run, image pull included; default 600, at most 3600
	ArtifactPaths  []string // absolute, at most 16, unique base names
}

type Status string

const (
	StatusQueued    Status = "queued"
	StatusStarting  Status = "starting"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusTimedOut  Status = "timed_out"
	StatusCancelled Status = "cancelled"
)
```

| Method and path | Permission | Does |
| --- | --- | --- |
| `POST /api/runs` | `runs:write` | Queues a run |
| `GET /api/runs` | `runs:read` | Lists runs, newest first; `limit` defaults to 100, at most 500 |
| `GET /api/runs/:id` | `runs:read` | Status, exit code, error, timings, artifact list |
| `GET /api/runs/:id/logs` | `runs:read` | Output as text; `follow=true` streams until the run ends |
| `GET /api/runs/:id/artifacts/:name` | `runs:read` | Downloads one artifact as a tar archive |
| `DELETE /api/runs/:id` | `runs:write` | Cancels a queued or running run |

Behaviour:

- **Ownership.** A run belongs to the user who submitted it. Only an admin whose credential carries no deployment restriction sees every run; everyone else, including an admin's API key limited to some deployments, sees only their own. A run outside the actor's view answers 404, the same as a missing one. The effective role is the API key's role when a key is used, so a key can narrow its user's view but never widen it.
- **Secrets.** `secret_env` in the request is merged into the instance environment at start. Its values are never written to the run record, the database or the audit log (run routes are audited without bodies), and they are masked as `[REDACTED]` in the output and in error messages.
- **Storage.** Records live in SQLite at `<deployments>/.flatrun/runs.db`. Output and artifacts live in `<deployments>/.flatrun/runs/<id>/`.
- **Restarts.** On start the agent marks every unfinished run `failed` with "agent restarted" and destroys its own leftover instances.
- **Failures.** A failed run carries the runtime's error, for example the image pull error. A run that succeeded but could not collect a requested artifact keeps `succeeded` and records the missing path in `error`.
- **Unavailable runtime.** If the runtime or storage cannot start, run endpoints answer 503 and the rest of the agent keeps working.
- **Configuration.** `runs.workers` (default 2) sets how many runs execute at once. Further runs wait in a queue of 128.

### Services on a VM runtime

Two ways to give a Compose service a VM boundary. Phase 0 decides between them.

- **A: VM backed container runtime.** Kata Containers, configured with Firecracker as its hypervisor, is registered as a Docker runtime on the node. A service sets `runtime: kata-fc`. The deployment code path is unchanged: FlatRun validates that the node has the runtime and places the service accordingly.
- **B: FlatRun drives Firecracker itself.** The service lifecycle gains a second backend beside `docker compose`, built on the runtime interface above.

A is preferred. It keeps the Compose file as the source of truth and keeps the project portable.

## Phases

Each phase merges on its own and leaves existing deployments untouched.

### 0. Spike on one KVM host

Answer, with measurements:

- Cold boot time and boot time from a snapshot.
- Memory overhead of an idle microVM.
- Whether a Docker daemon runs inside a microVM, for runs that start a Compose backend.
- Whether headless Chrome runs inside a microVM.
- Whether Kata with Firecracker, registered as a Docker runtime, runs an existing FlatRun project with one service switched by `runtime:`.

Output: the numbers, and a choice between A and B for services.

### 1. Runtime interface, Docker implementation

Built. The `runtimes` package with a Docker implementation. Only runs use it; deployments keep their current path.

Covered by tests against Docker: a run to completion with logs and a copied out file, a non zero exit, stop of a long running instance, no network by default, instances on the egress bridge unable to reach each other, removal of leftovers, and cleanup that leaves another scope's running instance alone.

### 2. Runs on Docker

Built: run storage, queue, timeout, cancellation, log streaming, local artifacts, secrets, permissions, and the Runs page in the UI.

Covered by tests through HTTP:

- A run that writes a file and exits reports `succeeded`, and the file downloads as an artifact.
- A run past its timeout is killed and reports `timed_out`, including one still stuck in setup.
- A cancelled run stops and reports `cancelled`; a cancelled queued run never starts.
- Each user sees and cancels only their own runs, and an API key scoped narrower than its user cannot widen it.
- A secret value never appears in the run JSON, the record, the output or the audit log.
- A run whose image does not exist reports `failed` with the pull error.

Not built yet: upload of artifacts to an object store (`docs/OBJECT_STORES.md`), the final status webhook, retention of old runs, and the `runs.execute` cluster capability, which belongs to Phase 5.

### 3. Firecracker runtime

Root filesystem built from an OCI image and cached, jailer, tap networking with a default deny policy, optional snapshot pool. Nodes advertise the runtime when `Available` succeeds.

Done when:

- The phase 2 tests pass with `runtime: firecracker`.
- A run that starts headless Chrome and saves a screenshot succeeds.
- After every run, no VM process, tap device, socket or root filesystem copy remains.
- A run cannot reach the host, the node's other runs, or the network beyond its policy.

### 4. Services on a VM runtime

Path A or B, as Phase 0 decided. The UI offers a runtime per service, limited to runtimes the target node has.

Done when: a Compose service on the VM runtime deploys, routes over HTTPS, restarts on failure and rolls back like a container service.

### 5. Runs across the fleet

The cluster places runs on nodes that have the runtime, the capacity and the `runs.execute` capability, and queues them when none has room.

Done when: runs submitted to one node execute on a peer that has capacity, and their logs and artifacts are reachable from the submitting node.

## Open questions

- Root filesystem format: an ext4 image per OCI image, cached, or a devmapper thin pool.
- Whether runs also use Kata, if Phase 0 shows it meets the boot time and teardown needs, or always use FlatRun's own Firecracker runtime.
- Usage metering per run (CPU seconds, memory, wall time) for operators who bill for runs.
- Secrets by reference to the secrets store, alongside inline `secret_env`.
- How long finished runs, their output and their artifacts are kept on the node.
