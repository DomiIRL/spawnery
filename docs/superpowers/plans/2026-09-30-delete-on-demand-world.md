# Deleting an On-Demand World — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A plugin can delete a member of an OnDemand group for good — server, pod and world claim — with `SpawneryApi.deleteServer(group, key)`.

**Architecture:** A new `DeleteServer` request on the agent channel. `KubeWriter.DeleteServer` resolves the group (OnDemand only), deletes the member's Server if present (the drain finalizer moves players first, as for a stop) and the member's labelled data claim; Kubernetes' pvc-protection holds the claim until the pod is gone. `StartServer` answers `UNAVAILABLE` while that claim is terminating. Both agents expose the call through the public API.

**Tech Stack:** Go (controller-runtime, envtest), protobuf (`make proto`), Kotlin agents + Java API (`make agent`, JUnit).

**Spec:** `docs/superpowers/specs/2026-09-30-delete-on-demand-world-design.md`

## Global Constraints

- Every command runs in the dev shell: `nix --extra-experimental-features 'nix-command flakes' develop -c <cmd>`; on an 8-core machine run envtest packages with `-p 1`.
- Generated files are committed: after proto changes `make proto`; after RBAC markers or API docs `make manifests generate`.
- A new `+kubebuilder:rbac` marker needs its row in `internal/rbacaudit/required.go`.
- `git add` new files before `make agent` (Nix reads the index).
- Only OnDemand members and only claims carrying `spawnery.cloud/managed-by` = this operator and `spawnery.cloud/group` = the group are ever deleted.
- Conventional Commits, signed, trailers `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_01BuEJveozFM4g3SgR8FgA46`. No consumer-specific names anywhere in this public repo.

## Review Focus

1. **A claim of a Persistent group whose ordinal name happens to equal `instance.Name(group, key)`** — must not be deleted: the group is resolved and must be OnDemand, and the claim's group label must match. Pinned in Task 2 (`TestDeleteRefusesAGroupThatIsNotOnDemand`, `TestDeleteLeavesAClaimItDidNotMake`).
2. **Start during a delete** — must not bind a new pod to a terminating claim. Pinned in Task 2 (`TestStartWhileTheWorldIsBeingDeletedIsUnavailable`).
3. **The Server controller recreating the claim while the deleted member drains.** Pinned in Task 3 (`TestADeletingServerDoesNotRecreateItsClaim`).
4. **Delete twice / after completion** — second is success while going, `NOT_FOUND` after. Pinned in Task 2.
5. **Operator RBAC** — `delete` on claims must exist or every delete fails at runtime only. Pinned in Task 3 (rbacaudit).

---

### Task 1: Protocol

**Files:** Modify `proto/spawnery/agent/v1alpha1/agent.proto`; regenerate `internal/agentpb/`, `agent/common/src/proto/java/`.

- [ ] **Step 1:** In `CloudRequest.request` add `DeleteServerRequest delete_server = 12;`, in `CloudResponse.result` add `DeleteServerResult delete_server = 13;`, and after `StopServerResult`:

```proto
// DeleteServerRequest deletes a member of an OnDemand group for good: its
// server, if one is running, and its world.
//
// Group and key rather than a server name, because a stopped member has no
// server any more and its world is what the caller wants gone.
//
// A running member is stopped as StopServerRequest stops one -- its players
// are moved through the proxies within the group's drain timeout -- and its
// claim is deleted at once; Kubernetes keeps the claim until the pod no
// longer uses it. The answer comes when both deletions are accepted.
//
// NOT_FOUND for a group this network does not have, and for a key with
// neither a server nor a world; REFUSED for a group that is not OnDemand, a
// key no name can be built from, and a claim this operator did not make.
message DeleteServerRequest {
  string group = 1;
  string key = 2;
}

// DeleteServerResult says the member is going.
message DeleteServerResult {
  // The member's name, as StartServerResult carries it.
  string server = 1;
  // True when a world claim was deleted; false when there was only a server.
  bool world = 2;
}
```

- [ ] **Step 2:** `make proto`; `go build ./...` and `nix … develop -c make agent` compile. Commit `feat(proto): DeleteServerRequest`.

### Task 2: The writer and the answer (operator)

**Files:** Modify `internal/agentserver/writer.go`, `internal/agentserver/requests.go`; Test `internal/agentserver/ondemand_envtest_test.go`.

