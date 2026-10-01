# Changeover stages

**Status:** design, decided 2026-10-01
**Date:** 2026-10-01

## 1. What goes wrong today

A change that reaches every group at once (a new version of a plugin
every group carries, a new base image) makes every group of a network stale
in the same pass. `spec.update.maxConcurrentChangeovers` bounds how many of
them change over together, and admits the waiting ones **by name**. Nothing
lets a network say which group goes first.

Order matters to a network for two reasons. Mostly it is a sensible sequence:
the proxies first, then the lobby players land in, then the game modes. Partly
it is a dependency: a backend carrying a new shared plugin may expect the
proxy to carry it too. Name order serves neither; with groups named `arena`,
`edge` and `lobby`, the proxy rolls second.

A second problem sits in the budget itself. A group holds its place until its
last stale server or pod is gone. A proxy drain waits for its players
(`ProxyGroup.spec.update.maxStaleSeconds` is 0 by default), so one idle player
on an old proxy holds the place for as long as they stay connected, and with
`maxConcurrentChangeovers: 1` every other group of the network waits with
them. `WhenEmpty` server groups already avoid this through `Deferred`; proxy
groups and `RollingUpdate` server groups have no equivalent.

## 2. The shape

```yaml
kind: ProxyGroup
metadata:
  name: edge
spec:
  changeoverStage: -10
---
kind: ServerGroup
metadata:
  name: arena
spec:
  changeoverStage: 10
```

`changeoverStage` is an optional `int32` on `ServerGroup` and `ProxyGroup`,
default 0. **A lower stage changes over first.** Negative values are allowed,
so a group can be moved ahead of every group that sets nothing. Groups of
the same stage change over together, within the budget.

It sits at the top of the spec, not under `spec.update`, because a server
group's `spec.update` is refused for persistent groups, and persistent groups
take part in stages (§3.3). On an on-demand group it is refused (CEL): its
members never roll.

The field is not rendered into any pod, so it feeds neither
`DesiredServerHash` nor `DesiredProxyHash`; setting it rolls nothing. The hash
goldens stay unchanged.

Unset everywhere, every group is in stage 0 and admission is exactly
today's.

## 3. The rules

### 3.1 When a group's new generation stands: `Deferred`

`status.changeover` keeps its values. `Deferred` widens from "a `WhenEmpty`
group has a Ready current server" to **"the group's new generation stands,
and what is left of the old one waits only for its players"**:

- **`WhenEmpty` server group:** unchanged — a current server is Ready (and the
  group stays `Deferred` while any current server still counts).
- **`RollingUpdate` server group:** no creates are pending, every current
  server is Ready, and every stale server that still exists is leaving
  (retiring or draining). A stale server not yet asked to leave is work the
  roll still has to do.
- **Proxy group:** at least `spec.replicas` current pods are Ready, and every
  stale pod still present is draining (carries
  `spawnery.cloud/draining-since`) or terminating.

The value keeps its name rather than becoming `Settled`, so that observers
already reading `Deferred` keep reading it.

A `Deferred` group holds no budget place and blocks no later stage. That is
the point and also the cost: the old pods keep their memory while the next
group starts its extra server. On a cluster sized for exactly one extra
server, that server stays Pending until the drain ends. `WhenEmpty` has
accepted this since it was introduced.

### 3.2 Admission

`AdmitChangeovers` stays one pure function over every group of the network.
`ChangeoverView` gains two fields:

```go
type ChangeoverView struct {
    Kind    string
    Name    string
    State   spawneryv1alpha1.ChangeoverState
    Failing bool
    Stage   int32 // spec.changeoverStage
    Surges  bool  // false for persistent groups: they take no budget place
}
```

A group is **in flight** when its state is `Waiting` or `Begun` and it is not
failing. Then:

1. **Stage gate.** A `Waiting` group is admitted only if no group of a lower
   stage is in flight.
2. **Budget.** Of the groups the gate passes, `Begun` surging groups hold
   places; the remaining places go to waiting surging groups ordered by stage,
   then name, then kind. Non-surging groups pass the gate and are admitted
   without a place. Budget unset means no cap, and the gate still applies.
3. **Never paused halfway.** A `Begun` group stays admitted even if a group of
   a lower stage becomes stale after it began. A paused changeover would keep
   its extra server and only make the wait longer.
