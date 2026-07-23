# Agents

An agent is a flat markdown file the runtime can execute. The file is the whole
definition: frontmatter for metadata and policy, a body of instructions. The
runtime is the layer underneath, shared with the assistant and the MCP server:
the tool loop, permission gates, protected mode, secret redaction, and per-call
approval before any state change.

This document describes the architecture: what ships today and the design the
next slices build toward, so complex, long-running workflows compose from the
same small parts.

## The file

```markdown
---
description: Fix log errors and shepherd the fix through review
scope: deployment
deployment: my-api
---
Read the recent logs. If a recurring error has a clear fix, produce it,
open a pull request, and follow the reviews. Merge only when CI is green
and one approval exists. If the change touches auth or payment code,
never merge; wait for the operator.
```

Files live in `<deployments>/.flatrun/agents/*.md`, named by basename. No
registration: drop a file in, it lists; delete it, it is gone. A bare markdown
file with no frontmatter is a system-scoped agent.

## Creating agents

Three equivalent paths, all producing the same file:

1. **Ask the assistant.** Describe the agent you want in chat; the assistant
   drafts the file and writes it with the `write_agent_file` tool, which is
   state-changing and therefore waits for your approval.
2. **The Agents view.** Write or edit the file in the panel.
3. **The filesystem.** Any editor, `git`, CI, or another agent. It is a plain
   file.

Because `write_agent_file` is just another tool, an agent can create or refine
other agents, including its own definition, each change gated by approval like
any other write. That is the loop that lets a system improve itself without
ever changing anything silently.

## A run is a session

Running an agent seeds an AI session with its instructions and advances the
shared tool loop. Every session guarantee applies unchanged, and each run
records the agent it belongs to, so an agent's run history is its sessions.

Statuses today: `ready` (turn finished), `awaiting_approval` (a state-changing
tool is waiting for a per-call decision). Planned: `waiting` (the run parked
itself for an event or schedule) and `completed`, yielded by two built-in
control tools, `wait_for_event(reason)` and `finish(summary)`, so a run can
span days without holding anything open.

## Policy is deterministic; instructions are advisory

The split that makes guarantees real. Soft conditions ("merge when CI is green
and one approval exists") live in the instructions; the model applies them.
Hard floors live in frontmatter policy, enforced by the runtime before any tool
runs:

```yaml
policy:
  auto_approve: [github__create_pull_request]   # runs without asking
  require_approval: [github__merge_pull_request] # always asks, regardless
  deny: [control_deployment]                     # not even registered
```

The default is unchanged: any state-changing tool pauses for per-call operator
approval. Policy can only widen approval upward (`auto_approve`) for named
tools, or harden it (`require_approval`, `deny`). A `dry_run` run auto-declines
every mutation and reports what would have happened.

## External tools over MCP

Planned frontmatter:

```yaml
mcp_servers:
  - name: github
    url: https://api.githubcopilot.com/mcp/
    credential: github-mcp   # credential manager ID, never a secret inline
```

Each server's tools are namespaced (`github__merge_pull_request`) and flow
through the same policy gate. Anything that speaks MCP, a coding agent, an
issue tracker, a deployment target, becomes available without per-integration
code. FlatRun already serves its own tools over MCP; this is the client side.

## Triggers and the wake cycle

Planned: `triggers` frontmatter for schedules (via the existing scheduler) and
webhooks (`POST /api/ai/agents/:name/events`, per-agent secret). A trigger
appends the event payload to the session as a new turn, redacted, and advances
the loop again with the full history as memory. Manual runs stay as they are.

## Budgets

`max_steps` caps tool rounds per wake (default 8). Planned: max wakes and a
deadline per run. The session message window is already capped. A loop cannot
run away.

## Delivery slices

1. Frontmatter policy (`auto_approve` / `require_approval` / `deny`),
   `max_steps`, `dry_run` runs.
2. Run lifecycle: `wait_for_event` / `finish`, `waiting` / `completed`
   statuses, the events endpoint.
3. MCP client and `mcp_servers`.
4. Triggers: scheduler integration and webhook secrets; run-history UI.