**Produces:** `Writer.DeleteServer(ctx, namespace, group, key string) (DeletedServer, error)`; `type DeletedServer struct{ Name string; World bool }`; `var ErrForeignClaim`, `var ErrWorldDeleting`.

- [ ] **Step 1: Failing tests** (append; `deleteOverTheWire` beside `stopOverTheWire`):

```go
func deleteOverTheWire(t *testing.T, f *serverFixture, pod *corev1.Pod, group, key string) *agentpb.CloudResponse {
	t.Helper()
	return askOverTheWire(t, f, pod, &agentpb.CloudRequest{
		Request: &agentpb.CloudRequest_DeleteServer{
			DeleteServer: &agentpb.DeleteServerRequest{Group: group, Key: key},
		},
	})
}

// worldOf creates the member's data claim the way the Server controller does.
func worldOf(t *testing.T, f *serverFixture, group, name string) {
	t.Helper()
	var g spawneryv1alpha1.ServerGroup
	if err := f.c.Get(f.ctx, client.ObjectKey{Namespace: f.ns, Name: group}, &g); err != nil {
		t.Fatalf("get group: %v", err)
	}
	srv := &spawneryv1alpha1.Server{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns}}
	if err := f.c.Create(f.ctx, podspec.BuildDataClaim(&g, srv)); err != nil {
		t.Fatalf("create claim: %v", err)
	}
}

func claimGoing(t *testing.T, f *serverFixture, name string) bool {
	t.Helper()
	var c corev1.PersistentVolumeClaim
	err := f.c.Get(f.ctx, client.ObjectKey{Namespace: f.ns, Name: podspec.DataClaimName(name)}, &c)
	return apierrors.IsNotFound(err) || (err == nil && !c.DeletionTimestamp.IsZero())
}

func TestDeleteRemovesARunningMemberAndItsWorld(t *testing.T)   // start c0ffee, worldOf; delete → no error, Server going, claimGoing, result Server name + World true
func TestDeleteRemovesTheWorldOfAStoppedMember(t *testing.T)   // worldOf only (no Server); delete → World true, claimGoing
func TestDeleteOfNothingIsNotFound(t *testing.T)                // neither → NOT_FOUND
func TestDeleteRefusesAGroupThatIsNotOnDemand(t *testing.T)    // makeEphemeralGroup → REFUSED
func TestDeleteOnAGroupThisNetworkDoesNotHaveIsNotFound(t *testing.T)
func TestDeleteRefusesAKeyNoNameCanBeBuiltFrom(t *testing.T)   // key "" or "UPPER!" → REFUSED
func TestDeleteLeavesAClaimItDidNotMake(t *testing.T)          // claim named DataClaimName(member) without our labels → REFUSED, claim not going
func TestDeleteTwiceWhileGoingSucceeds(t *testing.T)           // holdWhileStopping; delete twice → both succeed
func TestStartWhileTheWorldIsBeingDeletedIsUnavailable(t *testing.T) // worldOf + a finalizer on the claim ("test/hold", removed in Cleanup) so it stays Terminating; delete; start → UNAVAILABLE
```

Write each body in the style of `TestStopDeletesTheMember` (`makeOnDemandGroup(t, f, "private-servers", 2)`, `pod := f.proxyPod("gateway-aaaa")`, assert `resp.GetError().GetReason()`).

- [ ] **Step 2:** `go test ./internal/agentserver/ -run 'Delete|WhileTheWorld' -count=1` → build failure (`CloudRequest_DeleteServer` unknown only if Task 1 skipped; else `DeleteServer` not on Writer / answers missing).

- [ ] **Step 3: Implement.** In `writer.go`, beside `ErrInstanceStopping`:

```go
// ErrForeignClaim is a delete whose claim this operator did not make.
var ErrForeignClaim = errors.New("that claim was not made by this operator for that group")

// ErrWorldDeleting is a start on a key whose world is still being deleted.
var ErrWorldDeleting = errors.New("that member's world is still being deleted")
```

`Writer` interface: add

```go
	// DeleteServer deletes a member of an OnDemand group for good: its server
	// if one exists, and its world claim. It returns ErrNoSuchGroup,
	// ErrGroupNotOnDemand, instance.ErrBadKey, ErrForeignClaim, and
	// ErrNoSuchServer when neither a server nor a world is there.
	DeleteServer(ctx context.Context, namespace, group, key string) (DeletedServer, error)
```

