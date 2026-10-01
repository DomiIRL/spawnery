# Proxy Transfer on Drain Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A proxy group with `spec.update.transfer` moves the players off a leaving proxy with Minecraft's transfer packet: at once on a change of server, the rest after `forceAfterSeconds` unless their server's door is closed, and a signed cookie brings each player back to their target on the new proxy.

**Architecture:** The operator renders the opt-in into the proxy pod (`accepts-transfers` in velocity.toml, two env vars) and sends each server's door state in `ServerState.joins_closed`. The Velocity agent decides who to transfer with a pure policy over its network mirror, signs a cookie with a key derived from the forwarding secret, and on arrival reads the cookie before choosing the initial server.

**Tech Stack:** Go (operator, render, podspec, proto), Kotlin/Velocity 3.5.1 API (agent), protobuf, Go Minecraft test client (`internal/mcjoin`), kind e2e with real images.

**Spec:** `docs/superpowers/specs/2026-10-01-proxy-transfer-on-drain-design.md`

## Global Constraints

- Every Go/Make command: `nix --extra-experimental-features 'nix-command flakes' develop /home/paul/git/spawnery -c <cmd>` (no `cd` before it). Abbreviated `nd <cmd>` below.
- Agent build and JUnit: `nd make agent` (Nix build; **`git add` new files first** — Nix reads the git index). Single Gradle test runs are not available outside Nix; iterate with `make agent`.
- Machine `paul-desktop` (32 cores): no throttling.
- Field: `ProxyGroupSpec.Update.Transfer *ProxyTransferSpec` with `ForceAfterSeconds *int32` (`json:"forceAfterSeconds,omitempty"`, `+kubebuilder:validation:Minimum=0`); default 300 applied in code (`(*ProxyGroup).TransferForceAfter() (time.Duration, bool)`).
- Env vars (constants in `internal/podspec/proxy.go`, mirrored in `ProxyEnvironment.kt`): `SPAWNERY_TRANSFER_FORCE_AFTER_SECONDS` (decimal seconds), `SPAWNERY_FORWARDING_SECRET_FILE` (absolute path of the mounted secret, `ConfigMountPath + "/" + configSecretFile`). Both present only when `transfer` is set.
- velocity.toml: `accepts-transfers = true` only when `transfer` is set; otherwise unchanged.
- Proto: `ServerState.joins_closed` bool, next free field number in `ServerState`; false = open.
- Cookie key `spawnery:transfer`; payload `uuid|target|expiryUnixSeconds` + HMAC-SHA256; HMAC key = SHA-256 of `"spawnery-transfer-v1\n"` + secret bytes (trimmed of trailing newline as Velocity reads it); expiry 60 s after writing.
- Agent log lines: `spawnery: transferred '<player>' (<reason>) toward '<target>'` with reason `switch` or `forced`; `spawnery: transfer cookie from '<player>' refused: <reason>`.
- Hash goldens (`internal/podspec/hash_golden_test.go`) must stay unchanged for groups without `transfer`.
- Comments default to none (only what a careful reader cannot derive from the code next to it; no fix history).
- Commits: Conventional Commits with scope, body wrapped at 72, ending with
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and
  `Claude-Session: https://claude.ai/code/session_017uAZKpWDVZxbG4Y4XKnGot`; gpg-signed.
- Nothing about any real network in code, tests, docs or commits; example names `edge`, `lobby`, `arena`.
- Generated files after API/proto changes: `nd make manifests generate proto`, committed.

## Review Focus

