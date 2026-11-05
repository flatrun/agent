# Runtimes and runs

## What they are

A **runtime** creates, starts and destroys an isolated instance. Runs pick one
with the `runtime` field. Docker is one runtime, and it is the only one
available today: each instance is one container. Other runtimes, such as a
Firecracker microVM, plug in behind the same interface. Docker shares the host
kernel, so it is not an isolation boundary for untrusted code. Use it only for
trusted code. `docs/VM_RUNTIMES_PLAN.md` describes the planned VM runtimes.

A **run** executes work once, records its result and collects its output. For
example, a run can build a project, run a browser test or generate a report.
It has a timeout and can save files as artifacts before its container is removed.

Three layers describe the work:

| Layer | Question | Current behaviour |
| --- | --- | --- |
| Runtime | What executes the code? | Docker container |
| Lifecycle | How long does the work live? | A run executes once and records its outcome |
| Placement | Where does it execute? | Runs execute on the receiving agent; deployment placement is unchanged |

Runs use the runtime interface. Existing Compose deployments keep their own
execution path.

## Creating a run

Send a JSON object to `POST /api/runs`. The fields are at the top level, not
inside `spec`.

| Field | Meaning and default |
| --- | --- |
| `runtime` | `docker` is the only value accepted today. Omitted or empty means `docker`. |
| `image` | Required, non-empty image reference. A missing local image is pulled. |
| `command` | Array of command arguments passed as the container command. |
| `env` | Map of environment variable names to values. Stored in the run record. |
| `secret_env` | Map of environment variable names to secret values. Overrides matching names in `env` when the instance is created. |
| `workdir` | Working directory inside the container. |
| `cpus` | CPU limit, for example `1.5`. Zero or omitted means no CPU limit. Negative values are rejected. |
| `memory_bytes` | Memory limit in bytes. Zero or omitted means no memory limit. |
| `egress` | Whether the container has network access. Defaults to `false`. |
| `timeout_seconds` | Execution timeout in whole seconds. Zero or omitted means 600. Allowed values are 1 to 3600. |
| `artifact_paths` | Up to 16 absolute paths inside the container. Each must have a unique base name. Root, dot base names and paths containing a NUL byte are rejected. |

The request body is limited to 1 MiB. Invalid JSON or an invalid spec returns
`400`. A queued run returns `202` with its run record. A full queue or a closed
run manager returns `503`.

For example, this request writes a report and saves it as an artifact:

```json
{
  "runtime": "docker",
  "image": "alpine:3.21",
  "command": ["sh", "-c", "mkdir -p /out; echo complete > /out/report.txt"],
  "egress": false,
  "timeout_seconds": 60,
  "artifact_paths": ["/out/report.txt"]
}
```

## API

| Method and path | Permission | Behaviour |
| --- | --- | --- |
| `POST /api/runs` | `runs:write` | Queue a run. |
| `GET /api/runs` | `runs:read` | List visible runs, newest first, in an `items` array. |
| `GET /api/runs/:id` | `runs:read` | Read a run record. |
| `GET /api/runs/:id/logs` | `runs:read` | Read output as plain text. `follow=true` streams until the run finishes or the client disconnects. |
| `GET /api/runs/:id/artifacts/:name` | `runs:read` | Download a collected artifact as a tar archive. |
| `DELETE /api/runs/:id` | `runs:write` | Request cancellation and return the current record. Does not delete stored data. |

The list accepts `limit`, which defaults to 100. Values above 500 are capped at
500. Non-integer values and values below 1 return `400`.

A record contains `id`, `owner_user_id`, `owner_username`, `runtime`, `spec`,
`status`, `exit_code`, `artifacts` and `created_at`. It also contains `error` when
non-empty, `started_at` when the instance has started and `finished_at` when the
run has finished. `exit_code` is null until an exit code is recorded. Each
artifact has a `name` and a `size` in bytes.

## Permissions and visibility