```go
// DeletedServer is what a delete removed.
type DeletedServer struct {
	Name  string
	World bool
}

func (w KubeWriter) DeleteServer(ctx context.Context, namespace, group, key string) (DeletedServer, error) {
	name, err := instance.Name(group, key)
	if err != nil {
		return DeletedServer{}, err
	}
	var g spawneryv1alpha1.ServerGroup
	if err := w.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: group}, &g); err != nil {
		if apierrors.IsNotFound(err) {
			return DeletedServer{}, ErrNoSuchGroup
		}
		return DeletedServer{}, err
	}
	if !g.IsOnDemand() {
		return DeletedServer{}, ErrGroupNotOnDemand
	}

	var claim corev1.PersistentVolumeClaim
	claimKey := client.ObjectKey{Namespace: namespace, Name: podspec.DataClaimName(name)}
	haveClaim := true
	if err := w.Client.Get(ctx, claimKey, &claim); err != nil {
		if !apierrors.IsNotFound(err) {
			return DeletedServer{}, err
		}
		haveClaim = false
	}
	if haveClaim && (claim.Labels[podspec.LabelManagedBy] != podspec.ManagedByValue ||
		claim.Labels[podspec.LabelGroup] != group) {
		return DeletedServer{}, ErrForeignClaim
	}

	var srv spawneryv1alpha1.Server
	haveServer := true
	if err := w.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &srv); err != nil {
		if !apierrors.IsNotFound(err) {
			return DeletedServer{}, err
		}
		haveServer = false
	}
	if haveServer && (srv.Spec.GroupRef.Name != group || srv.Spec.Key != key) {
		haveServer = false // somebody else's server under that name; not ours to delete
	}
	if !haveServer && !haveClaim {
		return DeletedServer{}, ErrNoSuchServer
	}
	if haveServer {
		if err := w.Client.Delete(ctx, &srv); err != nil && !apierrors.IsNotFound(err) {
			return DeletedServer{}, err
		}
	}
	if haveClaim {
		if err := w.Client.Delete(ctx, &claim); err != nil && !apierrors.IsNotFound(err) {
			return DeletedServer{}, err
		}
	}
	return DeletedServer{Name: name, World: haveClaim}, nil
}
```

In `StartServer`, after the `IsOnDemand` check:

```go
	var claim corev1.PersistentVolumeClaim
	err = w.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podspec.DataClaimName(name)}, &claim)
	if err == nil && !claim.DeletionTimestamp.IsZero() {
		return StartedServer{}, ErrWorldDeleting
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return StartedServer{}, err
	}
```

In `requests.go`: `case req.GetDeleteServer() != nil: return s.answerDeleteServer(...)` in `answerCloudRequest`; in `answerStartServer` add `case errors.Is(err, ErrWorldDeleting): return refuse(reqID, agentpb.RequestError_UNAVAILABLE, "that member's world is still being deleted; the same request starts a fresh one once it is gone")`; and:

```go
func deletedServer(reqID uint64, result *agentpb.DeleteServerResult) *agentpb.CloudResponse {
	return &agentpb.CloudResponse{Id: reqID, Result: &agentpb.CloudResponse_DeleteServer{DeleteServer: result}}
}

// answerDeleteServer deletes a member and its world. The OnDemand bound and
// the claim's labels are what keep it from reaching any other world.
func (s *Server) answerDeleteServer(ctx context.Context, logger logr.Logger, id grpcauth.Identity,
	reqID uint64, req *agentpb.DeleteServerRequest) *agentpb.CloudResponse {
	deleted, err := s.opts.Writer.DeleteServer(ctx, id.Namespace, req.GetGroup(), req.GetKey())
	switch {
	case errors.Is(err, ErrNoSuchGroup):
		return refuse(reqID, agentpb.RequestError_NOT_FOUND, "no group by that name is on this network")
	case errors.Is(err, ErrGroupNotOnDemand):
		return refuse(reqID, agentpb.RequestError_REFUSED, "that group is not on-demand, so it has no member to delete")
	case errors.Is(err, instance.ErrBadKey):
		return refuse(reqID, agentpb.RequestError_REFUSED, err.Error())
	case errors.Is(err, ErrForeignClaim):
		return refuse(reqID, agentpb.RequestError_REFUSED, "a claim of that name exists but this operator did not make it for that group")
	case errors.Is(err, ErrNoSuchServer):
		return refuse(reqID, agentpb.RequestError_NOT_FOUND, "that key has neither a server nor a world")
	case err != nil:
		logger.V(1).Info("could not delete an on-demand server", "reason", err.Error())
		return refuse(reqID, agentpb.RequestError_UNAVAILABLE, "the operator could not write that just now")
	}
	return deletedServer(reqID, &agentpb.DeleteServerResult{Server: deleted.Name, World: deleted.World})
}
```

