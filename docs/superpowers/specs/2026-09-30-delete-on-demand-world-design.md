# Deleting an on-demand world — Design

**Status:** approved in conversation 2026-09-30 (answer A: a delete of a
running member stops it first).

## Goal

A plugin can delete a member of an OnDemand group for good: its server, its
pod and its world. Today it can only start and stop one, and stopping leaves
the world on its claim on purpose, so a network that lets players delete
their own worlds has no way to do it and the claims pile up.

## Behaviour

`SpawneryApi.deleteServer(group, key)` — the same two arguments
`startServer` takes, because the member may not be running: a stopped world
is only a claim, and that is exactly what the caller wants gone.

1. The group is resolved in the caller's namespace and must be OnDemand:
   `NOT_FOUND` for no such group, `REFUSED` for any other type. The bound is
   the one `stopServer` keeps: no name reaches a lobby's or an ordinal's
   world.
2. The member's name is `instance.Name(group, key)`; a key no name can be
   built from is `REFUSED`, as for `startServer`.
3. If the member's Server exists, it is deleted as `stopServer` deletes it:
   players are moved through the proxies within the group's drain timeout,
   then the pod goes.
4. The member's data claim (`podspec.DataClaimName(name)`) is deleted if it
   exists and carries this operator's labels for this group
   (`managed-by`, `spawnery.cloud/group`). A claim without them is not
   touched and the answer says so (`REFUSED`): something else made it.
   Kubernetes' pvc-protection holds the claim until the pod no longer uses
   it, so the order of 3 and 4 needs no bookkeeping of ours and survives an
   operator restart.
5. Neither a Server nor a claim: `NOT_FOUND`. A second delete of a member
   that is still going succeeds; one after it is gone is `NOT_FOUND`, as for
   `stopServer`.

The call completes once both deletions are accepted, not once the world is
gone; the claim's own deletion is Kubernetes' and takes as long as the drain.

**Start during a delete.** `startServer` on a key whose claim is being
deleted answers `UNAVAILABLE` (as for a member that is still stopping): a
pod created now would reference a claim that is on its way out. Once the
claim is gone the same start creates a fresh, empty world.

**The controller must not bring the claim back.** A Server that carries a
deletion timestamp gets no claim created or grown for it; otherwise the
Server controller, still draining the member, would recreate the claim the
delete just removed.

## Narrowing the right (added after review, option A)

The guide had promised that this operator never deletes a claim, because
its delete right would be cluster-wide: RBAC selects by name, and these
names are minted at runtime. Kept in the operator, the right is narrowed in
the API server instead:

- On-demand world claims carry `spawnery.cloud/key`, set only when the claim
  is created.
- The chart ships `ValidatingAdmissionPolicy`/`Binding`
  `spawnery-world-deletion`: a DELETE of a claim by the operator's
  ServiceAccount is admitted only with `spawnery.cloud/managed-by`, a
  non-empty `spawnery.cloud/key`, and the name `<group>-<key>-data`; an
  UPDATE by it may not change any of those three labels. Without the second
  rule the operator's `patch` on claims would let it label any claim into
  reach (found in review), so a key is never added later.
- `DeleteServer` refuses a world without its key label; such a world (made
  before this change) is deleted by hand.
- `DeleteServer` reads the claim past the manager's cache, which holds only
  labelled claims, so a foreign claim is `REFUSED` rather than `NOT_FOUND`.
- The chart's `kubeVersion` floor becomes 1.30 (VAP v1 GA).

## Surface

- proto: `DeleteServerRequest { string group = 1; string key = 2; }` in
  `CloudRequest`, `DeleteServerResult { string server = 1; bool world = 2; }`
  (`world`: a claim was deleted) in `CloudResponse`.
- `agentserver.Writer.DeleteServer(ctx, namespace, group, key)`, answering
  `ErrNoSuchGroup`, `ErrGroupNotOnDemand`, `instance.ErrBadKey`,
  `ErrNoSuchServer` (nothing there), `ErrForeignClaim`.
- Operator RBAC: `delete` on `persistentvolumeclaims` (marker in
  `server_controller.go`'s neighbour, row in `internal/rbacaudit`).
- Agent API (`cloud.spawnery:spawnery-api`): `CompletionStage<Void>
  deleteServer(String group, String key)`, failure shape as `startServer`'s.
  Both agents (Paper and Velocity) implement it, since `startServer` is
  callable from both.
- Docs: `docs/guides/on-demand-servers.md` (deleting a world, and that it is
  final), `docs/plugin-api/what-a-plugin-can-do.md`, the proto comments.

## Testing

- `agentserver` envtest: delete of a running member (Server deleted, claim
  deleted, claim held by pvc-protection until the pod is gone), of a stopped
  one (claim only), of an unknown key (`NOT_FOUND`), of a key in a
  non-OnDemand group (`REFUSED`), of a claim without our labels (`REFUSED`,
  untouched); start while the claim is terminating (`UNAVAILABLE`).
- controller envtest: a deleting Server does not recreate its claim.
- Agent JUnit: request/response mapping of `deleteServer` on both agents.
- On a cluster: create, stop, delete a member; the claim and its volume are
  gone; a new start of the key begins with an empty world.

## Not in this design

- Deleting worlds in bulk, or by age.
- Any backup before deletion: the caller decides whether to ask first.
