# Playable slots: capacity that is not the hard limit

**Status:** design, decided 2026-09-29
**Date:** 2026-09-29

## 1. What goes wrong today

A group has one number for the size of its servers, `maxPlayers`. The image
enforces it as `max-players`, and the operator measures every server's free
capacity against it: `slots − players`, where `slots` is what the agent reads
from `Bukkit.getMaxPlayers()` and the operator clamps to `maxPlayers`.

A round-based game wants two different numbers there. Its round takes, say,
twelve players; the server itself should admit more than twelve, so that
spectators, and staff joining to watch, still get in after the round is full.
A network sets `maxPlayers: 100` for the headroom and lets the game plugin stop
the round at twelve.

The operator then counts a full lobby as 88 free seats. Closing the door
(`acceptJoins(false)`) stops the count, but a game closes it when the round
starts, not when the lobby fills, so for the whole countdown of a full lobby
the group believes it has capacity it cannot sell:

- `spareSlots` never orders the next server while lobbies fill up; the next
  one is ordered when a round starts and is joinable a minute or more later.
- `status.freeSlots` and `/cloud` report the headroom as free seats.
- `connect` to a group picks the server with the most `slots − players`; with
  no emptier server in the group, that is a lobby whose round is already full.

Setting `maxPlayers: 12` fixes the count and takes away the headroom.

## 2. The shape

A second number, **playable slots**: the seats of a server that count as
capacity. `maxPlayers` stays the hard limit the image enforces. Playable slots
come from two places:

- **`spec.playableSlots`** on a ServerGroup, the default for all its servers;
- **the plugin at runtime**, `Spawnery.api().playableSlots(n)`, for the server
  it runs on — a game that knows its round size, or changes it between rounds,
  says so itself.

Without either, a server's playable slots are its slots, and everything behaves
exactly as today.

## 3. API

```yaml
apiVersion: spawnery.cloud/v1alpha1
kind: ServerGroup
metadata:
  name: duels
spec:
  type: Ephemeral
  maxPlayers: 100        # enforced max-players: room for spectators
  playableSlots: 12      # what counts as capacity
  scaling:
    minReplicas: 1
    maxReplicas: 8
    spareSlots: 12       # measured in playable seats
```

- `ServerGroupSpec.PlayableSlots *int32`, optional, `json:"playableSlots,omitempty"`.
- CEL on the spec: `!has(self.playableSlots) || (self.playableSlots >= 1 &&
  self.playableSlots <= self.maxPlayers)`.
- Allowed for every group type. For `Persistent` and `OnDemand` it changes only
  `freeSlots`, `connect` and what `/cloud` shows, because neither is sized by
  seats.
- Mutable. It feeds no pod, so it is **not** part of `DesiredServerHash`, and
  editing it rolls nothing.
- `Server.status.playableSlots int32`, mirrored beside `players` and `slots`
  and throttled with them.

## 4. Protocol

- `PlayerCount.playable_slots = 5` (int32): the value the plugin set, 0 if it
  set none. It rides on the periodic report, so a new stream restates it
  without anything having to remember to, and an agent older than the field
  sends 0.
- `ServerState.playable_slots` and `InstanceStatus.playable_slots`: the
  effective value (section 5.1), for plugins and for `/cloud`.

`slots` keeps its meaning. It cannot carry the playable number: the registry
discards a report whose players exceed its slots, and a server with spectators
beyond its playable seats would lose every report.

## 5. The operator

### 5.1 One server

The effective playable slots of a server with a report are the first of

1. the reported `playable_slots`, if above 0,
2. `spec.playableSlots`, if set,
3. its `slots`,

clamped to `[1, slots]`. A plugin value above the hard limit is therefore the
hard limit, not an error.

Its free seats are `max(0, playable − players)`. Players beyond the playable
seats make a server full, never negative. `clampReport` is unchanged: players
are still clamped to `slots`, which is `maxPlayers`.

`ServerView` gains `Playable`; every site that computes free seats reads it
instead of `Slots`:

