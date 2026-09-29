# Playable Slots Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a group count fewer seats as capacity (`playableSlots`) than its
servers admit (`maxPlayers`), from the group's spec or from the plugin at
runtime, everywhere the operator counts free seats and in `/cloud`.

**Architecture:** A new optional CRD field and a new field on the periodic
agent report feed one pure resolution rule in `internal/controller`
(reported value → spec → slots, clamped to `[1, slots]`). Every free-seat
computation reads the resolved value through one `ServerView` method; the
group's per-server capacity unit reads the spec. The resolved value is
mirrored into `Server.status`, travels in `ServerState` and `InstanceStatus`,
and is rendered by the agents.

**Tech Stack:** Go (controller-runtime, envtest, protobuf), Kotlin/Java agents
(Gradle through Nix), kind e2e.

**Spec:** `docs/superpowers/specs/2026-09-29-playable-slots-design.md`

## Global Constraints

- Every command runs in the dev shell with the flake path as an argument, never
  after a `cd`. Define once per shell:
  `ndev() { nix --extra-experimental-features 'nix-command flakes' develop /home/paul/git/spawnery-playable-slots -c "$@"; }`
  and run commands from `/home/paul/git/spawnery-playable-slots`.
- Machine is paul-desktop (32 cores): no `-p 1`.
- `ServerGroupSpec.PlayableSlots *int32`, `json:"playableSlots,omitempty"`,
  CEL `!has(self.playableSlots) || (self.playableSlots >= 1 && self.playableSlots <= self.maxPlayers)`.
- It is **not** part of `DesiredServerHash`; the hash goldens must not move.
- `PlayerCount.playable_slots = 5`, `ServerState.playable_slots = 13`,
  `InstanceStatus.playable_slots = 16`. 0 on the wire means "not set" /
  "same as slots".
- `slots` keeps its meaning everywhere (the hard limit, `maxPlayers`).
- Resolution: reported `playable_slots` if > 0, else `spec.playableSlots` if
  set, else `slots`; clamped to `[1, slots]`; free = `max(0, playable − players)`.
- Group capacity where nothing has reported: `spec.playableSlots` if set, else
  `maxPlayers`.
- Java API: `void playableSlots(int slots)` on `SpawneryApi`; negative →
  `IllegalArgumentException`.
- `/cloud`: equal → unchanged (`9 / 100`); different → `9 / 12 · max 100`
  (compact lists: `9/12 · max 100`); bar against playable, capped at 100 %.
- A minor release: 0.13.0 for the operator, the images and the chart.
- Public repository: invented examples only (`duels`, `lobby`), no names of any
  network that uses this.
- Comments: none by default; only what the code cannot say.
- Nix builds read the git index: `git add` new files before `make agent`.
- Generated files are committed: `make manifests generate proto` after API or
  proto changes.

**Deviations from the spec, to be confirmed by the reviewer of this plan:**

1. Spec §6 says a proxy call throws `IllegalStateException`. The existing
   server-only method `holdReadiness` throws `UnsupportedOperationException`
   on a proxy (`agent/api/.../SpawneryApi.java:341`,
   `agent/common/.../MirrorApi.kt:117-122`). This plan follows that precedent
   and amends the spec in Task 10.
2. Spec §9 puts the e2e case in the default e2e suite. That suite's images are
   deliberately unresolvable (`test/e2e/lifecycle_test.go:121-124`), so no agent
   ever reports there. Only the nightly tutorial run
   (`hack/e2e-tutorial.sh`, `SPAWNERY_E2E_TUTORIAL=1`) has live agents and a
   join. Task 9 adds the case there, with the tutorial group's numbers
   (`maxPlayers 20`, `playableSlots 1`, `spareSlots 2`, `maxReplicas 3`).
