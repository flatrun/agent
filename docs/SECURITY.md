# Security

Security reviews are report-only. They do not quarantine files, change firewall policy, rotate credentials or block deployments. Application authentication and authorization remain the application's responsibility.

## Profiles and approval

Each deployment has one platform baseline covering inventory, exposure, identity, privileges, integrity, vulnerabilities, credential metadata, data access, monitoring, writable execution surfaces, attachment safety, upload ingress and object authorization. Every concern appears even when collection is unavailable.

Template metadata can include `security_profile`. The agent preserves it through marketplace synchronization, the template cache, template listings and project creation. Template profiles are drafts. An empty collector image requires an administrator to supply an approved immutable image before collection is supported.

The project Security tab supports profile editing, agent proposals, source-template proposals, comparison and approval. A source-template proposal preserves local expectations, exceptions, scope, access policy, schedules and budgets. It does not replace the current approval.

Approvals bind the entire profile digest and deployment revision. Editing commands, images, roots, policy, schedules or budgets invalidates the previous approval. Changes to deployment configuration also require approval again. Container replacement alone does not identify a new approved scope.

Profile assistance has no tools and cannot approve or execute its output. Review proposed commands and scope before granting approval.

## State and collection

Configure a private state directory outside deployment storage:

```yaml
security_reviews:
  state_path: /var/lib/flatrun/security-reviews
  approved_images: []
```

The directory must have mode `0700` and cannot have symbolic-link parents. Review state, approvals, proposals, reports and encrypted evidence keys live there. Application profile files do not contain approval authority.

Collector images must already be available locally and be pinned by SHA-256 digest. The runner does not pull images. Collectors run as UID/GID 65532, with no network, a read-only root, dropped capabilities, no privilege escalation, the built-in seccomp profile, bounded scratch space, one CPU, 256 MiB RAM and 128 PIDs. Python starts with isolated imports. Go receives a locked toolchain and module environment. The working directory contains no application files.

File collection currently supports one explicitly approved retained bind root per target. Use separate targets for separate roots. Named volumes, container-layer snapshots, remote Docker daemons and unavailable metadata exporters remain unsupported. Compose input files outside the deployment directory require separate evidence support. Metadata-only collectors receive no application snapshot. Snapshots skip links, special files, hardlinks and files outside the approved budgets, recording the resulting gaps. Snapshot observations do not reconstruct historical filesystem state.

Collector sources are in `collectors/v1`. Build a collector image from an administrator-selected immutable Python base. Go binary collection additionally needs a trusted Go toolchain in that image. Do not install dependencies or run application entrypoints during collection.

Runtime inventory reads Composer, npm and Python distribution metadata. Go inventory reads binary build metadata and excludes build settings and linker flags from results. Lockfile declarations and path-derived package identities are distinguished from installed metadata. Unsupported package formats remain gaps. Source calls, handler overrides and attachment indicators require runtime and policy context; they are not proof of a vulnerability.

## Coverage and findings

Writable execution requires both effective production-writer permissions and an applicable handler or loader. A writable directory, an executable bit or a script-looking filename alone does not establish a vulnerability.

Attachment review distinguishes active-content indicators, archive hazards, content policy, processing limits and serving behavior. A valid file signature does not establish safe rendering. Unsupported, encrypted and oversized content remains incomplete.

Upload review needs the endpoint's intended access, naming, content, size, storage and processing rules. Configuration describes intended controls. Approved HTTP tests are needed to demonstrate enforcement.

Object authorization needs the approved action rule and historical subject, tenant, ownership, sharing and scoped-role facts. The access command pack distinguishes `allow`, `deny` and `unknown`. It emits an unauthorized-access candidate only when denial is joined with a demonstrated protected effect. HTTP 200 alone is insufficient. Application-reported facts remain subject to their source's trust limits.

Historical application audit exporters are not connected by default. Authorization, uploads, private delivery and production writer/loader relationships therefore remain unknown until their required evidence is connected. There is no mandatory application SDK.

Deterministic findings remain available without a model. Model assessment runs separately and receives bounded summaries and evidence references, with no tools. It cannot change deterministic findings or collection coverage. An unavailable model leaves its assessment unavailable.

## Scheduling and retention

Default profile targets are incremental every five minutes, retrospective hourly and full daily at 02:00 UTC. These are capacity targets, not guaranteed intervals. At most two collectors run per host, with one active review per deployment. Queued work remains visible in run history. Recovery coalesces older missed windows into bounded runs with explicitly unknown historical completeness.

Change manifests retain additions, changes and deletions across reconciliations. Manifest processing and historical-window coverage are separate. A current full snapshot cannot prove that no earlier activity was missed. Scheduling recovery and representative capacity measurement are operational release requirements.

Sanitized reports and evidence expire after ninety days unless an owner places the run on incident hold or an unresolved finding still references its evidence. Closing the finding allows normal expiry. Local retention does not constitute off-host delivery. Reports explicitly identify unconfigured or failed delivery.

An administrator can configure a separate HTTPS receiver:

```yaml
security_reviews:
  delivery:
    url: https://security-receiver.example/reports
    allowed_hosts: [security-receiver.example]
    credential_id: delivery-credential-reference
```

The referenced credential's password supplies the delivery bearer token. The token is never placed in a profile, report or collector input. Delivery rejects redirects, user information in URLs and URL query strings. Evidence is sent in bounded pages before the report. Receivers accept objects with `kind`, `deployment`, `run`, `id` and `data`, deduplicate the `Idempotency-Key` header, and enforce the configured retention and hold policy. Failed delivery is visible as a monitoring issue and retried independently of collection.

## API and CLI

Deployment operations live under `/api/deployments/:name/security-profile` and `/api/deployments/:name/security-reviews`. The generated CLI exposes the same operations under `projects`. Use its command help to inspect each request body.

Permissions are `security_profiles:read`, `security_profiles:write`, `security_profiles:approve`, `security_reviews:read`, `security_reviews:run` and `security_evidence:read`. Every endpoint also enforces deployment grants. New security permissions and resource grants use the intersection of the user and API key. Evidence access requires both review-read and evidence-read. Approval and incident holds require owner access. Stale approval requests return 409.

Versioned wire schemas are in `schemas/security/v1`; regenerate them with `go run ./tools/securityschemas`. The wire schemas are authoritative. Profiles containing unsupported fields remain unsupported drafts.

## Management and firewall

Version 2 configurations require authenticated management. The default listener is loopback. Public management requires explicit `api.public_management: true`. An optional `api.private_host` adds an authenticated listener on a private interface, allowing a local proxy to reach the agent without exposing management publicly. Containers sharing that interface can reach the authenticated listener. Private addressing does not establish workload trust.

New installer configurations bind the dashboard to loopback and preserve existing listener files on upgrades. Remote administration uses an SSH tunnel, VPN or configured private interface. Existing installations are not silently rebound. `GET /api/security/management-preview` shows the proposed migration without applying it.

Request capture includes successful requests to suspicious paths. Automatic IP blocking requires an authenticated proxy export, enabled automatic blocking and verified visitor attribution. A shared trusted proxy is not treated as an identified visitor.

Firewall configuration is a separate action. Default-deny rendering does not add public SSH access. Add an explicit trusted-source SSH rule before applying that policy. Established connections remain accepted. FlatRun owns only its own nftables table, including Docker bridge forwarding rules for IPv4 and IPv6. Rollback never flushes the host ruleset. A saved policy that was not enforced is unhealthy. Host networking and nonstandard container interfaces require separate coverage.