- `provisionalCapacity` (the scaler's capacity, arrived and on order);
- `readyContribution` (what a removal is judged against);
- `AggregateGroup` (`status.freeSlots`, `GroupState.free_slots`);
- the member choice of `connect` to a group in `internal/agentserver`.

### 5.2 A server without a report

Where no server has spoken yet — a create that is pending, a server whose
`Slots == 0` is credited in full, and the unit `decideSize` divides the gap by
— the group's capacity per server is `spec.playableSlots` if set, else
`maxPlayers`. A runtime value is not known for a server that has not started.

## 6. Plugin API

```java
/**
 * The seats of this server that count as capacity, from now until changed.
 * 0 hands the decision back to the group's spec.playableSlots.
 */
void playableSlots(int slots);
```

- On `SpawneryApi`. It sets local agent state that the next periodic report
  carries; it asks the operator nothing, so it returns nothing and cannot fail
  on the network.
- A negative value throws `IllegalArgumentException`. On a proxy it throws
  `IllegalStateException`: a proxy has no seats a group is sized by.
- `ServerInfo.playableSlots()` and `InstanceStatus.playableSlots()` read the
  effective value. Both are records; the existing constructors stay as
  overloads that pass the slots through as playable, so code building them
  still compiles.

## 7. `/cloud`

- Per server, in `/cloud status` and `/cloud list`: where playable and slots
  are equal, unchanged — `9 / 100`. Where they differ, `9 / 12 · max 100`.
  The fill bar measures against the playable seats and stops at 100 %, so
  `14 / 12 · max 100` shows two spectators without a bar past its end.
- Per group: built from `free_slots`, which now counts playable seats. The
  format stays; the numbers change.

## 8. Versions

A new CRD field and a new Java API method: a **minor** step. The operator, the
image and agent version, and the chart all move; `make manifests generate proto`
runs and its output is committed.

## 9. Testing

Every test below is seen red before the change that turns it green.

- `api/v1alpha1` (envtest): `playableSlots` 0 and `maxPlayers + 1` are refused;
  equal to `maxPlayers` and absent are accepted.
- `internal/controller`, pure:
  - the resolution of 5.1, each source winning once, and the clamp at both ends;
  - `max(0, …)` with players beyond the playable seats;
  - each site of 5.1 and 5.2 with one case;
  - **the case this exists for:** `maxPlayers 100`, `playableSlots 12`, one
    Ready server with 12 players and an open door, `spareSlots 1` — one server
    is ordered. The same group without `playableSlots` orders none, which is
    today's behaviour pinned rather than assumed.
- The existing suites stay green untouched: that is the evidence for "without
  the field, as today".
- `internal/agent`: `playable_slots` reaches the snapshot; a report with more
  players than playable seats is kept, and one with more players than slots is
  still discarded.
- `internal/agentserver`, `internal/netstate`: `connect` picks by playable free
  seats; `ServerState.playable_slots` carries the effective value.
- `internal/podspec`: the hash goldens do not move.
- `agent` (Kotlin): `playableSlots` validates, is carried on the next report and
  again after a new stream; `StatusLines`/`ListLines` render the equal and the
  differing case; `StatusConversion` maps the field. `FakeApi` follows.
- e2e: a group with `maxPlayers 3`, `playableSlots 1`, `spareSlots 1` and
  `minReplicas 1`; one `spawnery-join` bot joins; a second server is created.

## 10. Docs

- `guides/scaling-and-boosts.md`: a section on playable seats, the formula of
  5.1 and the headroom example.
- `plugin-api/what-a-plugin-can-do.md`: the new method, and that it rides on
  the report rather than asking.
- `guides/cloud-command.md`: the new per-server notation.
- `reference/crds.md`: generated.

## 11. Not in this design

- A playable figure for proxies. A proxy's capacity is
  `ProxyGroup.spec.config.playerLimit`, and nothing sizes proxies by seats.
- Changing the join balancing of the Velocity agent's router, which picks by
  connected players and never read slots.
- Closing the door automatically when a server reaches its playable seats. A
  full server already contributes no capacity; whether it still takes joins is
  the plugin's call.