3. Spec is silent on `ServerInfo.freeSlots()`
   (`agent/api/.../ServerInfo.java:130-140`, "how many more players this server
   would accept"). This plan leaves it as the hard-limit figure, which is what
   its javadoc promises.

## Review Focus

1. **A Server object written before this release** carries
   `status.playableSlots: 0`; `connect` and the agents must read that as
   "equal to slots", not as a full server. Tests: Task 6 (`resolveTarget`),
   Task 7 (record normalisation).
2. **`maxPlayers` lowered below an existing `playableSlots`** must be refused by
   the API server with the CEL message rather than accepted and clamped
   silently. Test: Task 1.
3. **A negative `playable_slots` from a misbehaving agent** must be discarded
   without dropping the stream and without replacing the last good value.
   Test: Task 3.
4. **A server whose group is gone** (reconciled against the fallback group with
   `maxPlayers 0`) must mirror its report unclamped, playable equal to slots.
   Test: Task 5.
5. **A plugin taking its value back** (`playableSlots(0)` after `12`) must hand
   the decision back to the spec on the next report. Test: Task 7.

---

### Task 1: CRD field, validation and status field

**Files:**
- Modify: `api/v1alpha1/servergroup_types.go` (field after `MaxPlayers`, ~line 171; CEL marker above `type ServerGroupSpec struct`, ~line 156)
- Modify: `api/v1alpha1/server_types.go` (after `Slots`, ~line 155)
- Test: `api/v1alpha1/servergroup_envtest_test.go`
- Test: `internal/podspec/hash_test.go` (`TestDesiredServerHashDiscriminates`)
- Generated: `api/v1alpha1/zz_generated.deepcopy.go`, `config/crd/bases/*`, `charts/spawnery/templates/crds.yaml`, `docs/reference/crds.md`

**Interfaces:**
- Produces: `ServerGroupSpec.PlayableSlots *int32`; `ServerStatus.PlayableSlots int32` (`json:"playableSlots"`).

- [ ] **Step 1: Write the failing envtest**

Append to `api/v1alpha1/servergroup_envtest_test.go`:

```go
func TestPlayableSlotsIsBoundedByMaxPlayers(t *testing.T) {
	c, ctx := testenv.Client(t)
	ns := testenv.Namespace(t, ctx, c)

	for name, tc := range map[string]struct {
		playable *int32
		ok       bool
	}{
		"absent":              {nil, true},
		"one":                 {ptr.To[int32](1), true},
		"equal to maxPlayers": {ptr.To[int32](100), true},
		"zero":                {ptr.To[int32](0), false},
		"above maxPlayers":    {ptr.To[int32](101), false},
	} {
		t.Run(name, func(t *testing.T) {
			g := ephemeralGroup(ns, "playable-"+strings.ToLower(strings.ReplaceAll(name, " ", "-")))
			g.Spec.PlayableSlots = tc.playable
			err := c.Create(ctx, g)
			if tc.ok && err != nil {
				t.Fatalf("playableSlots %v was refused: %v", tc.playable, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("playableSlots %v was accepted", ptr.Deref(tc.playable, -1))
			}
		})
	}
}

func TestLoweringMaxPlayersBelowPlayableSlotsIsRefused(t *testing.T) {
	c, ctx := testenv.Client(t)
	ns := testenv.Namespace(t, ctx, c)
	g := ephemeralGroup(ns, "duels")
	g.Spec.PlayableSlots = ptr.To[int32](12)
	if err := c.Create(ctx, g); err != nil {
		t.Fatalf("create: %v", err)
	}
	g.Spec.MaxPlayers = 10
	if err := c.Update(ctx, g); err == nil {
		t.Fatal("maxPlayers 10 below playableSlots 12 was accepted")
	}
}

func TestPlayableSlotsIsAllowedOnEveryType(t *testing.T) {
	c, ctx := testenv.Client(t)
	ns := testenv.Namespace(t, ctx, c)
	p := persistentGroup(ns, "persistent-playable")
	p.Spec.PlayableSlots = ptr.To[int32](10)
	if err := c.Create(ctx, p); err != nil {
		t.Fatalf("persistent: %v", err)
	}
	o := onDemandGroup(ns, "ondemand-playable")
	o.Spec.PlayableSlots = ptr.To[int32](5)
	if err := c.Create(ctx, o); err != nil {
		t.Fatalf("on-demand: %v", err)
	}
}
```

Add `"strings"` to the file's imports.

In `internal/podspec/hash_test.go`, add a row to the `cases` slice of
`TestDesiredServerHashDiscriminates`, after the `drain.timeoutSeconds` row:

```go
		{
			name:    "playableSlots does not change it",
			mutate:  func(g *spawneryv1alpha1.ServerGroup) { g.Spec.PlayableSlots = ptr.To(int32(12)) },
			changed: false,
		},
```

- [ ] **Step 2: Run to verify they fail**

Run: `ndev go test ./api/v1alpha1/ ./internal/podspec/ -run 'PlayableSlots|TestDesiredServerHashDiscriminates' -count=1`
Expected: FAIL to compile — `g.Spec.PlayableSlots undefined`.

- [ ] **Step 3: Add the fields and the rule**

In `api/v1alpha1/servergroup_types.go`, add this marker line directly below
the existing `storage.size must not shrink` marker (the last one above
`type ServerGroupSpec struct`):

```go
// +kubebuilder:validation:XValidation:rule="!has(self.playableSlots) || (self.playableSlots >= 1 && self.playableSlots <= self.maxPlayers)",message="spec.playableSlots must be between 1 and spec.maxPlayers"
```

After the `MaxPlayers` field:

```go
	// PlayableSlots is how many seats of each server count as capacity: what
	// spareSlots, status.freeSlots and a connect to the group measure. Unset,
	// every seat up to maxPlayers counts. A plugin can set its own server's
	// figure at runtime, which wins over this one.
	//
	// maxPlayers stays the limit a server enforces, so the seats between the
	// two are room for players the group is not sized by, such as spectators.
	// +optional
	PlayableSlots *int32 `json:"playableSlots,omitempty"`
```

In `api/v1alpha1/server_types.go`, after `Slots`:

```go
	// PlayableSlots is how many of Slots count as capacity: the plugin's
	// figure, else the group's spec.playableSlots, else Slots.
	// +optional
	PlayableSlots int32 `json:"playableSlots"`
```

- [ ] **Step 4: Regenerate**

Run: `ndev make manifests generate`
Expected: `zz_generated.deepcopy.go`, `config/crd/bases/spawnery.cloud_servergroups.yaml`, `config/crd/bases/spawnery.cloud_servers.yaml`, `charts/spawnery/templates/crds.yaml` and `docs/reference/crds.md` change; nothing else.

- [ ] **Step 5: Run to verify they pass**

Run: `ndev go test ./api/v1alpha1/ ./internal/podspec/ -count=1`
Expected: PASS, including `TestTheServerPodDigestHasNotMoved` and `TestTheEphemeralServerPodDigestHasNotMoved` unchanged.

- [ ] **Step 6: Commit**

```bash
git add api/v1alpha1 internal/podspec/hash_test.go config charts/spawnery/templates docs/reference/crds.md
git commit -m "feat(api): spec.playableSlots on ServerGroup

The seats of each server that count as capacity, between 1 and
maxPlayers, and its resolved value on Server.status. It feeds no pod,
so it is not part of the pod digest and editing it rolls nothing.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FyT72YgbxTAVLjWBWZWWbk"
```

---

### Task 2: Protocol fields

**Files:**
- Modify: `proto/spawnery/agent/v1alpha1/agent.proto` (`PlayerCount`, `ServerState`, `InstanceStatus`)
- Generated: `internal/agentpb/`, `agent/common/src/proto/java/`
- Test: `internal/agentpb/contract_test.go` (existing, must stay green)

**Interfaces:**
- Produces: Go `PlayerCount.GetPlayableSlots() int32`, `ServerState.PlayableSlots`, `InstanceStatus.PlayableSlots`; Java `PlayerCount.Builder.setPlayableSlots(int)`, `ServerState.getPlayableSlots()`, `InstanceStatus.getPlayableSlots()`.

- [ ] **Step 1: Add the fields**

In `message PlayerCount`, after `double mspt = 4;`:

```proto
  // Server agents only: the seats the plugin says count as capacity. 0 means
  // it said nothing -- what a proxy and an agent older than this field send --
  // and the group's spec.playableSlots decides. It cannot ride in slots: the
  // registry discards a report with more players than slots, and players
  // beyond the playable seats are legitimate.
  int32 playable_slots = 5;
```

In `message ServerState`, after `string node = 12;`:

```proto
  // How many of slots count as capacity, as the operator resolved it. 0 from
  // an operator older than this field; read it as equal to slots.
  int32 playable_slots = 13;
```

In `message InstanceStatus`, after `string node = 15; // as ServerState.node`:

```proto
  int32 playable_slots = 16; // as ServerState.playable_slots
```

- [ ] **Step 2: Regenerate and build**

Run: `ndev make proto && ndev go build ./... && ndev go test ./internal/agentpb/ -count=1`
Expected: regenerated Go and Java sources; build and contract test PASS.

- [ ] **Step 3: Commit**

```bash
git add proto internal/agentpb agent/common/src/proto/java
git commit -m "feat(proto): playable_slots on the report, the server state and the status

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FyT72YgbxTAVLjWBWZWWbk"
```

---

### Task 3: The registry keeps the reported value

**Files:**
- Modify: `internal/agent/registry.go` (`Snapshot` ~line 45, `entry` ~line 113, new method after `ReportTicks` ~line 324, `Lookup` ~line 713)
- Modify: `internal/agentserver/server.go` (`ServerMessage_PlayerCount` case ~line 612)
- Test: `internal/agent/registry_test.go`
- Test: `internal/agentserver/server_envtest_test.go`

**Interfaces:**
- Consumes: `PlayerCount.GetPlayableSlots()` (Task 2).
- Produces: `func (r *Registry) ReportPlayableSlots(key string, playable int32) error`; `Snapshot.PlayableSlots int32`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/registry_test.go`:

```go
func TestThePlayableFigureReachesTheSnapshot(t *testing.T) {
	r, _ := newTestRegistry()
	r.Connect("pod-uid-1", RoleServer)
	if err := r.ReportPlayers("pod-uid-1", 14, 100); err != nil {
		t.Fatalf("ReportPlayers: %v", err)
	}
	if err := r.ReportPlayableSlots("pod-uid-1", 12); err != nil {
		t.Fatalf("ReportPlayableSlots: %v", err)
	}
	if got := r.Lookup("pod-uid-1"); got.PlayableSlots != 12 || got.Players != 14 {
		t.Errorf("snapshot = %+v, want 14 players against 12 playable seats kept", got)
	}
}

func TestANegativePlayableFigureIsRefusedAndTheLastOneStands(t *testing.T) {
	r, _ := newTestRegistry()
	r.Connect("pod-uid-1", RoleServer)
	if err := r.ReportPlayableSlots("pod-uid-1", 12); err != nil {
		t.Fatalf("ReportPlayableSlots: %v", err)
	}
	if err := r.ReportPlayableSlots("pod-uid-1", -1); err == nil {
		t.Fatal("a negative playable figure was accepted")
	}
	if got := r.Lookup("pod-uid-1").PlayableSlots; got != 12 {
		t.Errorf("PlayableSlots = %d, want the previous 12", got)
	}
}

func TestPlayableSlotsNeedsALiveStream(t *testing.T) {
	r, _ := newTestRegistry()
	if err := r.ReportPlayableSlots("nobody", 12); err == nil {
		t.Fatal("a report for an unknown pod was accepted")
	}
}
```

Append to `internal/agentserver/server_envtest_test.go`:

```go
func TestThePlayableFigureReachesTheRegistry(t *testing.T) {
	f := newServerFixture(t)
	pod := f.pod("lobby-play")
	stream, closeConn := dialAgent(t, f.ctx, f.addr, f.ca, f.token(podspec.ServerServiceAccountName, []string{podspec.AgentTokenAudience}, pod))
	defer closeConn()

	mustSend(t, stream, hello(true))
	mustSend(t, stream, &agentpb.ServerMessage{Message: &agentpb.ServerMessage_PlayerCount{
		PlayerCount: &agentpb.PlayerCount{Players: 14, Slots: 100, PlayableSlots: 12},
	}})

	waitFor(t, func() bool { return f.agents.Lookup(string(pod.UID)).PlayableSlots == 12 })
	if got := f.agents.Lookup(string(pod.UID)).Players; got != 14 {
		t.Errorf("Players = %d, want 14: players beyond the playable seats are legitimate", got)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `ndev go test ./internal/agent/ ./internal/agentserver/ -run 'Playable' -count=1`
Expected: FAIL to compile — `r.ReportPlayableSlots undefined`, `PlayableSlots` not a field of `Snapshot`.

- [ ] **Step 3: Implement**

In `Snapshot`, after `Slots int32`:

```go
	// PlayableSlots is the last figure the plugin set, 0 if it set none.
	PlayableSlots int32
```

In `entry`, after `slots int32`:

```go
	playable       int32
```

After `ReportTicks`:

```go
// ReportPlayableSlots records the plugin's playable figure from the same
// report as the player count.
func (r *Registry) ReportPlayableSlots(key string, playable int32) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.entries[key]
	if !ok || !e.connected {
		return fmt.Errorf("no live stream for %q", key)
	}
	if playable < 0 {
		return fmt.Errorf("negative playable slots for %q: %d", key, playable)
	}
	e.playable = playable
	return nil
}
```

In `Lookup`'s `snap := Snapshot{...}` literal, after `Slots: e.slots,`:

```go
		PlayableSlots:     e.playable,
```

In `internal/agentserver/server.go`, extend the `ServerMessage_PlayerCount`
chain after the `ReportTicks` branch:

```go
		} else if err := s.opts.Agents.ReportPlayableSlots(id.PodUID,
			m.PlayerCount.GetPlayableSlots()); err != nil {
			RejectedReports.WithLabelValues(string(agent.RoleServer)).Inc()
			logger.V(1).Info("discarded a playable figure", "reason", err.Error())
		}
```

- [ ] **Step 4: Run to verify they pass**

Run: `ndev go test ./internal/agent/ ./internal/agentserver/ -count=1`
Expected: PASS, all existing tests included.

- [ ] **Step 5: Commit**

```bash
git add internal/agent internal/agentserver/server.go internal/agentserver/server_envtest_test.go
git commit -m "feat(agent): the registry keeps a server's playable figure

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FyT72YgbxTAVLjWBWZWWbk"
```

---

### Task 4: Free seats and capacity count playable seats

**Files:**
- Modify: `internal/controller/candidates.go` (`ServerView` ~line 28, after `clampReport` ~line 252, `AggregateGroup` ~line 548)
- Modify: `internal/controller/scaling.go` (`ScalingInputs` ~line 58, `provisionalCapacity` ~line 262-270, `readyContribution` ~line 330-337, `decideSize` ~line 648-665)
- Test: `internal/controller/candidates_test.go`, `internal/controller/scaling_test.go`

**Interfaces:**
- Produces:
  - `func playableSeats(reported int32, spec *int32, slots int32) int32`
  - `ServerView.Playable int32` (0 reads as `Slots`)
  - `func (v ServerView) freeSeats() int32`
  - `ScalingInputs.PlayableSlots int32` (0 = none) and `func (in ScalingInputs) capacity() int32`

- [ ] **Step 1: Write the failing tests**

Append to `internal/controller/candidates_test.go` (add `"k8s.io/utils/ptr"` to its imports if absent):

```go
func TestPlayableSeatsResolution(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reported int32
		spec     *int32
		slots    int32
		want     int32
	}{
		{"the plugin wins", 8, ptr.To[int32](12), 100, 8},
		{"the spec when the plugin said nothing", 0, ptr.To[int32](12), 100, 12},
		{"slots when neither did", 0, nil, 100, 100},
		{"a plugin figure above the limit is the limit", 500, nil, 100, 100},
		{"a spec figure above a lowered report is the report", 0, ptr.To[int32](12), 10, 10},
		{"nothing reported at all", 0, ptr.To[int32](12), 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := playableSeats(tc.reported, tc.spec, tc.slots); got != tc.want {
				t.Errorf("playableSeats(%d, %v, %d) = %d, want %d",
					tc.reported, ptr.Deref(tc.spec, -1), tc.slots, got, tc.want)
			}
		})
	}
}

func TestFreeSeatsNeverGoNegative(t *testing.T) {
	v := ServerView{Players: 14, Slots: 100, Playable: 12}
	if got := v.freeSeats(); got != 0 {
		t.Errorf("freeSeats = %d, want 0 with spectators beyond the playable seats", got)
	}
	v.Players = 9
	if got := v.freeSeats(); got != 3 {
		t.Errorf("freeSeats = %d, want 3", got)
	}
	v.Playable = 0
	if got := v.freeSeats(); got != 91 {
		t.Errorf("freeSeats = %d, want 91: no playable figure means every seat", got)
	}
}

func TestAggregateGroupCountsPlayableSeats(t *testing.T) {
	views := []ServerView{
		{Name: "a", Phase: phase.Ready, Registered: true, Slots: 100, Playable: 12, Players: 9},
		{Name: "b", Phase: phase.Ready, Registered: true, Slots: 100, Playable: 12, Players: 14},
	}
	got := AggregateGroup(views, "")
	if got.FreeSlots != 3 {
		t.Errorf("FreeSlots = %d, want 3 playable seats", got.FreeSlots)
	}
	if got.OnlinePlayers != 23 {
		t.Errorf("OnlinePlayers = %d, want all 23 including the spectators", got.OnlinePlayers)
	}
}
```

Append to `internal/controller/scaling_test.go`:

```go
func TestAFullLobbyWithHeadroomOrdersTheNextServer(t *testing.T) {
	lobby := ready("duels-a", 12, 100)
	lobby.Playable = 12
	got := DecideSize(ScalingInputs{
		Views:       []ServerView{lobby},
		MinReplicas: 1, MaxReplicas: 4,
		SpareSlots: 1, MaxPlayers: 100, PlayableSlots: 12,
	})
	if got.Create != 1 {
		t.Errorf("Create = %d, want 1: a lobby full at its playable seats has no room", got.Create)
	}
}

func TestWithoutPlayableSlotsAFullLobbyStillReadsAsRoom(t *testing.T) {
	got := DecideSize(ScalingInputs{
		Views:       []ServerView{ready("duels-a", 12, 100)},
		MinReplicas: 1, MaxReplicas: 4,
		SpareSlots: 1, MaxPlayers: 100,
	})
	if got.Create != 0 {
		t.Errorf("Create = %d, want 0: without the field 88 seats are free, as before", got.Create)
	}
}

func TestTheCapacityUnitIsThePlayableFigure(t *testing.T) {
	got := DecideSize(ScalingInputs{
		MinReplicas: 0, MaxReplicas: 10,
		SpareSlots: 24, MaxPlayers: 100, PlayableSlots: 12,
	})
	if got.Create != 2 {
		t.Errorf("Create = %d, want 2 servers of 12 playable seats for 24 spare", got.Create)
	}
}

func TestAPendingCreateCountsItsPlayableSeats(t *testing.T) {
	got := DecideSize(ScalingInputs{
		MinReplicas: 0, MaxReplicas: 10,
		SpareSlots: 24, MaxPlayers: 100, PlayableSlots: 12,
		PendingCreates: 1,
	})
	if got.Create != 1 {
		t.Errorf("Create = %d, want 1 more: the pending one brings 12, not 100", got.Create)
	}
}

func TestProvisionalCapacityCreditsAStartingServerItsPlayableSeats(t *testing.T) {
	if got := provisionalCapacity(starting("a"), 12); got != 12 {
		t.Errorf("provisionalCapacity = %d, want the group's 12", got)
	}
	full := ready("b", 14, 100)
	full.Playable = 12
	if got := provisionalCapacity(full, 12); got != 0 {
		t.Errorf("provisionalCapacity = %d, want 0 for a server past its playable seats", got)
	}
}

func TestReadyContributionCountsPlayableSeats(t *testing.T) {
	v := ready("a", 9, 100)
	v.Playable = 12
	if got := readyContribution(v); got != 3 {
		t.Errorf("readyContribution = %d, want 3", got)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `ndev go test ./internal/controller/ -run 'Playable|FreeSeats|FullLobby|CapacityUnit|PendingCreateCounts|ReadyContributionCounts' -count=1`
Expected: FAIL to compile — `playableSeats`, `ServerView.Playable`, `freeSeats`, `ScalingInputs.PlayableSlots` undefined.

- [ ] **Step 3: Implement**

In `ServerView`, after `Slots int32`:

```go
	// Playable is how many of Slots count as capacity; 0 reads as Slots.
	Playable int32
```

After `clampReport` in `candidates.go`:

```go
// playableSeats resolves how many of a server's slots count as capacity: the
// plugin's figure, else the group's, else all of them, and never more than
// the slots it reported.
func playableSeats(reported int32, spec *int32, slots int32) int32 {
	playable := slots
	switch {
	case reported > 0:
		playable = reported
	case spec != nil:
		playable = *spec
	}
	if playable > slots {
		playable = slots
	}
	if playable < 1 && slots > 0 {
		playable = 1
	}
	return playable
}

func (v ServerView) freeSeats() int32 {
	playable := v.Playable
	if playable <= 0 || playable > v.Slots {
		playable = v.Slots
	}
	if free := playable - v.Players; free > 0 {
		return free
	}
	return 0
}
```

In `AggregateGroup`, replace

```go
			free := v.Slots - v.Players
			if free > 0 {
				t.FreeSlots += free
			}
```

with

```go
			t.FreeSlots += v.freeSeats()
```

In `ScalingInputs`, after `MaxPlayers int32`:

```go
	// PlayableSlots is spec.playableSlots, 0 when unset.
	PlayableSlots int32
```

After `func (in ScalingInputs) floor() int32`:

```go
// capacity is what one server brings before it has reported anything.
func (in ScalingInputs) capacity() int32 {
	if in.PlayableSlots > 0 {
		return in.PlayableSlots
	}
	return in.MaxPlayers
}
```

In `provisionalCapacity`, rename the parameter `maxPlayers` to `capacity`
(both the signature and the `return maxPlayers` in the `v.Slots == 0` branch),
and replace

```go
	if free := v.Slots - v.Players; free > 0 {
		return free
	}
	return 0
}
```

(the tail of `provisionalCapacity`) with

```go
	return v.freeSeats()
}
```

In `readyContribution`, replace the same three-line tail with
`return v.freeSeats()`.

In `decideSize`, replace

```go
	provisional := in.PendingCreates * in.MaxPlayers
```

with

```go
	capacity := in.capacity()
	provisional := in.PendingCreates * capacity
```

replace `provisionalCapacity(v, in.MaxPlayers)` with `provisionalCapacity(v, capacity)`, and replace

```go
	if in.MaxPlayers > 0 && provisional < in.SpareSlots {
		gap := in.SpareSlots - provisional
		wanted = (gap + in.MaxPlayers - 1) / in.MaxPlayers
	}
```

with

```go
	if capacity > 0 && provisional < in.SpareSlots {
		gap := in.SpareSlots - provisional
		wanted = (gap + capacity - 1) / capacity
	}
```

- [ ] **Step 4: Run to verify they pass, and that nothing else moved**

Run: `ndev go test ./internal/controller/ -count=1`
Expected: PASS, every existing test untouched and green.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/candidates.go internal/controller/scaling.go internal/controller/candidates_test.go internal/controller/scaling_test.go
git commit -m "feat(controller): free seats and capacity count playable seats

A server's free seats are its playable seats minus its players, never
negative, and a group sizes itself in units of spec.playableSlots.
Without either figure every seat counts, as before.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FyT72YgbxTAVLjWBWZWWbk"
```

---

### Task 5: The controllers resolve and mirror the figure

**Files:**
- Modify: `internal/controller/servergroup_controller.go` (`collectViews` view literal ~line 1416; `ScalingInputs` literal ~line 943)
- Modify: `internal/controller/server_controller.go` (`mirrorPlayerCount` ~line 1085 and its caller ~line 1050)
- Test: `internal/controller/server_controller_test.go` (update the two `mirrorPlayerCount` calls ~line 3107/3117, add a test)
- Test: `internal/controller/servergroup_controller_test.go`

**Interfaces:**
- Consumes: `Snapshot.PlayableSlots` (Task 3), `playableSeats`, `ServerView.Playable`, `ScalingInputs.PlayableSlots` (Task 4), `ServerGroupSpec.PlayableSlots`, `ServerStatus.PlayableSlots` (Task 1).
- Produces: `func (r *ServerReconciler) mirrorPlayerCount(srv *spawneryv1alpha1.Server, snap agent.Snapshot, maxPlayers int32, specPlayable *int32, now metav1.Time)`.

- [ ] **Step 1: Write the failing tests**

In `internal/controller/server_controller_test.go`, change the two existing
calls in `TestTheMirroredCountIsClampedToTheGroupsCapacity` to pass `nil`
before `metav1.NewTime(...)`:

```go
	r.mirrorPlayerCount(srv, agent.Snapshot{Known: true, Players: 0, Slots: 1 << 30}, 80, nil,
		metav1.NewTime(time.Now()))
```

```go
	r.mirrorPlayerCount(gone, agent.Snapshot{Known: true, Players: 15, Slots: 20}, 0, nil,
		metav1.NewTime(time.Now()))
```

and append:

```go
func TestTheMirroredCountCarriesThePlayableFigure(t *testing.T) {
	r := &ServerReconciler{PlayerStatusInterval: time.Minute}
	srv := &spawneryv1alpha1.Server{}
	spec := ptr.To[int32](12)

	r.mirrorPlayerCount(srv, agent.Snapshot{Known: true, Players: 14, Slots: 100}, 100, spec,
		metav1.NewTime(time.Now()))
	if srv.Status.PlayableSlots != 12 || srv.Status.Players != 14 {
		t.Errorf("status = %d players, %d playable; want 14 and the spec's 12",
			srv.Status.Players, srv.Status.PlayableSlots)
	}

	r.mirrorPlayerCount(srv, agent.Snapshot{Known: true, Players: 14, Slots: 100, PlayableSlots: 8}, 100, spec,
		metav1.NewTime(time.Now()))
	if srv.Status.PlayableSlots != 8 {
		t.Errorf("playable = %d, want the plugin's 8 mirrored at once", srv.Status.PlayableSlots)
	}

	gone := &spawneryv1alpha1.Server{}
	r.mirrorPlayerCount(gone, agent.Snapshot{Known: true, Players: 15, Slots: 20}, 0, nil,
		metav1.NewTime(time.Now()))
	if gone.Status.PlayableSlots != 20 {
		t.Errorf("playable = %d without a group, want the report's 20", gone.Status.PlayableSlots)
	}
}
```

(add `"k8s.io/utils/ptr"` to the test file's imports if absent).

In `internal/controller/servergroup_controller_test.go`, append:

```go
func TestCollectViewsResolvesThePlayableFigure(t *testing.T) {
	f := newFixture(t)
	f.group.Spec.PlayableSlots = ptr.To[int32](20)
	if err := f.c.Update(f.ctx, f.group); err != nil {
		t.Fatalf("update group: %v", err)
	}
	f.createServer("lobby-play")
	f.reconcile("lobby-play")
	pod, ok := f.pod("lobby-play")
	if !ok {
		t.Fatal("no pod for lobby-play")
	}
	f.agents.Connect(string(pod.UID), agentRoleServer())
	if err := f.agents.ReportPlayers(string(pod.UID), 14, 100); err != nil {
		t.Fatalf("ReportPlayers: %v", err)
	}
	r := groupReconciler(f)

	views, _, err := r.collectViews(f.ctx, f.group)
	if err != nil || len(views) != 1 {
		t.Fatalf("collectViews = %v, %v; want one view", views, err)
	}
	if views[0].Playable != 20 {
		t.Errorf("Playable = %d, want the spec's 20", views[0].Playable)
	}

	if err := f.agents.ReportPlayableSlots(string(pod.UID), 12); err != nil {
		t.Fatalf("ReportPlayableSlots: %v", err)
	}
	views, _, _ = r.collectViews(f.ctx, f.group)
	if views[0].Playable != 12 {
		t.Errorf("Playable = %d, want the plugin's 12", views[0].Playable)
	}
}
```

(add `"k8s.io/utils/ptr"` to its imports if absent).

- [ ] **Step 2: Run to verify they fail**

Run: `ndev go test ./internal/controller/ -run 'Mirrored|CollectViewsResolves' -count=1`
Expected: FAIL to compile — too many arguments to `mirrorPlayerCount`.

- [ ] **Step 3: Implement**

`mirrorPlayerCount` becomes:

```go
func (r *ServerReconciler) mirrorPlayerCount(
	srv *spawneryv1alpha1.Server,
	snap agent.Snapshot,
	maxPlayers int32,
	specPlayable *int32,
	now metav1.Time,
) {
	if !snap.Known {
		return
	}
	// Clamped like the scaler's view, because the status is not only read by
	// people: netstate carries it into every agent's picture and the connect
	// router picks a group's target by playable seats minus players. Zero is
	// the fallback group standing in for one that is gone; it carries no
	// capacity, so there is nothing to clamp to and the report stands.
	players, slots := snap.Players, snap.Slots
	playable := slots
	if maxPlayers > 0 {
		players, slots = clampReport(players, slots, maxPlayers)
		playable = playableSeats(snap.PlayableSlots, specPlayable, slots)
	}
	significant := players != srv.Status.Players || slots != srv.Status.Slots ||
		playable != srv.Status.PlayableSlots
	overdue := srv.Status.PlayersUpdatedAt == nil ||
		now.Sub(srv.Status.PlayersUpdatedAt.Time) >= r.PlayerStatusInterval
	if !significant && !overdue {
		return
	}
	srv.Status.Players = players
	srv.Status.Slots = slots
	srv.Status.PlayableSlots = playable
	srv.Status.PlayersUpdatedAt = &now
}
```

Its caller:

```go
	r.mirrorPlayerCount(srv, snap, group.Spec.MaxPlayers, group.Spec.PlayableSlots, now)
```

In `collectViews`' `v := ServerView{...}` literal, after `Slots: slots,`:

```go
			Playable: playableSeats(snap.PlayableSlots, group.Spec.PlayableSlots, slots),
```

In the `DecideSize(ScalingInputs{...})` literal, after `MaxPlayers: group.Spec.MaxPlayers,`:

```go
				PlayableSlots: playableSpec(group),
```

and add next to `collectViews`:

```go
func playableSpec(group *spawneryv1alpha1.ServerGroup) int32 {
	if group.Spec.PlayableSlots == nil {
		return 0
	}
	return *group.Spec.PlayableSlots
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `ndev go test ./internal/controller/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/controller
git commit -m "feat(controller): resolve the playable figure and mirror it on the Server

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FyT72YgbxTAVLjWBWZWWbk"
```

---

### Task 6: The network picture, the status answer and connect

**Files:**
- Modify: `internal/netstate/netstate.go` (`ServerState` literal ~line 222)
- Modify: `internal/netstatus/status.go` (`server` ~line 206, `proxy` ~line 227)
- Modify: `internal/agentserver/requests.go` (`resolveTarget` ~line 493-500)
- Test: `internal/netstate/netstate_test.go`, `internal/netstatus/status_test.go`, `internal/agentserver/requests_test.go`

**Interfaces:**
- Consumes: `ServerStatus.PlayableSlots` (Task 1), proto fields (Task 2).

- [ ] **Step 1: Write the failing tests**

Append to `internal/agentserver/requests_test.go`:

```go
func TestAGroupTargetPicksByPlayableSeats(t *testing.T) {
	state := networkWith(nil, []*agentpb.ServerState{
		{Name: "duels-a", Group: "duels", Players: 12, Slots: 100, PlayableSlots: 12, Registered: true},
		{Name: "duels-b", Group: "duels", Players: 20, Slots: 100, PlayableSlots: 24, Registered: true},
	})
	got, ok := resolveTarget(state, &agentpb.ConnectRequest{
		Target: &agentpb.ConnectRequest_Group{Group: "duels"},
	})
	if !ok || got != "duels-b" {
		t.Errorf("target = %q ok=%v, want duels-b: duels-a is full at its playable seats", got, ok)
	}
}

func TestAPlayableFigureOfZeroReadsAsEverySeat(t *testing.T) {
	state := networkWith(nil, []*agentpb.ServerState{
		{Name: "lobby-a", Group: "lobby", Players: 90, Slots: 100, PlayableSlots: 12, Registered: true},
		{Name: "lobby-b", Group: "lobby", Players: 50, Slots: 100, Registered: true},
	})
	got, ok := resolveTarget(state, &agentpb.ConnectRequest{
		Target: &agentpb.ConnectRequest_Group{Group: "lobby"},
	})
	if !ok || got != "lobby-b" {
		t.Errorf("target = %q ok=%v, want lobby-b: an older status's 0 is all 100 seats", got, ok)
	}
}

func TestAGroupWhoseServersAreAllFullStillResolves(t *testing.T) {
	state := networkWith(nil, []*agentpb.ServerState{
		{Name: "duels-a", Group: "duels", Players: 14, Slots: 100, PlayableSlots: 12, Registered: true},
	})
	if got, ok := resolveTarget(state, &agentpb.ConnectRequest{
		Target: &agentpb.ConnectRequest_Group{Group: "duels"},
	}); !ok || got != "duels-a" {
		t.Errorf("target = %q ok=%v, want duels-a: full is not unroutable", got, ok)
	}
}
```

Append to `internal/netstate/netstate_test.go`:

```go
func TestBuildCarriesThePlayableFigure(t *testing.T) {
	srv := readyServer("ns", "duels-a", "lobby", 14, 100)
	srv.Status.PlayableSlots = 12
	src, _ := source(t, ephemeralGroup("ns", "lobby"), srv)

	got, err := src.Build(context.Background(), "ns", netstate.ForProxies)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n := got.GetServers()[0].GetPlayableSlots(); n != 12 {
		t.Errorf("playable_slots = %d, want 12", n)
	}
}
```

In `internal/netstatus/status_test.go`'s `network` fixture, after
`lobbyB.Spec.Retire = true` add `lobbyB.Status.PlayableSlots = 12`, and append:

```go
func TestStatusCarriesThePlayableFigure(t *testing.T) {
	res, err := status(t, allMeasured(), netstate.ForProxies, "lobby-b")
	if err != nil {
		t.Fatal(err)
	}
	if n := res.GetInstances()[0].GetPlayableSlots(); n != 12 {
		t.Errorf("playable_slots = %d, want 12", n)
	}
	res, err = status(t, allMeasured(), netstate.ForProxies, "gateway-a")
	if err != nil {
		t.Fatal(err)
	}
	gw := res.GetInstances()[0]
	if gw.GetPlayableSlots() != gw.GetSlots() {
		t.Errorf("a proxy's playable = %d, want its slots %d", gw.GetPlayableSlots(), gw.GetSlots())
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `ndev go test ./internal/agentserver/ ./internal/netstate/ ./internal/netstatus/ -run 'Playable|AllFull' -count=1`
Expected: `TestAGroupTargetPicksByPlayableSeats`, `TestBuildCarriesThePlayableFigure` and `TestStatusCarriesThePlayableFigure` FAIL; `TestAPlayableFigureOfZeroReadsAsEverySeat` and `TestAGroupWhoseServersAreAllFullStillResolves` already PASS (they pin the fallbacks).

- [ ] **Step 3: Implement**

In `netstate.go`'s `&agentpb.ServerState{...}`, after `Slots: srv.Status.Slots,`:

```go
			PlayableSlots: srv.Status.PlayableSlots,
```

In `netstatus/status.go`, `server()`: change the `Players:` line to

```go
		Players: srv.Status.Players, Slots: srv.Status.Slots, PlayableSlots: srv.Status.PlayableSlots,
		Tps: tps, Mspt: mspt,
```

and in `proxy()` change `Players: snap.Players, Slots: snap.Slots,` to

```go
		Players: snap.Players, Slots: snap.Slots, PlayableSlots: snap.Slots,
```

In `resolveTarget`, replace

```go
			if free := int(srv.GetSlots() - srv.GetPlayers()); free > bestFree {
```

with

```go
			if free := playableFree(srv); free > bestFree {
```

and add below `resolveTarget`:

```go
func playableFree(srv *agentpb.ServerState) int {
	playable := srv.GetPlayableSlots()
	if playable <= 0 || playable > srv.GetSlots() {
		playable = srv.GetSlots()
	}
	return max(0, int(playable-srv.GetPlayers()))
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `ndev go test ./internal/agentserver/ ./internal/netstate/ ./internal/netstatus/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/netstate internal/netstatus internal/agentserver/requests.go internal/agentserver/requests_test.go
git commit -m "feat(agentserver): connect picks by playable seats; the picture carries them

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FyT72YgbxTAVLjWBWZWWbk"
```

---

### Task 7: Java API and the agents

**Files:**
- Modify: `agent/api/src/main/java/cloud/spawnery/agent/api/SpawneryApi.java` (new method after `endRound`, ~line 318)
- Modify: `agent/api/src/main/java/cloud/spawnery/agent/api/ServerInfo.java`
- Modify: `agent/api/src/main/java/cloud/spawnery/agent/api/InstanceStatus.java`
- Modify: `agent/api/src/test/java/cloud/spawnery/agent/api/FakeApi.java`
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/MirrorApi.kt`
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/NetworkMirror.kt` (~line 72-92)
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/StatusConversion.kt` (~line 21-24)
- Modify: `agent/paper/src/main/kotlin/cloud/spawnery/agent/paper/ServerState.kt`
- Modify: `agent/paper/src/main/kotlin/cloud/spawnery/agent/paper/ServerRole.kt` (`playerCount()` ~line 55)
- Modify: `agent/paper/src/main/kotlin/cloud/spawnery/agent/paper/AgentPlugin.kt` (~line 116)
- Test: `agent/api/src/test/java/cloud/spawnery/agent/api/RecordCompatibilityTest.java`
- Test: `agent/common/src/test/kotlin/cloud/spawnery/agent/MirrorApiTest.kt`, `NetworkMirrorTest.kt`
- Test: `agent/paper/src/test/kotlin/cloud/spawnery/agent/paper/ServerRoleTest.kt`

**Interfaces:**
- Consumes: Java proto accessors (Task 2).
- Produces: `void SpawneryApi.playableSlots(int slots)`; `int ServerInfo.playableSlots()`; `int InstanceStatus.playableSlots()`; `MirrorApi(..., readiness: ReadinessGate? = null, playable: ((Int) -> Unit)? = null)`; `ServerState.playable: Int`, `ServerState.setPlayable(n: Int)`.

- [ ] **Step 1: Write the failing tests**

Append to `RecordCompatibilityTest.java`:

```java
    @Test
    void theZeroTwelveConstructorsStillBuildAndReadEverySeatAsPlayable() {
        ServerInfo server = new ServerInfo("lobby-a", "lobby", ServerPhase.READY, 1, 20, true, "", Map.of(), "", 0, false, "node-1");
        assertEquals(20, server.playableSlots());
        InstanceStatus instance = new InstanceStatus("lobby-a", "lobby", false, "Ready", true, 1, 20,
                OptionalDouble.empty(), OptionalDouble.empty(), Duration.ZERO, false, false, false, NONE, "node-1");
        assertEquals(20, instance.playableSlots());
    }

    @Test
    void aPlayableFigureOutsideOneToSlotsReadsAsEverySeat() {
        assertEquals(20, new ServerInfo("a", "g", ServerPhase.READY, 1, 20, true, "", Map.of(), "", 0, false, "", 0).playableSlots());
        assertEquals(20, new ServerInfo("a", "g", ServerPhase.READY, 1, 20, true, "", Map.of(), "", 0, false, "", 50).playableSlots());
        assertEquals(12, new ServerInfo("a", "g", ServerPhase.READY, 1, 20, true, "", Map.of(), "", 0, false, "", 12).playableSlots());
    }
```

Append to `MirrorApiTest.kt`:

```kotlin
    @Test
    fun `playableSlots refuses on a proxy`() {
        val api = MirrorApi(NetworkMirror(), proxySelf(), connector(), CloudEvents())

        assertFailsWith<UnsupportedOperationException> { api.playableSlots(12) }
    }

    @Test
    fun `playableSlots refuses a negative figure and hands the rest to the server`() {
        val set = mutableListOf<Int>()
        val api = MirrorApi(NetworkMirror(), serverSelf(), connector(), CloudEvents(), playable = { set += it })

        assertFailsWith<IllegalArgumentException> { api.playableSlots(-1) }
        api.playableSlots(12)
        api.playableSlots(0)
        assertEquals(listOf(12, 0), set)
    }
```

Append to `NetworkMirrorTest.kt` (it already imports `NetworkState` and `ServerState` from `cloud.spawnery.agent.pb`; add any missing):

```kotlin
    @Test
    fun `a server's playable figure reaches ServerInfo`() {
        val mirror = NetworkMirror()
        mirror.apply(
            NetworkState.newBuilder().addServers(
                ServerState.newBuilder().setName("duels-a").setGroup("duels").setPhase("Ready")
                    .setPlayers(14).setSlots(100).setPlayableSlots(12),
            ).build(),
        )
        assertEquals(12, mirror.servers().single().playableSlots())
    }
```

Append to `ServerRoleTest.kt`:

```kotlin
    @Test
    fun `the report carries the plugin's playable figure, and zero once it is taken back`() {
        val state = ServerState()
        val role = ServerRole(state, NetworkMirror(), dormantConnector(), aFeed(), CloudEvents())
        state.sample(players = 14, slots = 100)

        assertEquals(0, role.playerCount().playerCount.playableSlots)
        state.setPlayable(12)
        assertEquals(12, role.playerCount().playerCount.playableSlots)
        state.setPlayable(0)
        assertEquals(0, role.playerCount().playerCount.playableSlots)
    }
```

- [ ] **Step 2: Run to verify they fail**

Run: `git add -A agent && ndev make agent`
Expected: FAIL — compile errors: `playableSlots()` not in `ServerInfo`/`InstanceStatus`, no 13-argument constructor, `api.playableSlots` unresolved, no parameter `playable`, `setPlayable` unresolved.

- [ ] **Step 3: Implement the API**

`SpawneryApi.java`, after `endRound()`:

```java
    /**
     * Sets how many of this server's seats count as capacity, from now until
     * changed: what its group's spare slots, free slots and a connect to the
     * group measure. Players beyond it are still admitted up to the server's
     * limit, and make the server full rather than overfull.
     *
     * <p>It asks the operator nothing. The next periodic report carries it,
     * and every report after that, so a new session restates it without a
     * second call.
     *
     * <p>Servers only; a proxy throws {@link UnsupportedOperationException}.
     *
     * @param slots the playable seats; 0 hands the decision back to the
     *     group's {@code spec.playableSlots}. A figure above the server's
     *     slots counts as its slots.
     * @throws IllegalArgumentException if {@code slots} is negative
     */
    void playableSlots(int slots);
```

`ServerInfo.java`: add `int playableSlots` as the last record component
(after `String node`); document it in the class javadoc after `@param node`:

```java
 * @param playableSlots how many of {@code slots} count as capacity. Equal to
 *     {@code slots} when nothing narrowed it, and for a report from an
 *     operator older than this field.
```

In the compact constructor, after the `node` line:

```java
        if (playableSlots <= 0 || playableSlots > slots) {
            playableSlots = slots;
        }
```

and add, above the existing "before {@code node}" constructor:

```java
    /** The record as it was before {@code playableSlots}, which it reads as every seat. */
    public ServerInfo(
            String name,
            String group,
            ServerPhase phase,
            int players,
            int slots,
            boolean registered,
            String state,
            Map<String, String> attributes,
            String incarnation,
            int number,
            boolean held,
            String node) {
        this(name, group, phase, players, slots, registered, state, attributes, incarnation, number, held, node, slots);
    }
```

Change the two older constructors' `this(...)` calls to end in `…, held, "", slots)` and `…, false, "", slots)`.

`InstanceStatus.java`: add `int playableSlots` as the last component, the same
normalisation in the compact constructor, the sentence
"{@code playableSlots} is how many of {@code slots} count as capacity, equal to
{@code slots} when nothing narrowed it." at the end of the class javadoc, and:

```java
    /** The record as it was before {@code playableSlots}, which it reads as every seat. */
    public InstanceStatus(String name, String group, boolean proxy, String phase, boolean ready,
                          int players, int slots, OptionalDouble tps, OptionalDouble mspt, Duration age,
                          boolean retiring, boolean held, boolean draining, ResourceUsage usage,
                          String node) {
        this(name, group, proxy, phase, ready, players, slots, tps, mspt, age, retiring, held, draining, usage, node, slots);
    }
```

with the existing "before {@code node}" constructor's call changed to end in `…, usage, "", slots)`.

`FakeApi.java`, after `endRound()`:

```java
    final List<Integer> playable = new ArrayList<>();

    @Override
    public void playableSlots(int slots) {
        playable.add(slots);
    }
