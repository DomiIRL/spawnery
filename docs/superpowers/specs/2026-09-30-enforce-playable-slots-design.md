# Enforcing playable slots — Design

**Status:** approved in conversation 2026-09-30 (permission per group; the
refusal is a translatable component, not a configured text).

**Builds on:** `2026-09-29-playable-slots-design.md`.

## Goal

A group can make its playable slots a limit, not only a count: once a server
holds as many players as its playable slots, nobody else gets in — except
players with a bypass permission (staff, spectators). `maxPlayers` stays the
hard limit for everyone.

## API

```yaml
spec:
  maxPlayers: 100
  playableSlots: 12
  enforcePlayableSlots: true
```

- `ServerGroupSpec.EnforcePlayableSlots bool`, optional, default false.
  Without it nothing changes.
- Read at runtime: it reaches the servers over the agent channel and feeds no
  pod, so it is **not** part of `DesiredServerHash`; flipping it rolls
  nothing.
- Allowed for every server group type; on a group without `playableSlots`
  and without a plugin value, the playable slots are the slots and the check
  never refuses anyone `maxPlayers` would admit.

## Protocol

`GroupState` gains `int32 playable_slots = 9` (the group's
`spec.playableSlots`, 0 if unset) and `bool enforce_playable_slots = 10`. An
older agent ignores both; an older operator sends neither, which reads as
"not enforced".

## The check (Paper agent)

On Bukkit's `PlayerLoginEvent`, when this server's group has
`enforce_playable_slots`. It is deprecated for removal in Paper 26.3, but it
is the only login event that has the `Player` and so its permissions; its
successors (`PlayerConnectionValidateLoginEvent`, `PlayerServerFullCheckEvent`)
carry only a profile, and spawnery cannot ask a permissions plugin directly.
When Paper removes it, the check moves to the proxy (`ServerPreConnectEvent`),
which knows permissions but sees counts only as reported -- decided
2026-09-30 to take the exact count now and that move later.

- **Effective playable slots**: the plugin's runtime value if above 0, else
  the group's `playable_slots` if above 0, else the server's max players;
  clamped to `[1, max players]` — the same order the operator uses.
- **Seated players**: online players without the bypass permission, plus
  players this agent admitted and who have not joined or left yet (logins
  are handled one after another, but a player admitted in this tick is not
  online yet; without this, a burst of logins overshoots).
- A player **with** `spawnery.join.full.<group>` is always admitted (up to
  `maxPlayers`, which the server enforces itself) and never counts as seated.
- A player without it is refused when `seated >= playable`.

Bypass players do not take a seat, so a watching admin never takes a
player's place.

**After review:** the admitted-not-joined players are released on join,
quit and `PlayerConnectionCloseEvent` (a connection can end during the
configuration phase, where neither of the first two fires) and expire after
five minutes as a backstop. The listener is registered only once the
server's group turns enforcement on, because any `PlayerLoginEvent`
listener makes Paper refuse its reconfiguration API on the whole server.
The operator keeps counting every player, bypass players included: its
free seats and scaler see a watching admin as seated, which at worst orders
the next server one watcher early; the reported player count also decides
whether a server is empty, so it is not bent for this.

## The refusal

The login is disallowed with an Adventure **translatable component**, key
`spawnery.join.full`, argument the group's display name, fallback
`This round is full.`. A network with its own translations renders the key
in the player's language; one without shows the fallback. No text is
configured on the group.

Velocity shows the refusal. On a server switch the player stays where they
were; on the network's first join the proxy's usual failed-connect handling
applies (the next fallback server). The proxy is not changed: `connect` to a
group already picks the server with the most free playable seats.

## Plugin API

Nothing new. A game sets its round size with `playableSlots(n)` as today;
with enforcement on, that is also the size of the door. The docs name the
permission and the translation key.

## Testing

- Operator: `GroupState` carries both fields from the spec (unit test of the
  state builder); the fields do not move `DesiredServerHash` (golden test
  unchanged).
- Paper agent (JUnit, the pure decision extracted from the event handler):
  not enforced admits; enforced and seats free admits; enforced and full
  refuses; a bypass player is admitted when full and does not count; a
  pending admitted player counts; runtime value beats the group value beats
  max players; the refusal is the translatable component with the key and
  fallback.
- On a cluster: a group with `playableSlots: 1` and enforcement; a second
  player is refused with the message, a player with the permission gets in.

## Not in this design

- A proxy-side pre-check (to refuse without the backend handshake).
- Per-player seat reservations or queues.