Admins and operators have `runs:read` and `runs:write` by default. Viewers have
`runs:read`. Permissions control the operation; ownership controls which runs
are visible.

A run belongs to the submitting user. An actor with the effective `admin` role
can see all runs unless its API key has deployment restrictions. All other
actors see only runs owned by their user ID. An API key's non-empty role becomes
the effective role; otherwise the user's role applies.

These rules apply to listing, reading, logs, artifacts and cancellation. A run
outside the actor's view returns `404`, as does a missing run. A deployment
restriction on an admin API key removes access to other users' runs; it does not
associate runs with those deployments.

## Lifecycle

| Status | Meaning |
| --- | --- |
| `queued` | Waiting for a worker. |
| `starting` | Creating and starting the instance. |
| `running` | The instance has started. |
| `succeeded` | The process exited with code zero and log collection and cleanup succeeded. Artifact collection can still report an error. |
| `failed` | A non-zero exit, execution error, log collection failure, cleanup failure or restart recovery. |
| `timed_out` | The execution timeout expired. Cleanup failure can change this to `failed`. |
| `cancelled` | Cancellation was processed. Cleanup failure can change this to `failed`. |

The timeout starts when a worker takes the run, before image setup. Time spent
in the queue does not count. Log collection, artifact collection and container
cleanup use separate deadlines, so finalization can outlast the execution timeout.

Cancellation stops active work. A cancelled queued run does not start.
Cancellation of a finished run leaves it unchanged. Cancellation of active work
can finish after the DELETE response, so read the record again for its final
status.

After execution, the agent collects logs and requested artifacts, then attempts
to remove the container. Runtime errors are recorded in `error`. Artifact
collection errors do not change the execution status. For example, a run can
remain `succeeded` when a requested file is missing. Log collection or container
cleanup failure can change a successful run to `failed`.

On startup, stored `queued`, `starting` and `running` records become `failed`
with `error: "agent restarted"`. The agent then removes leftover containers in
its runtime scope. It does not resume those runs.

## Network and container settings

With `egress: false`, the container uses Docker's `none` network. With
`egress: true`, it joins the shared bridge `flatrun-runs-egress`. When creating
that bridge, the agent disables inter-container communication. An existing
bridge with that name is reused without changing its settings.

Containers have no host mounts, use `no-new-privileges` and have a process limit
of 1024. Inputs come from the image, command and environment.

Each container has `dev.flatrun.runtime.instance` and
`dev.flatrun.runtime.scope` labels. The scope is derived from the absolute
deployments path. Leftover discovery and removal filter by that scope, so agents
with different deployments paths do not clean up each other's containers.

## Secrets and audit

`secret_env` is kept in memory for instance creation. It is not included in the
run record or run database. Values supplied in `env` are stored, even if the
same name also appears in `secret_env`.

Exact non-empty secret values are replaced with `[REDACTED]` in collected output
and runtime error messages. Artifacts are copied without redaction. Do not write
secrets into files requested as artifacts.

Run routes use audit capture without request bodies. Events depend on the
audit configuration and successful audit initialization.

## Storage and configuration

Run records are stored in SQLite. Logs and artifacts stay on the receiving node:

```text
<deployments_path>/.flatrun/runs.db
<deployments_path>/.flatrun/runs/<id>/output.log
<deployments_path>/.flatrun/runs/<id>/artifacts/<base-name>.tar
```

Each collected path becomes a tar archive named from its base name. For example,
`/out/report.txt` becomes `report.txt.tar`. Finished run records, logs and
artifacts are not cleaned up automatically.

Configure runs in the agent config:

```yaml
runs:
  workers: 2
  egress_subnet: ""
```

`runs.workers` sets the number of concurrent workers. Values at or below zero
use the default of 2. The waiting queue holds 128 runs.

`runs.egress_subnet` supplies a CIDR when creating the egress bridge. Empty lets
Docker choose the subnet. It does not change an existing bridge.

If the Docker runtime or run storage cannot initialize, run endpoints return
`503` after authorization. The server continues to initialize its other routes.