```

(import `java.util.ArrayList`/`java.util.List` if absent).

- [ ] **Step 4: Implement the agents**

`MirrorApi.kt`: add the constructor parameter after `readiness`:

```kotlin
    /**
     * Where a server keeps its playable figure; null on a proxy. Last and
     * defaulted, as ever.
     */
    private val playable: ((Int) -> Unit)? = null,
```

and the method after `holdReadiness`:

```kotlin
    override fun playableSlots(slots: Int) {
        val sink = playable ?: throw UnsupportedOperationException(
            "this is a proxy; a proxy has no seats a group is sized by",
        )
        require(slots >= 0) { "playable slots must not be negative, got $slots" }
        sink(slots)
    }
```

`NetworkMirror.kt`: in the `ServerInfo(...)` call add `it.playableSlots,` after `it.node,`.

`StatusConversion.kt`: change the `InstanceStatus(...)` call's last line to
`i.retiring, i.held, i.draining, usage(i.usage), i.node, i.playableSlots,`.

`paper/ServerState.kt`, after `slotCount`:

```kotlin
    private val playableCount = AtomicInteger(0)
    val playable: Int get() = playableCount.get()

    fun setPlayable(slots: Int) {
        playableCount.set(slots)
    }
```

`paper/ServerRole.kt` `playerCount()`: add `.setPlayableSlots(state.playable)` after `.setMspt(state.mspt)`.

`paper/AgentPlugin.kt`: change

```kotlin
                val api = MirrorApi(mirror, self, connector, events, readiness)
