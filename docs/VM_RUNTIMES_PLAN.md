# VM runtimes plan

Runs and the Docker runtime are described in `docs/RUNTIMES_AND_RUNS.md`. This
plan adds a runtime that isolates code in a virtual machine, so a run or a
service can execute code its operator does not trust. For example, a CI job
that builds a contributor's branch, or a customer's service that must not share
a kernel with its neighbours.

The VM runtime is Firecracker. Each instance is one microVM with its own guest
kernel. The agent drives Firecracker directly for runs. Services on a VM
runtime are decided after Phase 0.

## Constraints

- A VM runtime implements the existing `runtimes.Runtime` interface. Runs
  choose it with the `runtime` field. The run API, permissions, storage and
  lifecycle do not change.
- Firecracker needs `/dev/kvm`, the `firecracker` and `jailer` binaries, a guest
  kernel, and root on the node for the jailer and tap devices. `Available`
  checks each one and names what is missing.
- Docker stays the default runtime. A node without Firecracker keeps running
  Docker runs, and only Firecracker runs answer `503`.
- A project stays a directory with a Compose file. A service chooses its
  runtime with the Compose `runtime` field, and the project keeps working under
  plain `docker compose` on a host where that runtime is installed.
- Existing deployments, their API and their on-disk layout do not change.

## Runtime contract a VM must meet

The run manager drives every runtime the same way: `Create`, `Start`, then
`Logs` with follow running alongside `Wait`. After the command ends it drains
the logs, calls `Stop` if the run timed out or was cancelled, then `CopyOut`
for each requested path, then `Destroy`. A Firecracker instance has to support
each step:

| Step | What the manager does | What a microVM needs |
| --- | --- | --- |
| Create | Builds an instance from the spec | A root filesystem from the OCI image, a VM config, and a jailed process not yet booted. On failure it removes whatever it allocated, because no instance is returned |
| Start | Starts it | Boot the VM; a guest init runs the command with its env and working directory |
| Logs | Follows output from just after `Start` | The guest init buffers output from the first byte, streams stdout and stderr interleaved as plain text, and ends the stream when the command exits. `follow=false` returns what is buffered |
| Wait | Waits for the exit code | Returns the command's exit code, an error if the VM or its vsock link dies, and returns when its context is cancelled |
| Stop | Ends a run that timed out or was cancelled | Ends the command, not the VM: the guest init kills the process tree and stays up for `CopyOut`. Cancelling also unblocks a log reader |
| CopyOut | Copies requested paths as tar, after success, failure, timeout or cancel | The guest init tars a path on request |
| Destroy | Removes everything, safe to repeat | Kill the VM process, remove the tap device, socket, jail and filesystem copy. Works on an instance that never started or was only partly created |
| Leftovers, DestroyByID | Finds and removes instances after a restart | Instance state on disk under the agent's scope |

The guest init is a small static binary FlatRun builds and places in every root
filesystem. It talks to the host over vsock. It keeps the VM alive after the
command exits, because the manager copies artifacts after `Wait` returns, also
for runs that timed out or were cancelled.

Deadlines the runtime must fit, as the manager sets them today:

| Step | Budget |
| --- | --- |
| Create, boot and the command | The run's own timeout, image preparation included |
| Log drain after the command exits | 3 seconds, after which a successful run is marked failed |
| Stop | 5 seconds, with 1 second of grace |
| All artifacts together | 10 seconds |
| Destroy | 10 seconds |
| Leftover cleanup at startup | 10 seconds shared with the availability check |

Runtime selection:

- The manager holds one runtime per type. Docker keeps serving when another
  runtime is unavailable.
- An unknown `runtime` value answers `400`. A known runtime that is unavailable
  on this node answers `503` with the reason `Available` gave.
- A failure inside `Create` after the run was accepted fails that run, as it
  does for Docker today.

Recovery:

- Each instance's state directory, holding its ID and scope, is written before
  any VM process, tap device or file is created, so a crash at any point leaves
  something `Leftovers` can find.
- `Leftovers` and `DestroyByID` act only on instances of the agent's own scope,
  and handle state left by a partly created instance.

Firecracker's API socket path must fit in a Unix socket path, which is at most
108 bytes. Instance sockets live under a short runtime directory, not under the
deployments path.

Resources map as follows. `cpus` rounds up to whole vCPUs. `memory_bytes` sets
the VM memory, with a default when it is zero, because a VM needs a fixed size.
Without egress the VM has no network device. With egress it gets a tap device
with outbound NAT, and traffic between VMs and to the host is dropped.

## Phases

Each phase merges on its own and leaves existing deployments untouched.

