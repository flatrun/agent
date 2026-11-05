# VM runtimes plan

Runs and the Docker runtime are described in `docs/RUNTIMES_AND_RUNS.md`. This
plan adds runtimes that isolate code in a virtual machine, so a run or a
service can execute code its operator does not trust. For example, a CI job
that builds a contributor's branch, or a customer's service that must not share
a kernel with its neighbours.

## Constraints

- A VM runtime implements the existing runtime interface. Runs choose it with
  the `runtime` field; nothing else in the run API changes.
- Firecracker needs `/dev/kvm`: bare metal, or a host that exposes nested
  virtualisation. A node only accepts a runtime whose `Available` check passes.
- A project stays a directory with a Compose file. A service chooses its
  runtime with the Compose `runtime` field, and the project keeps working under
  plain `docker compose` on a host where that runtime is installed.
- Existing deployments, their API and their on-disk layout do not change.

## Services on a VM runtime

There are two ways to give a Compose service a VM boundary. Phase 0 decides
between them.

- **A: VM backed container runtime.** Kata Containers, configured with
  Firecracker as its hypervisor, is registered as a Docker runtime on the node.
  A service sets `runtime: kata-fc`. The deployment code path is unchanged:
  FlatRun checks that the node has the runtime and places the service there.
- **B: FlatRun drives Firecracker itself.** The service lifecycle gains a
  second backend beside `docker compose`, built on the runtime interface.

A is preferred. It keeps the Compose file as the source of truth and keeps the
project portable.

## Phases

Each phase merges on its own and leaves existing deployments untouched.

### 0. Spike on one KVM host

Answer, with measurements:

- Cold boot time, and boot time from a snapshot.
- Memory overhead of an idle microVM.
- Whether a Docker daemon runs inside a microVM, for runs that start a Compose
  backend.
- Whether headless Chrome runs inside a microVM.
- Whether Kata with Firecracker, registered as a Docker runtime, runs an
  existing FlatRun project with one service switched by `runtime:`.

Output: the numbers, and a choice between A and B for services.

### 1. Firecracker runtime

A root filesystem built from an OCI image and cached, the jailer, tap
networking with a default deny policy, and an optional snapshot pool. Nodes
advertise the runtime when `Available` succeeds.

Done when:

- The run HTTP tests pass with `runtime: firecracker`.
- A run that starts headless Chrome and saves a screenshot succeeds.
- After every run, no VM process, tap device, socket or root filesystem copy
  remains.
- A run cannot reach the host, the node's other runs, or the network beyond its
  policy.

### 2. Services on a VM runtime

Path A or B, as Phase 0 decided. The UI offers a runtime per service, limited
to runtimes the target node has.

Done when: a Compose service on the VM runtime deploys, routes over HTTPS,
restarts on failure and rolls back like a container service.

### 3. Runs across the fleet

The cluster places runs on nodes that have the runtime, the capacity and a
`runs.execute` capability, and queues them when none has room.

Done when: runs submitted to one node execute on a peer that has capacity, and
their logs and artifacts are reachable from the submitting node.

## Open questions

- Root filesystem format: an ext4 image per OCI image, cached, or a devmapper
  thin pool.
- Whether runs also use Kata, if Phase 0 shows it meets the boot time and
  teardown needs, or always use FlatRun's own Firecracker runtime.
- Usage metering per run (CPU seconds, memory, wall time) for operators who
  bill for runs.
- Secrets by reference to the secrets store, alongside inline `secret_env`.
- Upload of artifacts to an object store, a final status webhook, and retention
  of finished runs.
