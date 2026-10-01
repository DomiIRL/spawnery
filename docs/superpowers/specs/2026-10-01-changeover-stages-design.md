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

For proxies it is worse than one wait: they drain strictly one at a time,
fewest players first, and nothing else in the group moves while one drains.
An idle player on the first proxy to drain holds every other old proxy in
service, taking new players on the old generation, for as long as they stay.

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
  `spawnery.cloud/draining-since`) or terminating. With the blue/green roll
  of §3.4 this is reached as soon as the new pods are Ready, whoever is still
  on the old ones.

Like `WhenEmpty` today, both new cases hold `Deferred` once reached: a group
that was `Deferred` stays so while every remaining stale member is leaving and
a current one still exists, Ready or not. A readiness blip must not take a
budget place nobody admitted the group to.

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
    Stage      int32 // spec.changeoverStage
    Persistent bool  // gated by stage, takes no budget place
}
```

A group is **in flight** when its state is `Waiting` or `Begun` and it is not
failing. Then:

1. **Stage gate.** A `Waiting` group is admitted only if no group of a lower
   stage is in flight.
2. **Budget.** Of the groups the gate passes, `Begun` non-persistent groups
   hold places; the remaining places go to waiting non-persistent groups
   ordered by stage, then name, then kind. Persistent groups the gate passes
   are admitted without a place. Budget unset means no cap, and the gate still applies.
3. **Never paused halfway.** A `Begun` group stays admitted even if a group of
   a lower stage becomes stale after it began. A paused changeover would keep
   its extra server and only make the wait longer.
4. **Failing does not block.** A group whose `BackingOff` or `Degraded` is True
   neither holds a place nor gates a later stage. Where the order carries a
   dependency, this means a proxy that cannot start does not hold the network
   still; the dependency is a preference, not a guarantee. A group at its
   `maxReplicas` ceiling whose cold start is refused gates nothing either.

### 3.3 Persistent groups

A persistent group rolls one ordinal at a time and never surges. It takes
part in stages and not in the budget:

- Its state is `Waiting` while it has a stale ordinal and none is down,
  `Begun` once a stale ordinal below `replicas` is down (a surplus ordinal or
  a current one leaving does not count) and for as long as stale ordinals
  remain, and empty when no stale ordinal is left. It has no
  `Deferred`: nothing of it waits for players without also being replaced.
- It publishes that state in `status.changeover`, which it does not today.
- `DecidePersistentSize` gets `ChangeoverRefused bool`. When set, the stale
  nomination is skipped; missing and surplus ordinals are handled as today.

On-demand groups are never in the list.

### 3.4 Proxy groups roll blue/green

`DecideRollout` stops replacing stale proxies one at a time. While the group
may surge:

1. **Target** is `replicas` plus one pod for every stale pod, draining or not.
   Every stale pod gets its replacement up front, and a replacement that dies
   mid-drain is rebuilt, as the surge of one is today.
2. **Once at least `replicas` current pods are Ready**, every stale pod not yet
   draining is marked in the same pass. Each drains on its own deadline
   (`maxStaleSeconds`, `drain.timeoutSeconds`) as today.
3. Before that, a stale pod that serves nobody (not Ready, not draining) is
   still marked at once, as today, so a crashlooping proxy cannot hold its
   own replacement back.

Without a place (budget or stage), the group does not surge: target is
`replicas`, and only a stale pod that serves nobody is replaced, in place —
today's waiting behaviour.

"Stale" keeps its meaning in `DecideRollout`: a pod of an old hash, a pod on a
node that is leaving, or a pod an admin asked to retire. All three go
blue/green. Surplus without anything stale (a lowered `replicas`) is handled as today:
no new pods are involved.

**The cost** is memory: a proxy changeover now runs up to `replicas` extra
pods instead of one, until the new ones are Ready and the old ones have
drained. A budget place still counts groups, so a proxy group's place is
worth `replicas` pods. The CRD reference and the guide say so.

## 4. Where it is decided

As for the budget: each group decides for itself from the shared cache, once
per reconcile, when its own state is `Waiting`.

- `changeoverSiblings` reads `spec.changeoverStage` and the kind of each
  sibling (`Persistent` from `spec.type`) along with what it reads today, and
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
  memory cost, and that failing groups do not block. Its section "Proxies
  wait for their players too" is rewritten for the blue/green roll: the
  sentence that a group of N proxies waits N times goes, and the cost of
  `replicas` extra pods comes in. The CRD reference is regenerated.

## 6. Testing

- **`AdmitChangeovers`, table tests:** a later stage waits for an in-flight
  earlier one; the same stage runs together within the budget; `Deferred` and
  failing groups gate nothing; a persistent group gates but takes no place; a
  `Begun` group of a later stage is not displaced; stages apply with the
  budget unset; all stages 0 reproduces the existing table.
- **`ownProxyChangeover`:** a group whose current pods are all Ready and whose
  stale pods are all draining, one of them with a player on it, is
  `Deferred` — the idle player case of §1. Fewer Ready current pods than
  `replicas` is `Begun`.
- **`DecideRollout`:** three stale pods and `replicas: 3` create three; with
  three current Ready, all three stale are marked in one pass; with two
  current Ready, none is (except one not serving); a dying replacement during
  the drain is rebuilt; without surge, nothing is created and only a
  non-serving stale pod is marked; a lowered `replicas` with nothing stale
  drains its surplus as today. The existing cases that encode one-at-a-time
  replacement of stale pods change on purpose; each changed case is named in
  the commit.
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
- **e2e (kind, real images):** the main suite cannot carry this — its images
  never resolve, so nothing becomes Ready and nothing reaches `Deferred`. The
  test goes into the tutorial suite (`hack/e2e-tutorial.sh`, real Purpur and
  Velocity, run nightly): put the tutorial's proxy group in stage -10 and its
  server group in stage 0, change both in one apply (an environment variable
  on each), and assert that the server group's first current server is
  created only after every current proxy pod is Ready and every old one is
  draining. Run once by hand on `paul-desktop` before the PR, and shown to
  bite by removing the stage gate in a throwaway worktree and recording the
  failure. Persistent groups are covered by envtest only.

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
