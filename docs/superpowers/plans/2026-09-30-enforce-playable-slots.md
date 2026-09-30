# Enforcing Playable Slots — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `ServerGroup.spec.enforcePlayableSlots` turns a server's playable slots into a login limit, bypassed by `spawnery.join.full.<group>`, refused with the translatable component `spawnery.join.full`.

**Architecture:** The operator carries the group's `playableSlots` and the new flag in `GroupState` (fields 9, 10). The common agent keeps them per group in `NetworkMirror`, internal to the agent (the public `Group` record is unchanged). The Paper agent decides on `PlayerLoginEvent` with a pure `LoginGate` function and tracks admitted-but-not-joined players.

**Tech Stack:** Go (API types, netstate), protobuf, Kotlin (agent common + paper), Paper 26.3 API (`PlayerLoginEvent`, Adventure).

**Spec:** `docs/superpowers/specs/2026-09-30-enforce-playable-slots-design.md`

## Global Constraints

- Dev shell for every command: `nix --extra-experimental-features 'nix-command flakes' develop -c <cmd>`; envtest packages with `-p 1` on an 8-core machine.
- After API type or proto changes: `make manifests generate proto` and commit the generated files.
- `git add` new files before `make agent`.
- `enforcePlayableSlots` must not feed `DesiredServerHash` (`internal/podspec/hash_golden_test.go` stays unchanged).
- Permission string: `spawnery.join.full.<group>` exactly. Translation key: `spawnery.join.full`, one argument (the group's display name, else its name), fallback `This round is full.`
- Conventional Commits, signed, session trailers; nothing consumer-specific in this public repo.

## Review Focus

1. **A burst of logins for the last seat** — must not overshoot: pending admitted players count. Pinned in Task 3 (`a player admitted but not yet joined takes a seat`).
2. **A bypass player on a full server** — admitted, and never counted as seated. Pinned in Task 3.
3. **An older operator (no fields)** — reads as not enforced. Pinned in Task 2 (`absent fields mean not enforced`).
4. **A plugin value above max players, or 0** — clamped / falls back. Pinned in Task 3.
5. **Flipping the flag rolls nothing.** Pinned in Task 1 (golden test unchanged; an explicit test that the hash ignores the field).

---

### Task 1: API field and GroupState (operator)

**Files:** Modify `api/v1alpha1/servergroup_types.go`, `proto/spawnery/agent/v1alpha1/agent.proto`, `internal/netstate/netstate.go`; Test `internal/netstate/netstate_test.go`, `internal/podspec/hash_golden_test.go` (read only) or a new test beside it.

- [ ] **Step 1: Failing tests.** In `internal/netstate` (use the file's existing fixture for building a state from ServerGroups): a group with `PlayableSlots: ptr.To[int32](12)` and `EnforcePlayableSlots: true` yields `GroupState.PlayableSlots == 12` and `EnforcePlayableSlots == true`; a group without them yields 0 and false. In `internal/podspec`: `DesiredServerHash` of a group with and without `EnforcePlayableSlots` is equal.
- [ ] **Step 2:** run → build failure (unknown fields).
- [ ] **Step 3: Implement.**
  - API, after `PlayableSlots`:

```go
	// EnforcePlayableSlots refuses a login once a server holds as many
	// players as its playable slots, except for players with the permission
	// spawnery.join.full.<group>, who never take a seat. Read at runtime over
	// the agent channel; changing it restarts no server.
	// +optional
	EnforcePlayableSlots bool `json:"enforcePlayableSlots,omitempty"`
```

  - proto `GroupState`: `int32 playable_slots = 9;` (the group's spec.playableSlots, 0 if unset) and `bool enforce_playable_slots = 10;`, each with a comment saying an older operator sends neither, which reads as not enforced.
  - `netstate.go` ServerGroup branch: `PlayableSlots: ptr.Deref(g.Spec.PlayableSlots, 0), EnforcePlayableSlots: g.Spec.EnforcePlayableSlots,`.
  - `make manifests generate proto`.
- [ ] **Step 4:** `go test ./internal/netstate/ ./internal/podspec/ -count=1` green; commit `feat(api): enforcePlayableSlots`.

### Task 2: The mirror keeps each group's admission (agent common)

**Files:** Modify `agent/common/src/main/kotlin/cloud/spawnery/agent/NetworkMirror.kt`; Test `agent/common/src/test/kotlin/cloud/spawnery/agent/NetworkMirrorTest.kt`.

- [ ] **Step 1: Failing tests:**

```kotlin
    @Test
    fun `a group's admission comes from its state`() {
        val mirror = NetworkMirror()
        mirror.apply(NetworkState.newBuilder().addGroups(
            GroupState.newBuilder().setName("duels").setPlayableSlots(12).setEnforcePlayableSlots(true)).build())
        assertEquals(GroupAdmission(playableSlots = 12, enforce = true), mirror.admission("duels"))
    }

    @Test
    fun `absent fields mean not enforced`() {
        val mirror = NetworkMirror()
        mirror.apply(NetworkState.newBuilder().addGroups(GroupState.newBuilder().setName("duels")).build())
        assertEquals(GroupAdmission(playableSlots = 0, enforce = false), mirror.admission("duels"))
        assertEquals(null, mirror.admission("unknown"))
    }
```

- [ ] **Step 2:** `make agent` fails (unresolved `GroupAdmission`/`admission`).
- [ ] **Step 3:** `data class GroupAdmission(val playableSlots: Int, val enforce: Boolean)`; the snapshot gains `admissions: Map<String, GroupAdmission>` filled in `apply` from `state.groupsList`; `fun admission(group: String): GroupAdmission? = snapshot.admissions[group]`. Not exposed through `MirrorApi`.
- [ ] **Step 4:** `git add -A agent && make agent` green; commit `feat(agent): the mirror keeps each group's admission`.

### Task 3: The login gate (Paper agent)

**Files:** Create `agent/paper/src/main/kotlin/cloud/spawnery/agent/paper/LoginGate.kt`; Modify `AgentPlugin.kt`; Test `agent/paper/src/test/kotlin/cloud/spawnery/agent/paper/LoginGateTest.kt`.

**Produces:** `object LoginGate { fun admits(...): Boolean; fun effectivePlayable(pluginValue: Int, groupValue: Int, maxPlayers: Int): Int; fun refusal(groupDisplayName: String): Component; const val KEY = "spawnery.join.full"; fun permission(group: String) = "spawnery.join.full.$group" }`.

- [ ] **Step 1: Failing tests** (`LoginGateTest`):

```kotlin
class LoginGateTest {
    private fun admits(enforce: Boolean = true, bypass: Boolean = false, seated: Int, pending: Int = 0, playable: Int) =
        LoginGate.admits(enforce = enforce, bypass = bypass, seated = seated, pending = pending, playable = playable)

    @Test fun `not enforced admits a full server`() = assertTrue(admits(enforce = false, seated = 12, playable = 12))
    @Test fun `a free seat admits`() = assertTrue(admits(seated = 11, playable = 12))
    @Test fun `a full server refuses`() = assertFalse(admits(seated = 12, playable = 12))
    @Test fun `a bypass player gets onto a full server`() = assertTrue(admits(bypass = true, seated = 12, playable = 12))
    @Test fun `a player admitted but not yet joined takes a seat`() = assertFalse(admits(seated = 11, pending = 1, playable = 12))

    @Test fun `the plugin's value beats the group's`() = assertEquals(8, LoginGate.effectivePlayable(8, 12, 100))
    @Test fun `the group's value stands without the plugin's`() = assertEquals(12, LoginGate.effectivePlayable(0, 12, 100))
    @Test fun `without either the playable slots are max players`() = assertEquals(100, LoginGate.effectivePlayable(0, 0, 100))
    @Test fun `a value above max players is max players`() = assertEquals(100, LoginGate.effectivePlayable(500, 0, 100))

    @Test fun `the refusal is the translatable key with the group and a fallback`() {
        val c = LoginGate.refusal("Duels") as TranslatableComponent
        assertEquals("spawnery.join.full", c.key())
        assertEquals("This round is full.", c.fallback())
        assertEquals(Component.text("Duels"), c.arguments().single().asComponent())
    }

    @Test fun `the permission names the group`() = assertEquals("spawnery.join.full.duels", LoginGate.permission("duels"))
}
```

- [ ] **Step 2:** `make agent` fails (unresolved `LoginGate`).
- [ ] **Step 3: Implement** `LoginGate` (pure; `effectivePlayable` = first of plugin>0, group>0, maxPlayers, clamped to `[1, maxPlayers]`; `admits` = `!enforce || bypass || seated + pending < playable`; `refusal` = `Component.translatable().key(KEY).fallback("This round is full.").arguments(Component.text(name)).build()`). In `AgentPlugin`:
  - a `pending: MutableSet<UUID>` touched on the main thread only;
  - `@EventHandler(priority = EventPriority.HIGH) fun onLogin(event: PlayerLoginEvent)`: skip if `event.result != ALLOWED`; `val group = System.getenv("SPAWNERY_GROUP")`; `val admission = mirror.admission(group) ?: return`; `if (!admission.enforce) return`; `bypass = event.player.hasPermission(LoginGate.permission(group))`; `seated = Bukkit.getOnlinePlayers().count { !it.hasPermission(LoginGate.permission(group)) }`; `playable = LoginGate.effectivePlayable(state.playable, admission.playableSlots, Bukkit.getMaxPlayers())`; if `!LoginGate.admits(...)` → `event.disallow(PlayerLoginEvent.Result.KICK_FULL, LoginGate.refusal(displayName))` where `displayName` is the mirror group's display name or the group name; else if not bypass → `pending += event.player.uniqueId`;
  - `@EventHandler(priority = EventPriority.MONITOR) fun onJoin(PlayerJoinEvent)` and `onQuit(PlayerQuitEvent)` remove the UUID from `pending`; `onLoginMonitor(PlayerLoginEvent)` at MONITOR removes it again if a later listener disallowed the login.
  - Mark the use of the deprecated event with `@Suppress("DEPRECATION")` and a one-line comment pointing at the spec.
- [ ] **Step 4:** `git add -A agent && make agent` green; commit `feat(agent): refuse a login past the playable slots`.

### Task 4: Docs and finish

- [ ] `docs/guides/scaling-and-boosts.md` (the playable-slots section): enforcement, the permission, the key and fallback, that bypass players take no seat, that the check is the server's and why (`PlayerLoginEvent`, planned move to the proxy). `docs/plugin-api/what-a-plugin-can-do.md`: one paragraph that `playableSlots(n)` is also the door when the group enforces it. Commit `docs: enforcing playable slots`.
- [ ] `nix … develop -c env GOFLAGS=-p=1 make test` and `make agent`; final review; push; PR (not merged by the agent).
