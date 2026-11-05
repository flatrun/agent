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
- A run executes untrusted code. Nothing from the host enters it except what its spec lists. Outbound network is denied by default and opened per run. Secrets are resolved when the run starts and are never written to the run record.
- Runs get their own permissions (`runs:read`, `runs:write`) and their own cluster capability (`runs.execute`), following the authorization rules in `AGENTS.md`.
- Existing deployments, their API and their on-disk layout do not change.

## Interfaces

### Runtime

The runtime knows how to create, run and destroy one isolated instance. It knows nothing about services, runs, routing or schedules.

```go
package runtime

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
	Resources orchestrator.Resources
	Mounts    []Mount       // explicit list; nothing else from the host
	Network   NetworkPolicy // deny by default
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
	Exec(ctx context.Context, cmd []string, io IO) (Exit, error)
	Logs(ctx context.Context, follow bool) (io.ReadCloser, error)
	CopyOut(ctx context.Context, path string) (io.ReadCloser, error)
	Stop(ctx context.Context, grace time.Duration) error
	Destroy(ctx context.Context) error // removes every resource the instance created
}

// Implemented by runtimes that can boot from a saved memory image.
type Snapshotter interface {
	Snapshot(ctx context.Context, inst Instance) (SnapshotRef, error)
	Restore(ctx context.Context, ref SnapshotRef, spec Spec) (Instance, error)
}
```

### Run

A run is a runtime instance plus a lifecycle: queued, started, watched, timed out, harvested, destroyed.

```go
package runs

type Spec struct {
	Runtime   runtime.Kind
	Instance  runtime.Spec
	Timeout   time.Duration
	Artifacts []string    // paths copied out before teardown
	Secrets   []SecretRef // resolved through internal/secrets at start
	Store     string      // object store that receives the artifacts
	Notify    *Webhook    // called once with the final status
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

| Method and path | Does |
| --- | --- |
| `POST /api/runs` | Queues a run |
| `GET /api/runs/:id` | Status, exit code, timings, artifact list |
| `GET /api/runs/:id/logs` | Streams output, live or finished |
| `GET /api/runs/:id/artifacts/:name` | Downloads one artifact |
| `DELETE /api/runs/:id` | Cancels a queued or running run |

Runs are persisted in SQLite beside the scheduler database, so a run's record survives an agent restart. An instance still alive after a restart is destroyed and its run marked failed. Artifacts go to a configured object store (`docs/OBJECT_STORES.md`), not to the agent's disk.

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

The `runtime` package with a Docker implementation. Only new code uses it; deployments keep their current path.

Done when: the Docker runtime creates, runs, copies out of and destroys an instance in tests, and `Destroy` leaves no container, volume or network behind.

### 2. Runs on Docker

Run storage, queue, timeout, cancellation, log streaming, artifact upload, final webhook. Permissions and the cluster capability.

Done when, tested through HTTP:

- A run that writes a file and exits reports `succeeded`, and the file downloads as an artifact.
- A run past its timeout is killed and reports `timed_out`.
- A cancelled run stops and reports `cancelled`.
- An actor without `runs:read` sees no runs, and an API key scoped narrower than its user cannot widen it.

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