1. **A transfer when no other proxy can take the player** (single replica, or every other proxy not Ready): must not happen. → Task 5 policy case.
2. **A forged or replayed cookie** (another player's UUID, expired, tampered target, unknown server) must route as a fresh join, never to the named server. → Task 4 cases, Task 6 receiver case.
3. **A player behind a closed door** is never forced, also after the deadline, and is forced once the door opens. → Task 5 cases; Task 9 e2e second case.
4. **A client that cannot be transferred** (protocol < 1.20.5, Velocity throws) is tried once, not every second. → Task 5 "tried once" case; Task 6 catches the exception.
5. **The opt-in off** changes nothing: no env vars, `accepts-transfers = false`, same pod hash. → Task 2 golden + render cases.

---

### Task 1: API field

**Files:**
- Modify: `api/v1alpha1/proxygroup_types.go` (`ProxyUpdateSpec` ~line 198)
- Test: `api/v1alpha1/proxygroup_types_test.go` (create if missing, else add) and the ProxyGroup envtest file in `api/v1alpha1/` for CEL/minimum
- Generated: CRDs, chart CRDs, `docs/reference/crds.md`, deepcopy

**Interfaces:** Produces `ProxyTransferSpec{ForceAfterSeconds *int32}`, `ProxyUpdateSpec.Transfer *ProxyTransferSpec`, `func (g *ProxyGroup) TransferForceAfter() (time.Duration, bool)` — `(0,false)` when unset; `(300s,true)` when `transfer: {}`; `(n s,true)` otherwise.

- [ ] **Step 1: Failing test**

```go
func TestTransferForceAfter(t *testing.T) {
	for _, tc := range []struct {
		name string
		u    *ProxyUpdateSpec
		want time.Duration
		on   bool
	}{
		{"no update", nil, 0, false},
		{"update without transfer", &ProxyUpdateSpec{}, 0, false},
		{"transfer with defaults", &ProxyUpdateSpec{Transfer: &ProxyTransferSpec{}}, 300 * time.Second, true},
		{"transfer at once", &ProxyUpdateSpec{Transfer: &ProxyTransferSpec{ForceAfterSeconds: ptr.To[int32](0)}}, 0, true},
		{"transfer after 90", &ProxyUpdateSpec{Transfer: &ProxyTransferSpec{ForceAfterSeconds: ptr.To[int32](90)}}, 90 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &ProxyGroup{Spec: ProxyGroupSpec{Update: tc.u}}
			got, on := g.TransferForceAfter()
			if got != tc.want || on != tc.on {
				t.Errorf("TransferForceAfter() = %v, %v; want %v, %v", got, on, tc.want, tc.on)
			}
		})
	}
}
```

Plus an envtest beside the existing ProxyGroup CEL tests: `forceAfterSeconds: -1` is refused (minimum).

- [ ] **Step 2: Run → compile failure.** `nd go test ./api/v1alpha1/ -run 'TestTransferForceAfter' -count=1`
- [ ] **Step 3: Implement**

```go
// ProxyTransferSpec moves players off a leaving proxy with Minecraft's
// transfer packet (clients 1.20.5 and newer): at once when they change
// server, the rest after ForceAfterSeconds unless their server has closed
// its door. Setting it rolls the group once.
type ProxyTransferSpec struct {
	// ForceAfterSeconds is how long a leaving proxy waits before it
	// transfers players who have not changed server. Default 300.
	// +kubebuilder:validation:Minimum=0
	// +optional
	ForceAfterSeconds *int32 `json:"forceAfterSeconds,omitempty"`
}
```

Add `Transfer *ProxyTransferSpec \`json:"transfer,omitempty"\`` to `ProxyUpdateSpec` with doc `// Transfer moves players to another proxy instead of waiting for them.` and `+optional`. Add `TransferForceAfter` beside `DrainTimeout` (`defaultTransferForceAfter = 300 * time.Second`).

- [ ] **Step 4:** `nd make manifests generate`; `nd go test ./api/v1alpha1/ -count=1` → PASS.
- [ ] **Step 5: Commit** `feat(api): spec.update.transfer on proxy groups`

---

### Task 2: Render and pod environment

**Files:**
- Modify: `internal/render/velocity.go`, `internal/render/values.go` (a `AcceptsTransfers bool` in `Values` or the proxy part of it — follow how `OnlineMode` reaches velocity.toml), `cmd/spawnery-config/main.go` only if a flag/env carries the value into the renderer
- Modify: `internal/podspec/proxy.go` (env vars; the value that reaches spawnery-config)
- Test: `internal/render/velocity_test.go`, `internal/podspec/proxy_test.go`, `internal/podspec/env_test.go` (reserved `SPAWNERY_` prefix list), `internal/podspec/hash_golden_test.go` (must stay unchanged)

**Interfaces:** Consumes `TransferForceAfter()`. Produces `podspec.EnvTransferForceAfterSeconds = "SPAWNERY_TRANSFER_FORCE_AFTER_SECONDS"`, `podspec.EnvForwardingSecretFile = "SPAWNERY_FORWARDING_SECRET_FILE"`.

- [ ] **Step 1: Read first.** How `OnlineMode` travels from `ProxyGroupSpec.Config` into `render.Values` and velocity.toml (`internal/render/velocity.go` reasserted keys, `spawnery-config` inputs, the config ConfigMap/values the operator writes). `accepts-transfers` must travel the same way and be reasserted by the renderer (an overlay must not be able to flip it on a group without `transfer`, nor off with it — decide which: **the renderer owns it**, same as online-mode).
- [ ] **Step 2: Failing tests.**
  - render: with `AcceptsTransfers: true` the output velocity.toml has `accepts-transfers = true`; with false it has `accepts-transfers = false`; an overlay setting it is overridden.
  - podspec: `BuildProxyPod` for a group with `transfer: {forceAfterSeconds: 90}` has env `SPAWNERY_TRANSFER_FORCE_AFTER_SECONDS=90` and `SPAWNERY_FORWARDING_SECRET_FILE=<ConfigMountPath>/forwarding.secret`; without `transfer` neither var exists.
  - env_test: the two new names are in the reserved list it checks.
  - hash golden test passes unchanged.
- [ ] **Step 3: Implement**, minimal, following the OnlineMode path.
- [ ] **Step 4:** `nd go test ./internal/render/ ./internal/podspec/ ./cmd/spawnery-config/ -count=1` → PASS; `nd make test` → PASS.
- [ ] **Step 5: Commit** `feat(podspec): render the transfer opt-in into proxy pods`

---

### Task 3: `ServerState.joins_closed`

**Files:**
- Modify: `proto/spawnery/agent/v1alpha1/agent.proto` (`ServerState`, next free number)
- Modify: `internal/agent/registry.go` (a read of door state per server, like `Announcements(namespace)`) and `internal/netstate/netstate.go` (~line 224, fill the field)
- Generated: `internal/agentpb/`, `agent/common/src/proto/java/` (`nd make proto`)
- Test: `internal/netstate/netstate_test.go`, `internal/agent/registry_test.go`, `internal/agentpb/contract_test.go` if it lists fields

**Interfaces:** Produces `ServerState.JoinsClosed` (Go) / `getJoinsClosed()` (Java).

- [ ] **Step 1: Read** how `Announcements(namespace)` is keyed (server name vs pod) and how `AcceptingJoins` is stored in `internal/agent/registry.go:86-92, 740-755`. Add a method in the same style, e.g. `ClosedDoors(namespace string) map[string]bool` (server name → closed), returning only closed ones.
- [ ] **Step 2: Failing tests:** registry: a server that sent AcceptJoins(false) appears, one that reopened does not, an unknown one does not. netstate: `Build` sets `JoinsClosed` true for a closed server and false otherwise.
- [ ] **Step 3: Implement**; proto field comment one line: `// True while the server has closed its door (AcceptJoins false). False for a server that never said.`
- [ ] **Step 4:** `nd make proto` then `nd go test ./internal/agent/ ./internal/netstate/ ./internal/agentpb/ -count=1` → PASS; `nd make test`.
- [ ] **Step 5: Commit** `feat(proto): proxies learn which servers have closed their door`

---

### Task 4: Transfer cookie (agent, pure)

**Files:**
- Create: `agent/velocity/src/main/kotlin/cloud/spawnery/agent/velocity/TransferCookie.kt`
- Test: `agent/velocity/src/test/kotlin/cloud/spawnery/agent/velocity/TransferCookieTest.kt`

**Interfaces:** Produces

```kotlin
class TransferCookie(secret: ByteArray, private val clock: () -> Long /* epoch seconds */) {
    fun write(player: UUID, target: String): ByteArray
    fun read(player: UUID, bytes: ByteArray): Result
    sealed interface Result {
        data class Valid(val target: String) : Result
        data class Refused(val reason: String) : Result
    }
    companion object { const val KEY = "spawnery:transfer"; const val LIFETIME_SECONDS = 60L }
}
```

- [ ] **Step 1: Failing tests** (JUnit 5 + kotlin.test, like `RouterTest.kt`):

```kotlin
class TransferCookieTest {
    private val secret = "s3cret".toByteArray()
    private val alice = UUID.fromString("00000000-0000-0000-0000-00000000000a")
    private val bob = UUID.fromString("00000000-0000-0000-0000-00000000000b")
    private var now = 1_000L
    private val cookie = TransferCookie(secret) { now }

    @Test fun `a cookie written for a player reads back its target`() {
        assertEquals(TransferCookie.Result.Valid("lobby-1"), cookie.read(alice, cookie.write(alice, "lobby-1")))
    }
    @Test fun `another player's cookie is refused`() {
        assertIs<TransferCookie.Result.Refused>(cookie.read(bob, cookie.write(alice, "lobby-1")))
    }
    @Test fun `an expired cookie is refused`() {
        val bytes = cookie.write(alice, "lobby-1"); now += 61
        assertIs<TransferCookie.Result.Refused>(cookie.read(alice, bytes))
    }
    @Test fun `a tampered target is refused`() {
        val bytes = cookie.write(alice, "lobby-1")
        val forged = String(bytes).replace("lobby-1", "arena-1").toByteArray()
        assertIs<TransferCookie.Result.Refused>(cookie.read(alice, forged))
    }
    @Test fun `a cookie signed with another secret is refused`() {
        val other = TransferCookie("other".toByteArray()) { now }
        assertIs<TransferCookie.Result.Refused>(cookie.read(alice, other.write(alice, "lobby-1")))
    }
    @Test fun `garbage is refused, not thrown`() {
        assertIs<TransferCookie.Result.Refused>(cookie.read(alice, byteArrayOf(1, 2, 3)))
    }
}
```

- [ ] **Step 2:** `git add` the new files; `nd make agent` → FAIL (unresolved TransferCookie).
- [ ] **Step 3: Implement.** Payload `"$uuid|$target|$expiry"` UTF-8, then `"|"` + Base64url(HMAC-SHA256(key, payload)); key = SHA-256(`"spawnery-transfer-v1\n".toByteArray() + secret`). Compare MACs with `MessageDigest.isEqual`. Server names cannot contain `|` (Kubernetes names); still split from the right so a target is never cut. Reasons: `malformed`, `other player`, `expired`, `bad signature`.
- [ ] **Step 4:** `nd make agent` → PASS.
- [ ] **Step 5: Commit** `feat(agent): a signed cookie for transferred players`

---

### Task 5: Transfer policy (agent, pure) and the mirror's door state

**Files:**
- Create: `agent/velocity/src/main/kotlin/cloud/spawnery/agent/velocity/TransferPolicy.kt`
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/NetworkMirror.kt` — keep `joins_closed` per server name internally (`fun closedDoors(): Set<String>`), **without** changing the public `ServerInfo` record in `agent/api` (that is the published Java API).
- Test: `TransferPolicyTest.kt`, `agent/common/src/test/.../NetworkMirrorTest.kt` (closed doors follow each NetworkState)