```

to

```kotlin
                val api = MirrorApi(mirror, self, connector, events, readiness, state::setPlayable)
```

The Velocity agent keeps its `MirrorApi` call unchanged: no `playable`, so it throws.

- [ ] **Step 5: Run to verify they pass**

Run: `git add -A agent && ndev make agent`
Expected: build succeeds with every JUnit suite green.

- [ ] **Step 6: Commit**

```bash
git add agent
git commit -m "feat(agent): playableSlots in the plugin API, the report and the picture

A server's plugin sets its playable figure; the agent carries it on every
report. ServerInfo and InstanceStatus gain playableSlots(), each keeping
its previous constructor, which reads every seat as playable.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FyT72YgbxTAVLjWBWZWWbk"
```

---

### Task 8: `/cloud` shows both figures

**Files:**
- Create: `agent/common/src/main/kotlin/cloud/spawnery/agent/Seats.kt`
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/StatusLines.kt` (`memberLine` ~line 143, `instanceLines` ~line 164-169)
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/ListLines.kt` (`serverInfoLines` ~line 74-79, `groupInfoLines` members ~line 129)
- Test: `agent/common/src/test/kotlin/cloud/spawnery/agent/CloudCommandTest.kt`

**Interfaces:**
- Consumes: `ServerInfo.playableSlots()`, `InstanceStatus.playableSlots()` (Task 7).
- Produces: `internal fun seatsText(players: Int, playable: Int, slots: Int, spaced: Boolean): String`, `internal fun seatsFill(players: Int, playable: Int, slots: Int): Double`.

- [ ] **Step 1: Write the failing tests**

Append to `CloudCommandTest.kt`:

```kotlin
    @Test
    fun `info on a server shows playable seats beside the limit`() {
        run(
            "cloud info duels-a",
            api(
                NetworkState.newBuilder().addServers(
                    ServerState.newBuilder().setName("duels-a").setGroup("duels").setPhase("Ready")
                        .setPlayers(9).setSlots(100).setPlayableSlots(12).setRegistered(true),
                ).build(),
            ),
        )
        assertTrue(sent.any { plain(it).contains("Players") && plain(it).contains("9 / 12 · max 100") }, "$sent")
    }

    @Test
    fun `info on a group lists its servers with playable seats`() {
        run(
            "cloud info duels",
            api(
                NetworkState.newBuilder()
                    .addGroups(GroupState.newBuilder().setName("duels").setKind(GroupState.Kind.EPHEMERAL))
                    .addServers(
                        ServerState.newBuilder().setName("duels-a").setGroup("duels").setPhase("Ready")
                            .setPlayers(9).setSlots(100).setPlayableSlots(12).setRegistered(true),
                    ).build(),
            ),
        )
        assertTrue(sent.any { plain(it).contains("9/12 · max 100") }, "$sent")
    }

    @Test
    fun `status of a server with spectators shows a full bar and the limit`() {
        run("cloud status lobby-r", api(aNetworkWithProxies()))
        statusAnswer {
            metricsAvailable = true
            total = usage(400, 500, 1L shl 30, 2L shl 30, 1, 1).build()
            addInstances(
                cloud.spawnery.agent.pb.InstanceStatus.newBuilder().setName("lobby-r").setGroup("lobby")
                    .setPhase("Ready").setPlayers(14).setSlots(100).setPlayableSlots(12)
                    .setAgeSeconds(60).setUsage(usage(400, 500, 1L shl 30, 2L shl 30, 1, 1)),
            )
        }
        val players = sent.first { plain(it).contains("Players") && plain(it).contains("max") }
        assertTrue(plain(players).contains("14 / 12 · max 100"), players)
        assertTrue(players.contains("<dark_gray></dark_gray>"), "the bar is not full: $players")
    }
