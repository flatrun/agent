# Events

Configure receivers and subscriptions in the agent YAML file, then restart the
agent. Each receiver has its own secret and stable public URL:
`POST /api/event-hooks/{name}`. Providers are `github`, `gitlab`, and `generic`.
Their signature and token checks are the same as deployment webhooks. Generic
deliveries may supply `type`, `subject`, string-valued `attributes`, and `labels`;
without a type they remain `generic.push` events.

```yaml
events:
  delivery_limit: 1000
  endpoints:
    - name: ci
      provider: github
      secret: your-webhook-secret-here
  github_app:
    app_id: 12345
    private_key_file: /etc/flatrun/github-app.pem
  subscriptions:
    - name: actions
      source: github/ci
      action: github_runner
      labels: [self-hosted, flatrun]
      github_runner:
        runner_group_id: 1
        image: ghcr.io/actions/actions-runner
        runtime: firecracker
        timeout_seconds: 600
```

Adapters produce the existing events model with `id`, `source`
(provider/endpoint), `type`, `occurred_at`, and extracted `attributes`.
GitHub types include
`github.push`, `github.pull_request.opened`, and `github.workflow_job.queued`.
Attributes include `branch`, `revision`, `pr`, `repository`, and, for jobs,
`job_id`, `installation_id`, `subject`, and `labels`. Event time uses the job creation time
when supplied, otherwise the receive time.

A subscription matches its source, type, every attribute filter, and every label
filter. Omitted filters match anything. Attribute values match exactly; labels
ignore case. Use `attributes: {branch: main}` to select a branch.

Actions:

- `deploy`: set `deployment` to a git deployment. Push events follow its existing
  branch and auto-deploy settings, including its environments.
- `preview`: set `deployment` to a git deployment. PR events create, update, or
  remove previews according to its existing preview setting.
- `run`: supply `run` with image, command, env, workdir, runtime, egress,
  timeout_seconds, cpus, memory_bytes, or artifact_paths. These values come only
  from config; string substitution is not supported. Event data is passed only
  through environment variables: `FLATRUN_EVENT_BRANCH`,
  `FLATRUN_EVENT_REVISION`, `FLATRUN_EVENT_JOB_ID`, `FLATRUN_EVENT_TYPE`, and
  `FLATRUN_EVENT_SUBJECT`. Quote these variables when using them in shell commands
  and never evaluate them as shell code.
- `github_runner`: set subscription `labels` (required) and supply a
  `github_runner` block with optional `runner_group_id`, `runtime`, `image`, `timeout_seconds`, `cpus`, and
  `memory_bytes`. It always matches `github.workflow_job.queued`; omit `type`.
- `notify`: publish the same event to the existing notification rules.

Existing deployment URLs, secrets, responses, and push/PR behavior are preserved
through built-in deploy and preview subscriptions. They also use normalized
events and share delivery logging and deduplication when a delivery ID is given.

## Actions runners

Install the GitHub App on the repository with Administration write permission
and subscribe its webhook to workflow jobs. Keep its private key readable only
by the agent. The delivery's installation ID selects the installation token.
The token is restricted to the delivery's repository.

Runner subscriptions require at least one label. Only queued jobs start runners,
and every configured label must be present.
The runner name is unique. JIT labels come from the subscription and the work folder is
`_work`. Installation tokens and JIT configuration are never written to records
or the delivery log. The run receives JIT configuration only as secret env
`JIT`; its command is `sh -c 'exec ./run.sh --jitconfig "$JIT"'` in
`/home/runner`, with `RUNNER_ALLOW_RUNASROOT=1` and egress enabled.
Completed and cancelled job events require no cleanup action: the runner exits
with the job.

Image defaults to `ghcr.io/actions/actions-runner`, timeout to 600 seconds
(maximum 3600), and runner group to 1. Set runtime to `firecracker` or `docker`.
Runtime defaults to `firecracker` at config load. An unavailable runtime fails
the delivery. Docker runs require an explicit `runtime: docker`.

Container jobs need a runner image with Docker Engine installed. For Firecracker,
use a derived image whose `run.sh` wrapper starts the Docker daemon, waits for
it to be ready, then executes the original runner script with its arguments.
The daemon and job containers stay inside the VM. The default runner action
does not install or start Docker. A Docker runtime run has no privileged mode
or host Docker socket, so this Docker-in-VM arrangement requires Firecracker.

## Delivery and inspection

Verified deliveries return 202 before actions execute. Processing is bounded to
128 concurrent deliveries. Repeated IDs from the same source return 200 without
another action while processing, succeeded, or ignored. A failed delivery may be
redelivered with the same ID; the new attempt replaces its record. Missing
delivery IDs use a payload hash. IDs and outcomes are
stored in the existing events SQLite database with per-subscription outcomes,
default 1000 entries, maximum 10000. Completed entries are evicted oldest first.
Deduplication history survives restarts until eviction. Failed deliveries retry
only subscriptions that did not succeed. Failures are not retried automatically.
Actions in progress at shutdown are not resumed automatically.

`GET /api/events/subscriptions` lists configured subscriptions.
`GET /api/events/deliveries` lists recent IDs, sources, types, times, and outcomes
(`processing`, `ignored`, `succeeded`, `failed`). Neither returns raw payloads or
receiver secrets. Both require `config:read` and global admin access, including
an unscoped API key when a key is used: this is agent-wide automation configured
in the agent file. The generic config API cannot edit the events section.