**Interfaces:**

```kotlin
class TransferPolicy(private val forceAfterMillis: Long, private val clock: () -> Long /* epoch millis */) {
    data class Picture(
        val self: String,                // this pod's name (SPAWNERY_PROXY)
        val group: String,               // this proxy group's name
        val proxies: List<ProxyInfo>,    // NetworkMirror.proxies()
        val closedDoors: Set<String>,    // NetworkMirror.closedDoors()
    )
    data class Occupant(val id: UUID, val server: String?)
    /** Whether this proxy may transfer at all now; starts the clock the first time it is leaving. */
    fun leaving(picture: Picture): Boolean
    /** Players to force now, each with the target to put in the cookie. Marks them tried. */
    fun forced(picture: Picture, occupants: List<Occupant>): List<Pair<UUID, String>>
    /** Whether a change of server by [player] should become a transfer. Marks the player tried when true. */
    fun onSwitch(picture: Picture, player: UUID): Boolean
    fun forget(player: UUID)   // on disconnect
}
```

Rules (spec §3): leaving = own `ProxyInfo` (by `self`) has `draining`; somewhere else = another `ProxyInfo` with the same `group`, `ready && !draining`; `forced` only when leaving, somewhere else exists, `now - firstLeavingAt >= forceAfterMillis`, occupant has a server, that server not in `closedDoors`, not tried before; `onSwitch` only when leaving, somewhere else exists, not tried.