```

The existing `status of a server shows its node, bars and markers` test
(`3 / 20`, no playable figure) is the "equal" case and must stay green.

- [ ] **Step 2: Run to verify they fail**

Run: `git add -A agent && ndev make agent`
Expected: FAIL — the three new tests: the output reads `9 / 100`, `9/100`, `14 / 100`.

- [ ] **Step 3: Implement**

Create `Seats.kt`:

```kotlin
package cloud.spawnery.agent

internal fun seatsText(players: Int, playable: Int, slots: Int, spaced: Boolean): String {
    val of = if (spaced) " / " else "/"
    return if (playable in 1 until slots) "$players$of$playable · max $slots" else "$players$of$slots"
}

internal fun seatsFill(players: Int, playable: Int, slots: Int): Double {
    val capacity = if (playable in 1..slots) playable else slots
    return if (capacity > 0) (players.toDouble() / capacity).coerceAtMost(1.0) else 0.0
}
```

`StatusLines.kt` `memberLine`: replace `Style.number("${i.players()}/${i.slots()}"),` with

```kotlin
    Style.number(seatsText(i.players(), i.playableSlots(), i.slots(), spaced = false)),
```

`instanceLines`: replace the `val fill = …` line and the `Style.number("${i.players()} / ${i.slots()}")` in the following `Layout.field("Players", …)` with

```kotlin
    val fill = seatsFill(i.players(), i.playableSlots(), i.slots())