Any fake `Writer` in tests (`grep -rn 'StopServer(ctx' internal --include=*_test.go`) gets a `DeleteServer` stub.

- [ ] **Step 4:** `go test ./internal/agentserver/ -count=1` green. Commit `feat(agentserver): delete an on-demand member and its world`.

### Task 3: RBAC and the claim guard (operator)

**Files:** Modify `internal/controller/server_controller.go` (marker only), `internal/rbacaudit/required.go`; Test `internal/controller/server_controller_test.go` (or an on-demand controller envtest file).

- [ ] **Step 1: Failing test** `TestADeletingServerDoesNotRecreateItsClaim`: with the fixture's group switched to a non-ephemeral type that has storage (reuse the persistent-group helper `createPersistentGroup` and a server of it), create server, reconcile (claim created), put the drain finalizer on the Server, delete the claim (strip finalizers so it is gone), delete the Server, reconcile twice; assert the claim does not exist. Run it; if it already passes, record that the guard exists (`createPod` requires no deletion timestamp) and keep the test as the pin.
- [ ] **Step 2:** Marker `// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;patch;delete` (extend the existing one), row `{Group: "", Resource: "persistentvolumeclaims", Verb: "delete", Why: "the agent writer deletes an on-demand member's world when a plugin asks for it; no other path deletes a claim"}`, and change the `patch` row's Why to drop "never delete". `make manifests generate`; diff `config/rbac/role.yaml` shows `delete`.
- [ ] **Step 3:** `go test -p 1 ./internal/rbacaudit/ ./internal/controller/ -count=1` green (note: `TestManagerReconcilesEndToEnd` may time out under `-race` on small machines, as on master). Commit `feat(rbac): the operator may delete a world claim`.

### Task 4: Agent API (Java + Kotlin)

**Files:** Modify `agent/api/src/main/java/cloud/spawnery/agent/api/SpawneryApi.java`, `agent/common/src/main/kotlin/cloud/spawnery/agent/CloudConnector.kt`, `MirrorApi.kt`; any other `SpawneryApi` implementation (`grep -rln 'override fun stopServer' agent`); Test `agent/common/src/test/kotlin/cloud/spawnery/agent/CloudConnectorTest.kt`.

- [ ] **Step 1: Failing tests** beside the `stopServer` ones: `deleteServer("private-servers","c0ffee")` sends `deleteServer.group/key`; a `DeleteServerResult` completes the future with null; an error response fails it with the reason.
- [ ] **Step 2:** API:

```java
    /**
     * Deletes one private server for good: stops it if it is running, as
     * {@link #stopServer} does, and deletes its world. There is no undo.
     *
     * <p>Group and key as {@link #startServer} takes them, because a stopped
     * server's world is all that is left of it. The stage completes when the
     * deletion is under way; a {@link #startServer} of the same key answers
     * {@code UNAVAILABLE} until the world is gone, and then starts an empty one.
     *
     * <p>It fails with {@code NOT_FOUND} for a group this network does not have
     * or a key with neither a server nor a world, and with {@code REFUSED} for a
     * group that is not on-demand, a key no name can be built from, or a world
     * this operator did not make. Timeout and renewed stream as for
     * {@link #startServer}.
     */
    CompletionStage<Void> deleteServer(String group, String key);
```

Kotlin: `fun deleteServer(group: String, key: String): CompletionStage<Void>` in `CloudConnector` built like `stopServer` with `setDeleteServer(DeleteServerRequest.newBuilder().setGroup(group).setKey(key))`; response branch `response.hasDeleteServer() -> requests.complete(response.id, null)`; `MirrorApi.deleteServer` delegates.
- [ ] **Step 3:** `git add -A agent && nix … develop -c make agent` green. Commit `feat(agent): deleteServer in the plugin API`.

### Task 5: Docs

- [ ] `docs/guides/on-demand-servers.md`: a section "Deleting a world" (what goes, that it is final, start-during-delete answer). `docs/plugin-api/what-a-plugin-can-do.md`: the new call beside start/stop. Commit `docs: deleting an on-demand world`.
- [ ] Full suite: `nix … develop -c env GOFLAGS=-p=1 make test` and `make agent`; push the branch, open a PR (not merged by the agent).