- [ ] **Step 1: Failing tests** — one per rule:
  - not leaving → nothing (`forced` empty, `onSwitch` false)
  - leaving but no other ready, non-draining proxy of the group (only self; another group's proxy ready) → nothing
  - leaving, before the deadline: `onSwitch` true, `forced` empty
  - after the deadline: occupant on an open server is forced with its server as target; occupant on a closed server is not; the same occupant after the door opens is forced
  - a player forced once is not forced again; a player who switched (tried) is not forced
  - an occupant without a server (still connecting) is not forced
  - the clock starts at the first `leaving(picture)` that returns true, not at construction
  - `forget` lets a reconnecting player with the same UUID be tried again
- [ ] **Step 2:** `git add`; `nd make agent` → FAIL.
- [ ] **Step 3: Implement** (ConcurrentHashMap-backed `tried` set; `firstLeavingAt` as `AtomicLong`, 0 = not yet).
- [ ] **Step 4:** `nd make agent` → PASS.
- [ ] **Step 5: Commit** `feat(agent): who a leaving proxy transfers, and when`

---

### Task 6: Wiring in the Velocity plugin

**Files:**
- Modify: `agent/velocity/src/main/kotlin/cloud/spawnery/agent/velocity/ProxyEnvironment.kt` (+ test): optional `transfer: Transfer?` with `forceAfterSeconds` and `secretFile: Path`; both env vars present → configured; exactly one present, or a non-numeric/negative number, or an unreadable secret file → **dormant transfer** (log once, transfer off) rather than a dormant proxy — the proxy must keep serving.
- Modify: `AgentPlugin.kt`: construct `TransferCookie` and `TransferPolicy` when configured; subscribe to `ServerPreConnectEvent`; a 1 s scheduled pass for forced transfers; arrival handling; `forget` on `DisconnectEvent`.
- Test: `ProxyEnvironmentTest.kt`; a small `Transfers.kt` class holding the event-independent glue (what to do for a switch, for a pass, for an arrival) with a `TransfersTest.kt` using fakes like `FakePlayers.kt`, so `AgentPlugin` only forwards events.

**Velocity API facts to use** (verify against the velocity-api jar on the test classpath — `agent/velocity/build.gradle.kts` pins 3.5.1):
- `Player.transferToHost(InetSocketAddress)`; throws for clients older than 1.20.5 — catch, log, count as tried.
- `Player.storeCookie(Key, ByteArray)`; `Player.requestCookie(Key)` with the answer in `CookieReceiveEvent`.
- `Player.getVirtualHost(): Optional<InetSocketAddress>` — transfer target; if empty, do not transfer.
- `InboundConnection.getHandshakeIntent()` == `HandshakeIntent.TRANSFER` marks an arriving transfer.
- Key: `net.kyori.adventure.key.Key.key("spawnery", "transfer")`.

**Arrival:** request the cookie only for `HandshakeIntent.TRANSFER` connections, and before `PlayerChooseInitialServerEvent` decides. Use an async event task (`EventTask.withContinuation`) on an event that runs before initial server choice and allows waiting (read Velocity's login event order; `LoginEvent`/`PostLoginEvent` are candidates — pick the one in which `requestCookie` is permitted and that completes before `PlayerChooseInitialServerEvent`), complete the continuation on `CookieReceiveEvent` for that player or after a 2 s timeout. Stash the verified target per UUID; `onChooseInitialServer` uses it if `proxy.getServer(target)` exists and the mirror shows it registered, else the normal `router.choose`. Log refusals with the reason.

**Switch:** in `ServerPreConnectEvent`, only when the player already has a current server (a switch, not the initial connect) and `policy.onSwitch(...)` is true: deny the connection (`ServerResult.denied()`), `storeCookie(KEY, cookie.write(uuid, target.serverInfo.name))`, then `transferToHost(virtualHost)`.

**Forced pass:** every second, `policy.forced(picture, occupants)`; for each, store the cookie for the current server, transfer.

- [ ] **Step 1: Failing tests** for `ProxyEnvironment` (absent → no transfer; both → configured; one missing / bad number / missing file → transfer off with a reason, proxy still configured) and `Transfers` (switch path denies + stores + transfers; switch when policy says no → untouched; pass transfers each forced occupant; a transfer that throws is logged and not retried; arrival with valid cookie → target; invalid → null with reason logged).
- [ ] **Step 2:** `git add`; `nd make agent` → FAIL.
- [ ] **Step 3: Implement.** Keep `AgentPlugin` changes to forwarding events into `Transfers`.
- [ ] **Step 4:** `nd make agent` → PASS; `nd make test` (the Go-side agreement tests that read Kotlin constants, e.g. env names, must pass — add the two env names wherever such a cross-language check exists).
- [ ] **Step 5: Commit** `feat(agent): a leaving proxy transfers its players`

---

### Task 7: `mcjoin` follows a transfer

**Files:**
- Modify: `internal/mcjoin/mcjoin.go`, `cmd/spawnery-join/main.go` (flag `--follow-transfers`)
- Test: `internal/mcjoin/mcjoin_test.go`

**What it must do** for the protocol version mcjoin already speaks (read its handshake constant; take every packet ID below from that version's protocol table — cite the source URL in the commit body, not in a comment):
- clientbound **Store Cookie** (configuration and play state): remember key → bytes.
- clientbound **Cookie Request** (login, configuration, play): answer with serverbound **Cookie Response** (key, has-payload, payload).
- clientbound **Transfer** (configuration and play): close, reconnect to the given host/port with handshake next-state **3 (transfer)**, log in again, answer the cookie request from the stored cookies, and continue to hold.
- `Result` gains `Transfers int` and the server it ended on (whatever mcjoin already reports for routing; if it reports nothing about the backend, the e2e reads the proxy's roster instead — see Task 9).

- [ ] **Step 1: Failing tests** against the in-process fake server the existing tests use (`mcjoin_test.go`): store cookie → cookie request answered with the bytes; transfer → a second connection arrives with next-state 3 and the cookie is answered there; `--follow-transfers` off → a transfer ends the hold as today (error naming the transfer).
- [ ] **Step 2:** run → FAIL. `nd go test ./internal/mcjoin/ -count=1`
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** → PASS; `nd make test`.
- [ ] **Step 5: Commit** `feat(mcjoin): follow a transfer with its cookies`

---

### Task 8: Docs

**Files:** `docs/guides/updates-and-drain.md` (section "Proxies wait for their players too": add "Moving players to another proxy"), `docs/guides/upgrading.md` only if the release note fits (cap 950 words; it is at 949 — prefer the guide).

- [ ] **Step 1:** Write the section from spec §2–§3: the YAML, the two moments, closed doors shield, clients 1.20.5+, `maxStaleSeconds` as the last bound, the enabling roll, cookies routing back, a forwarding-secret rotation routes transferred players as fresh joins, what the agent logs.
- [ ] **Step 2:** `nd make test` (docs-length linter) → PASS.
- [ ] **Step 3: Commit** `feat(docs): moving players off a draining proxy`

---

### Task 9: Real-system check (tutorial e2e), bite proof, hand-check notes

**Files:** `test/e2e/tutorial_test.go` (new test), `hack/e2e-tutorial.sh` (add to the `-run` list), `docs/tutorial/network.yaml` only if the tutorial proxy needs `onlineMode: false` for mcjoin (read how existing tutorial tests join — `TestTutorialPath` uses mcjoin against the tutorial's NodePort).

- [ ] **Step 1: Test** `TestTutorialTransferOnDrain` (gated by `SPAWNERY_E2E_TUTORIAL=1`):
  1. Patch the tutorial ProxyGroup: `replicas: 2`, `update.transfer.forceAfterSeconds: 0`; wait for the roll this causes to finish (both proxies Ready, status.changeover "").
  2. Join with mcjoin `--follow-transfers --hold <long>` in a goroutine; read from the network picture / proxy roster which proxy pod and which backend server the player is on.
  3. Change an env var on the ProxyGroup (rolls it). Wait until the player's original proxy pod is gone.
  4. Assert: mcjoin reports ≥ 1 transfer and is still connected; the player is on a different proxy pod; the player is on the **same backend server** as before.
  5. Second case: close the backend's door (`/cloud` or the plugin API path the tests already use for AcceptJoins — read the e2e helpers; if none exists, use `kubectl exec` into the backend with the agent's command, or skip with a clear `t.Skip` reason and cover it by envtest/Kotlin only — say which in the report), roll again, assert the player is still on the old proxy after `forceAfterSeconds` + 10 s.
- [ ] **Step 2: Run** on paul-desktop exactly as Task 9 of the changeover-stages plan did:
  `systemd-run --scope --user --property=Delegate=yes -- nix --extra-experimental-features 'nix-command flakes' develop /home/paul/git/spawnery -c env KIND_EXPERIMENTAL_PROVIDER=podman CONTAINER=podman bash hack/e2e-tutorial.sh`
  in the background; read the output file when it completes; quote PASS lines.
- [ ] **Step 3: Bite proof** in a throwaway worktree: make `Transfers` never call `transferToHost`; the image must be rebuilt from the worktree (the script builds images from its own checkout); record the FAIL; remove the worktree.
- [ ] **Step 4: Hand-check notes** for Paul in the report (not in the repo): how to see it with a real client on the tutorial cluster — join, `kubectl patch` the proxy group's env, watch the client; once switching server (`/server lobby` or the tutorial's equivalent), once standing still with `forceAfterSeconds: 30`. What to look for: loading screen length, landing server.
- [ ] **Step 5:** `nd make test`, `nd make lint` → PASS. **Commit** `test(e2e): a rolled proxy hands its player to another`

---

## After the plan

Push to `feat/changeover-stages`; PR #83 gains these commits (its description is updated with a section on transfers). Release (minor; `imageVersion` moves because `agent/` changed, `operatorVersion`, chart) only on Paul's word.