### 0. Spike

Runs on the maintainer's workstation: Intel Core Ultra 9 185H, 22 threads,
30 GB of memory, Linux 7.0, Docker 29.3.1, with working `/dev/kvm`. Free disk is
34 GB, so guest images stay small and are deleted after the spike.

Inputs, all from the Firecracker project:

- Firecracker v1.17.0 for x86_64, checked against its published sha256.
- Guest kernel `vmlinux-6.1.155` and root filesystem `ubuntu-24.04.squashfs`
  from Firecracker's v1.15 CI artifacts, the newest set published.

Measured so far, 1 vCPU and 256 MiB unless stated:

| Measurement | Result |
| --- | --- |
| Cold boot until the guest runs its first program | 0.62 to 0.69 s of guest time, 10 runs |
| Cold boot, run `/bin/true`, shut down | 0.81 s wall time, 5 runs |
| Host memory of an idle VM | 48 MiB at 256 MiB configured, 61 MiB at 1024 MiB |
| Snapshot of a running VM | 256 ms to save; a 256 MiB memory file and 14 KB of state |
| Restore from that snapshot and resume | 23 to 41 ms, 3 runs |
| ext4 root filesystem from `busybox:latest` (4 MiB of files) | 0.17 to 0.21 s, 9 MiB on disk, 3 runs |
| ext4 root filesystem from `node:22-alpine` (159 MiB of files) | 0.99 to 1.19 s, 182 MiB on disk, 3 runs |
| Cold boot from the `busybox` filesystem, run `sh -c echo`, shut down | 0.71 s wall time |
| Cold boot from the `node` filesystem, run `node -e`, shut down | 0.81 s wall time |

Guest memory is only backed on the host once the guest touches it, so a large
configured size costs little while the VM is idle.

A root filesystem is built by exporting a container created from the image and
writing the files into an ext4 image with `mkfs.ext4 -d`. This needs no root and
no loop device. A program from the image can run as the guest's first process,
with no init system in the image.

Still to answer:

- Time and size of a root filesystem for an image with headless Chrome.
- Whether a vsock guest init can stream output, report the exit code and serve
  a tar copy of a path after the command exits.
- Whether headless Chrome runs inside a microVM and saves a screenshot.
- Whether a Docker daemon runs inside a microVM, for runs that start a Compose
  backend.
- Whether GitHub hosted Linux runners expose `/dev/kvm`, so CI can run the
  Firecracker tests.

Kata Containers is measured separately. Registering it as a Docker runtime
changes the Docker daemon configuration and restarts the daemon. Live restore
is off on the workstation, so a restart stops all 43 containers running there.
Kata runs on a disposable cloud VM with nested virtualisation instead.

Output: the numbers written into this document, the root filesystem format, and
a choice between path A and path B for services.

### 1. Firecracker runtime for runs

- The guest init, built from this repository and placed in each root
  filesystem.
- Root filesystem built from the OCI image and cached per image digest.
- The `jailer`, one jail per instance, under the agent's scope.
- Tap networking with outbound NAT and a default deny between instances.
- The run manager holds one runtime per type, with the selection and
  recovery rules above.
- `GET /api/runs/runtimes` lists the runtimes on the node and why any is
  unavailable. The New run form offers only those.
- Configuration under `runs.firecracker`: binary paths, kernel path, state
  directory and default memory.

Done when:

- The run HTTP tests pass with `runtime: firecracker`.
- A run that starts headless Chrome and saves a screenshot succeeds.
- After every run, no VM process, tap device, socket, jail or root filesystem
  copy remains.
- A run cannot reach the host, another run, or the network when egress is off.
- A node without Firecracker still serves Docker runs.

### 2. Services on a VM runtime

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

The UI offers a runtime per service, limited to runtimes the target node has.

Done when: a Compose service on the VM runtime deploys, routes over HTTPS,
restarts on failure and rolls back like a container service.

### 3. Runs across the fleet

The cluster places runs on nodes that have the runtime, the capacity and a
`runs.execute` capability, and queues them when none has room.

Done when: runs submitted to one node execute on a peer that has capacity, and
their logs and artifacts are reachable from the submitting node.

## Open questions

- Root filesystem format: an ext4 image per OCI image, or a devmapper thin
  pool. Phase 0 decides.
- Whether runs also use Kata, if Phase 0 shows it meets the boot time and
  teardown needs.
- Usage metering per run (CPU seconds, memory, wall time) for operators who
  bill for runs.
- Secrets by reference to the secrets store, alongside inline `secret_env`.
- Upload of artifacts to an object store, a final status webhook, and retention
  of finished runs.