4. **Failing does not block.** A group whose `BackingOff` or `Degraded` is True
   neither holds a place nor gates a later stage. Where the order carries a
   dependency, this means a proxy that cannot start does not hold the network
   still; the dependency is a preference, not a guarantee.

### 3.3 Persistent groups

A persistent group rolls one ordinal at a time and never surges. It takes
part in stages and not in the budget:

- Its state is `Waiting` while it has a stale ordinal and none is down,
  `Begun` once one is down (`takedownInFlight`) or a current server stands
  beside stale ones, and empty when no stale ordinal is left. It has no
  `Deferred`: nothing of it waits for players without also being replaced.
- It publishes that state in `status.changeover`, which it does not today.
- `DecidePersistentSize` gets `ChangeoverRefused bool`. When set, the stale
  nomination is skipped; missing and surplus ordinals are handled as today.

On-demand groups are never in the list.

## 4. Where it is decided

As for the budget: each group decides for itself from the shared cache, once
per reconcile, when its own state is `Waiting`.

- `changeoverSiblings` reads `spec.changeoverStage` and the kind of each
  sibling (persistent → `Surges: false`) along with what it reads today, and
  lists persistent groups too.
- The early return on a budget of 0 in `proxyChangeover` and around the
  server group's call goes: with stages, an unset budget no longer means
  "admit everything".
- `ownServerChangeover` and `ownProxyChangeover` learn the wider `Deferred`
  of §3.1; `ownPersistentChangeover` is new.
- No new watch. Every group is reconciled at least every five seconds, which
  is also how often a changeover makes progress.

**The race.** Two reconcilers read the cache a moment apart, and a sibling's
`status.changeover` is one status write behind its reconcile. A later stage
can therefore begin just as an earlier one turns stale; by rule 3 it then runs
to the end. This is accepted, as for the budget: the failure stages prevent
is a whole network in the wrong order, not a race of one pass.

## 5. What an operator sees

- A group held by the gate reports `Progressing=True`, reason
  `WaitingForEarlierStage`, message `waiting for stage -10: edge`. A group
  the gate passes but the budget does not keeps
  `WaitingForChangeoverBudget`.
- `spawnery_network_changeovers_waiting` counts both.
- `docs/guides/updates-and-drain.md` explains stages with a made-up network
  (a proxy group, a lobby, two game modes), the widened `Deferred` and its
  memory cost, and that failing groups do not block. The CRD reference is
  regenerated.

## 6. Testing

- **`AdmitChangeovers`, table tests:** a later stage waits for an in-flight
  earlier one; the same stage runs together within the budget; `Deferred` and
  failing groups gate nothing; a persistent group gates but takes no place; a
  `Begun` group of a later stage is not displaced; stages apply with the
  budget unset; all stages 0 reproduces the existing table.
- **`ownProxyChangeover`:** a group whose current pods are all Ready and whose
  one stale pod is draining with a player on it is `Deferred` — the idle
  player case of §1. Fewer Ready current pods than `replicas` is `Begun`.
- **`ownServerChangeover`:** `RollingUpdate` with every stale server retiring
  and every current one Ready is `Deferred`; one stale server not yet retiring
  keeps it `Begun`.
- **`DecidePersistentSize`:** refused nominates no stale ordinal and still
  replaces a missing one.
- **Hash goldens** unchanged.
- **envtest:** a proxy group in stage -10 and a server group in stage 0, both
  stale. The server group reports `WaitingForEarlierStage` until the proxy
  group is `Deferred`, then begins while the old proxy pod is still
  draining. A persistent group in stage 10 nominates no stale ordinal until
  the server group is `Deferred`.
- **e2e (kind):** a proxy group, an ephemeral and a persistent group on three
  stages; change the network's image and assert the order from the creation
  times of the first current pod of each group. The test is shown to bite by
  reverting the stage gate in a throwaway worktree and recording the failure.

## 7. Not in this

- **Moving players off a draining proxy.** Minecraft's transfer packet
  (1.20.5+) lets a proxy send its players to the public address, where they
  land on a new proxy; it would make proxy drains short instead of only
  harmless. It touches the agents, the operator and the protocol, and gets
  its own spec.
- A timeout on the stage wait. `Deferred` releases a stage when its new
  generation stands; a timeout would also release one whose new generation
  never came up, and has no honest default.
- Stages for on-demand groups: their members never roll.