```

and

```kotlin
            Style.number(seatsText(i.players(), i.playableSlots(), i.slots(), spaced = true)),
```

`ListLines.kt` `serverInfoLines`: the same two replacements with `s.` instead of `i.`.
`groupInfoLines`: replace `Style.number("${it.players()}/${it.slots()}"),` with

```kotlin
                        Style.number(seatsText(it.players(), it.playableSlots(), it.slots(), spaced = false)),
```

- [ ] **Step 4: Run to verify they pass**

Run: `git add -A agent && ndev make agent`
Expected: build succeeds, every suite green.

- [ ] **Step 5: Commit**

```bash
git add agent
git commit -m "feat(agent): /cloud shows playable seats beside the limit

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FyT72YgbxTAVLjWBWZWWbk"
```

---

### Task 9: e2e on the tutorial's live network

**Files:**
- Modify: `test/e2e/tutorial_test.go` (new test after `TestTutorialPath`)
- Modify: `hack/e2e-tutorial.sh` (the `go test` line, ~line 117)

**Interfaces:**
- Consumes: `ServerGroupSpec.PlayableSlots` (Task 1) and the operator behaviour of Tasks 3-6. The tutorial's published images predate `playable_slots`, so this exercises the spec path, which is the one an agent older than this release takes.

- [ ] **Step 1: Write the test**

Append to `test/e2e/tutorial_test.go`:

```go
// TestTutorialPlayableSlots runs after TestTutorialPath on the same network:
// with one playable seat per server and two kept spare, one held join leaves
// one server full, and the group builds a third.
func TestTutorialPlayableSlots(t *testing.T) {
	if os.Getenv("SPAWNERY_E2E_TUTORIAL") != "1" {
		t.Skip("set SPAWNERY_E2E_TUTORIAL=1; hack/e2e-tutorial.sh does this nightly")
	}

	key := client.ObjectKey{Namespace: tutorialNamespace, Name: tutorialServerGroup}
	var g spawneryv1alpha1.ServerGroup
	if err := k8s.Get(ctx, key, &g); err != nil {
		t.Fatalf("get ServerGroup: %v", err)
	}
	restore := g.DeepCopy()
	patch := client.MergeFrom(g.DeepCopy())
	g.Spec.PlayableSlots = ptr.To[int32](1)
	g.Spec.Scaling.SpareSlots = 2
	if err := k8s.Patch(ctx, &g, patch); err != nil {
		t.Fatalf("patch ServerGroup: %v", err)
	}
	t.Cleanup(func() {
		var now spawneryv1alpha1.ServerGroup
		if err := k8s.Get(ctx, key, &now); err != nil {
			return
		}
		back := client.MergeFrom(now.DeepCopy())
		now.Spec.PlayableSlots = nil
		now.Spec.Scaling.SpareSlots = restore.Spec.Scaling.SpareSlots
		_ = k8s.Patch(ctx, &now, back)
	})

	liveServers := func() int {
		var list spawneryv1alpha1.ServerList
		if err := k8s.List(ctx, &list, client.InNamespace(tutorialNamespace)); err != nil {
			return -1
		}
		n := 0
		for _, s := range list.Items {
			if s.Spec.GroupRef.Name == tutorialServerGroup && s.Status.Phase != string(phase.Failed) {
				n++
			}
		}
		return n
	}
	eventuallyIn(t, tutorialOperatorNamespace, 3*time.Minute, "two servers with one playable seat each", func() (bool, string) {
		n := liveServers()
		return n == 2, fmt.Sprintf("%d live Servers", n)
	})

	joinPath, err := exec.LookPath("spawnery-join")
	if err != nil {
		t.Fatalf("spawnery-join not on PATH (%v); run this through nix develop", err)
	}
	const hold = 90 * time.Second
	cmd := exec.Command(joinPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(tutorialJoinPort),
		"--timeout", (hold + 5*time.Second).String(),
		"--hold", hold.String(),
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start spawnery-join: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	eventuallyIn(t, tutorialOperatorNamespace, hold-10*time.Second, "a third server once one seat is taken", func() (bool, string) {
		n := liveServers()
		return n == 3, fmt.Sprintf("%d live Servers; join output so far: %s", n, out.String())
	})
}
```

Add to the file's imports: `"k8s.io/utils/ptr"` and `"github.com/spawnery/spawnery/internal/phase"`.

In `hack/e2e-tutorial.sh`, change `-run TestTutorialPath` to
`-run 'TestTutorialPath|TestTutorialPlayableSlots'`.

- [ ] **Step 2: Verify it compiles**

Run: `ndev go vet -tags e2e ./test/e2e/`
Expected: no output.

- [ ] **Step 3: Run it red against the operator without Tasks 3-6**

In a throwaway worktree at the commit before Task 3, with this test and the
script change cherry-picked:

```bash
git worktree add --detach /tmp/claude-1000/playable-red <Task-2-commit>
cd /tmp/claude-1000/playable-red && git cherry-pick <this-task's-commit-once-made> --no-commit
systemd-run --scope --user --property=Delegate=yes -- nix --extra-experimental-features 'nix-command flakes' develop /tmp/claude-1000/playable-red -c env KIND_EXPERIMENTAL_PROVIDER=podman make e2e-tutorial
```

Expected: `TestTutorialPlayableSlots` FAILS at "a third server once one seat is taken" (two Servers: without the operator change a server with one player still has 19 free seats). Remove the worktree afterwards: `git worktree remove --force /tmp/claude-1000/playable-red`.

- [ ] **Step 4: Run it green on this branch**

Run: `systemd-run --scope --user --property=Delegate=yes -- nix --extra-experimental-features 'nix-command flakes' develop /home/paul/git/spawnery-playable-slots -c env KIND_EXPERIMENTAL_PROVIDER=podman make e2e-tutorial`
Expected: `TestTutorialPath` and `TestTutorialPlayableSlots` PASS.

- [ ] **Step 5: Commit**

```bash
git add test/e2e/tutorial_test.go hack/e2e-tutorial.sh
git commit -m "test(e2e): a held join on a group with one playable seat builds a server

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FyT72YgbxTAVLjWBWZWWbk"
```

(Step 3 needs this commit's hash; make the commit first, then run Step 3, then Step 4.)

---

### Task 10: Docs, and the spec brought in line

**Files:**
- Modify: `docs/guides/scaling-and-boosts.md` (new section before `## The arithmetic, in the order the operator runs it`, and the capacity formula in that section)
- Modify: `docs/plugin-api/what-a-plugin-can-do.md` (new section after `## Closing this server to new players`)
- Modify: `docs/guides/cloud-command.md` (the paragraph "Every answer opens with a heading…", ~line 134)
- Modify: `docs/superpowers/specs/2026-09-29-playable-slots-design.md` (§6 exception, §9 e2e)

- [ ] **Step 1: scaling-and-boosts.md**

Insert before `## The arithmetic, in the order the operator runs it`:

````markdown
## Seats that count, and seats that do not

A round-based game often wants its servers to admit more players than a round
takes, so that spectators can still join a full round. `maxPlayers` is the
limit a server enforces; `playableSlots` is how many of those seats count as
capacity:

```yaml
spec:
  maxPlayers: 100
  playableSlots: 12
  scaling:
    minReplicas: 1
    maxReplicas: 8
    spareSlots: 12
```

A server's free seats are then `max(0, playable − players)`, where `playable`
is the figure its plugin set with `Spawnery.api().playableSlots(n)`, else
`spec.playableSlots`, else its slots — never more than its slots. A lobby with
twelve players is full, and `spareSlots` orders the next server while its
countdown runs, not after its round has started. Players beyond the twelve are
still let in up to `maxPlayers`, and make the server full rather than
overfull. `status.freeSlots`, `/cloud` and a connect to the group all count
the same seats.
````

In `## The arithmetic…`, change the formula block's first line to

```text
wanted = ceil((spareSlots - provisional) / capacity)   when provisional < spareSlots
```

and add after the block: "`capacity` is `playableSlots` if the group sets it, else `maxPlayers`: what one server brings before it has said anything."

- [ ] **Step 2: what-a-plugin-can-do.md**

Insert after the `## Closing this server to new players` section:

````markdown
## Saying how many seats count

`playableSlots(n)` tells the operator how many of this server's seats count as
capacity, for as long as you do not change it. `playableSlots(0)` hands the
decision back to the group's `spec.playableSlots`.

```java
Spawnery.api().playableSlots(12);
```

**It asks nothing.** The value rides on the agent's periodic report, so there
is no stage to wait for and nothing to fail on the network; it reaches the
operator within one report interval and is restated on every new session.

**Nobody is turned away by it.** Players beyond the playable seats are still
admitted up to the server's limit. The server then counts as full: its group
builds another one, and a connect to the group prefers a server with room.

A figure above the server's slots counts as its slots. A negative one throws
`IllegalArgumentException`; on a proxy the call throws
`UnsupportedOperationException`. `ServerInfo.playableSlots()` reads back what
the operator resolved for any server.
````

- [ ] **Step 3: cloud-command.md**

Replace "bars show players against slots," with "bars show players against playable seats (a server whose playable seats differ from its slots reads `9 / 12 · max 100`),".

- [ ] **Step 4: Spec amendments**

In the spec §6 replace "On a proxy it throws `IllegalStateException`: a proxy has no seats a group is sized by." with "On a proxy it throws `UnsupportedOperationException`, as `holdReadiness` does: a proxy has no seats a group is sized by."

In §9 replace the e2e bullet with: "e2e, on the nightly tutorial run (`hack/e2e-tutorial.sh`), the only one with live agents: the tutorial group patched to `playableSlots 1`, `spareSlots 2`; one `spawnery-join` bot held; a third server is created."

- [ ] **Step 5: Verify the docs linters**

Run: `ndev make test`
Expected: PASS (the docs linters are part of it).

- [ ] **Step 6: Commit**

```bash
git add docs
git commit -m "docs: playable seats in scaling, the plugin API and /cloud

The spec follows the code on two points: a proxy throws
UnsupportedOperationException as holdReadiness does, and the e2e case
runs on the tutorial network, the only one with live agents.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FyT72YgbxTAVLjWBWZWWbk"
```

---

### Task 11: 0.13.0

**Files:**
- Modify: `flake.nix` (`imageVersion` ~line 363, `operatorVersion` ~line 542, each with its comment paragraph)
- Modify: `charts/spawnery/Chart.yaml` (`version`, `appVersion` with its comment), `charts/spawnery/values.yaml` (`image.tag`)
- Modify: every image tag and install version in `README.md`, `agent/api/README.md`, `charts/spawnery/README.md`, `config/samples/*.yaml`, `docs/**/*.md`, `docs/tutorial/network.yaml`, `test/e2e/manifests/ondemand.yaml`
- Generated: `docs/reference/chart-values.md`

- [ ] **Step 1: Move the numbers**

`flake.nix`, above `imageVersion = "0.12.0";` add

```nix
          #
          # 0.13.0 moves it because the API and the agents changed:
          # SpawneryApi.playableSlots(int), carried on every report, and
          # ServerInfo and InstanceStatus gain playableSlots(), each keeping
          # its previous constructor. /cloud shows playable seats beside the
          # limit.
```

and set `imageVersion = "0.13.0";`. Above `operatorVersion = "0.11.0";` add

```nix
          #
          # 0.13.0 moves it with the chart and the images: spec.playableSlots
          # and a plugin's runtime figure decide the free seats the group
          # scales on, reports and routes a connect by. Nothing rolls.
```

and set `operatorVersion = "0.13.0";`.

`charts/spawnery/Chart.yaml`: `version: 0.13.0`; above `appVersion` add

```yaml
#
# 0.13.0 moves it with the operator and the images: ServerGroup gains
# playableSlots and Server.status playableSlots. Every existing object
# validates unchanged.
```

and set `appVersion: "0.13.0"`. `charts/spawnery/values.yaml`: `tag: "0.13.0"`.

- [ ] **Step 2: Move every tag and install version**

Run: `grep -rn --include='*.md' --include='*.yaml' -E '0\.1[12]\.0' README.md agent/api/README.md charts/spawnery/README.md config/samples docs test/e2e/manifests | grep -v 'docs/superpowers\|docs/archive'`
Change each image tag suffix `-0.12.0` to `-0.13.0`, each chart install `--version 0.11.0` to `0.13.0`, and the Maven coordinate in `agent/api/README.md` to `0.13.0`. Re-run the grep.
Expected: no matches left outside `docs/superpowers` and `docs/archive`.

- [ ] **Step 3: Regenerate and run everything**

Run: `ndev make manifests && ndev make test && git add -A && ndev make agent && ndev make lint`
Expected: `docs/reference/chart-values.md` changes to 0.13.0; all PASS.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "chore: 0.13.0, playable slots

All three numbers move. The operator: spec.playableSlots and a plugin's
runtime figure decide the free seats a group scales on, reports and
routes a connect by. The images: SpawneryApi.playableSlots(int), carried
on every report, and playableSlots() on ServerInfo and InstanceStatus.
The chart: ServerGroup and Server gain the fields; every existing object
validates unchanged.

A minor step by the rule of 2026-09-08: a new CRD field and a new API
method. Nothing rolls on upgrade.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FyT72YgbxTAVLjWBWZWWbk"
```

Tagging `v0.13.0` is not part of this plan; it follows a green CI on master after merge.
