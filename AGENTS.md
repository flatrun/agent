# FlatRun Agent Guide

## Authorization

Permissions and resource grants answer different questions. A permission allows an operation. A resource grant limits where that operation may run. Endpoints that operate on deployments or another owned resource must enforce both.

Rules:

- Define dedicated read and write permissions for each module. Do not reuse an unrelated permission because two features share a page, plugin, or transport.
- Enforce authorization in the HTTP API. UI guards are not security boundaries.
- Filter collection responses to resources the actor may read.
- Validate every resource referenced by create, update, delete, bulk, and action requests.
- Preserve records outside the actor's scope when processing bulk updates. A scoped request must never replace a global collection.
- Require explicit global access for host-wide, fleet-wide, and all-resource operations. An empty resource identifier must not grant global access.
- Apply the intersection of user and API key grants. An API key may narrow its user's access but must never widen it.
- Keep secret-bearing administration resources separate from safe selectors. A scoped feature may receive target identifiers and display names without receiving target credentials.
- Test authorization through HTTP with actors whose resource grants differ. Prove that each actor sees only allowed records and cannot change the other actor's records.

## Tests

Drive regression tests through the boundary used in production. HTTP features must create requests through their router and middleware instead of calling handlers' collaborators directly.
