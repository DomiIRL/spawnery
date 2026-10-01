# Changeover Stages Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Groups set `spec.changeoverStage`; a change reaching the whole network rolls lower stages first, a group releases later stages (and its budget place) once its new generation stands (`Deferred`), and proxy groups roll blue/green so that point is reached as soon as the new proxies are Ready.

**Architecture:** Extends the changeover budget of 0.6.0. Every group still decides for itself from the shared cache through the pure `AdmitChangeovers`; it gains a stage gate and a `Persistent` flag. The per-group state functions (`ownServerChangeover`, `ownProxyChangeover`, new `ownPersistentChangeover`) learn the wider `Deferred`. `DecideRollout` replaces every stale proxy up front and marks them all once `replicas` current pods are Ready.

**Tech Stack:** Go, controller-runtime, kubebuilder markers/CEL, envtest, kind e2e (tutorial suite with real images).

**Spec:** `docs/superpowers/specs/2026-10-01-changeover-stages-design.md`

## Global Constraints

- Every command runs in the dev shell: `nix --extra-experimental-features 'nix-command flakes' develop /home/paul/git/spawnery -c <cmd>` (no `cd` before it; this plan abbreviates it as `nd <cmd>`).
- Machine is `paul-desktop` (32 cores): full test runs in parallel are fine. Check with `hostname`; on the dev VM use `-p 1`.
- `changeoverStage`: `int32`, optional, default 0, lower first, negatives allowed; top level of `ServerGroupSpec` and `ProxyGroupSpec`; refused on `type: OnDemand` by CEL.
- New condition reason `WaitingForEarlierStage`, message exactly `waiting for stage <n>: <name>[, <name>…]` (names sorted, deduplicated).
- Budget message stays exactly `waiting for a changeover place; changing over: <names>`.
- `status.changeover` keeps its four values; `Deferred` widens, it is not renamed.
- The field must not reach any pod: `internal/podspec/hash_golden_test.go` stays unchanged. If it moves, the field leaked into the render — fix that, do not update the golden.
- Comments: default is none (see `~/.claude/CLAUDE.md` "Kommentare"). Keep the existing long comments that stay true; update the ones this change makes false (the one-at-a-time proxy comments in `rollout.go` and `proxygroup_controller.go`).
- Commits: Conventional Commits with scope, body wrapped at 72, ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commits are gpg-signed; on `paul-desktop` just commit.
- Nothing about the private network that motivated this goes into code, tests, docs or commits. Example names: `edge` (proxy group), `lobby`, `arena`.
- After API changes: `nd make manifests generate` and commit the generated files.

## Review Focus

1. **A proxy group whose replacements never become Ready** must keep serving on its old pods: nothing is marked until `replicas` current pods are Ready, and `BackingOff`/`Degraded` frees its place and its stage. → Task 4 test "replacements not ready: nothing marked", Task 7 envtest relies on the existing Degraded path.
2. **The spec reverted mid blue/green** (old hash comes back while new pods exist): the old pods are current again, the new ones are stale; nothing must drain the pods now serving players before the others are Ready. → Task 4 case "a reverted spec mid-roll marks nothing while the old pods are the current ones".
3. **A readiness blip on a `Deferred` group** must not take a budget place back (hysteresis). → Task 3 and Task 5 cases "was Deferred, current not Ready, stays Deferred"; Task 6 envtest proves `was` reaches the function live.
4. **Stages with the budget unset** must still gate; all stages 0 with budget unset must change nothing for existing networks. → Task 2 cases.
5. **A persistent group with an ordinal added during the roll** must not become `Begun` and skip its stage. → Task 3 case "a new current ordinal beside stale ones while Waiting stays Waiting".

---

### Task 1: API field, reason, CEL

**Files:**
- Modify: `api/v1alpha1/servergroup_types.go` (spec struct near `Update` at ~line 362, CEL markers at lines 140–158)
- Modify: `api/v1alpha1/proxygroup_types.go` (`ProxyGroupSpec`, near `Update` ~line 253)
- Modify: `api/v1alpha1/common_types.go` (reasons near line 203; `ChangeoverDeferred` doc at ~327)
- Test: `api/v1alpha1/servergroup_envtest_test.go`
- Generated: `config/crd/bases/`, `charts/spawnery/templates/crds.yaml`, `docs/reference/crds.md`, `zz_generated.deepcopy.go` (int32 needs no deepcopy change, regenerate anyway)

**Interfaces:**
- Produces: `ServerGroupSpec.ChangeoverStage int32`, `ProxyGroupSpec.ChangeoverStage int32`, `spawneryv1alpha1.ReasonWaitingForEarlierStage = "WaitingForEarlierStage"`.

- [ ] **Step 1: Write the failing CEL test**

Read `TestServerGroupOnDemandRefusesSizingFields` (servergroup_envtest_test.go:93) for the fixture helpers it uses, then add beside it, using the same helpers to build a valid on-demand group:

```go
func TestServerGroupOnDemandRefusesChangeoverStage(t *testing.T) {
	g := validOnDemandGroup("od-stage") // the helper the neighbouring tests use; adapt the name
	g.Spec.ChangeoverStage = 5
	err := k8sClient.Create(ctx, g)
	if err == nil || !strings.Contains(err.Error(), "spec.changeoverStage is not allowed for type OnDemand") {
		t.Fatalf("create = %v, want the changeoverStage refusal", err)
	}
}
```

If the neighbouring tests use a different client/ctx name or builder, use theirs; the assertion text stays.

- [ ] **Step 2: Run it, expect a compile failure** (`ChangeoverStage` undefined)

Run: `nd go test ./api/v1alpha1/ -run TestServerGroupOnDemandRefusesChangeoverStage -count=1`

- [ ] **Step 3: Add the fields, the marker and the reason**

`servergroup_types.go`, CEL block (after line 152):

```go
// +kubebuilder:validation:XValidation:rule="self.type != 'OnDemand' || !has(self.changeoverStage) || self.changeoverStage == 0",message="spec.changeoverStage is not allowed for type OnDemand"
```

`ServerGroupSpec`, directly above `Update`:

```go
	// ChangeoverStage orders this group's changeover against the network's
	// other groups: a group waits while any group of a lower stage is still
	// changing over. Groups of one stage change over together, within
	// Network.spec.update.maxConcurrentChangeovers. Not for type OnDemand.
	// +optional
	ChangeoverStage int32 `json:"changeoverStage,omitempty"`
```

`ProxyGroupSpec`, directly above `Update`:

```go
	// ChangeoverStage orders this group's changeover against the network's
	// other groups: a group waits while any group of a lower stage is still
	// changing over. Groups of one stage change over together, within
	// Network.spec.update.maxConcurrentChangeovers.
	// +optional
	ChangeoverStage int32 `json:"changeoverStage,omitempty"`
```

`common_types.go`, beside `ReasonWaitingForChangeoverBudget`:

```go
	// ReasonWaitingForEarlierStage: a group of a lower changeoverStage is
	// still changing over.
	ReasonWaitingForEarlierStage = "WaitingForEarlierStage"
```

Replace the `ChangeoverDeferred` doc comment with:

```go
	// ChangeoverDeferred means the group's new generation stands and its
	// remaining stale servers or pods wait only for their players: it holds
	// no place in the network's budget and gates no later stage.
```

- [ ] **Step 4: Regenerate and run**

Run: `nd make manifests generate` then `nd go test ./api/v1alpha1/ -count=1` and `nd go test ./internal/podspec/ -run Golden -count=1`
Expected: PASS; the hash golden test passes unchanged.

- [ ] **Step 5: Commit**

```bash
git add api/ config/ charts/ docs/reference/
git commit -m "feat(api): spec.changeoverStage on server and proxy groups"
```

---

### Task 2: Stage gate in `AdmitChangeovers`, wait description

**Files:**
- Modify: `internal/controller/changeover.go`
- Test: `internal/controller/changeover_test.go`

**Interfaces:**
- Consumes: Task 1 reason constant.
- Produces:
  - `ChangeoverView{Kind, Name string; State ChangeoverState; Failing bool; Stage int32; Persistent bool}`
  - `AdmitChangeovers(groups []ChangeoverView, budget int32) map[string]bool` (same signature)
  - `type ChangeoverWait struct{ Reason, Message string }` (zero value = not waiting)
  - `describeWait(groups []ChangeoverView, budget int32, self ChangeoverView) ChangeoverWait` — `groups` includes `self`.

- [ ] **Step 1: Add the failing table cases** to `TestAdmitChangeovers` (keep every existing case unchanged — they must still pass with `Stage` 0 and `Persistent` false):

```go
		{
			"a later stage waits for an earlier one in flight, budget unset",
			[]ChangeoverView{
				{Kind: "ProxyGroup", Name: "edge", State: spawneryv1alpha1.ChangeoverBegun, Stage: -10},
				{Kind: "ServerGroup", Name: "lobby", State: spawneryv1alpha1.ChangeoverWaiting},
			},
			0,
			map[string]bool{"ProxyGroup/edge": true},
		},
		{
			"a waiting earlier stage gates as much as a begun one",
			[]ChangeoverView{
				{Kind: "ProxyGroup", Name: "edge", State: spawneryv1alpha1.ChangeoverWaiting, Stage: -10},
				{Kind: "ServerGroup", Name: "arena", State: spawneryv1alpha1.ChangeoverWaiting},
			},
			1,
			map[string]bool{"ProxyGroup/edge": true},
		},
		{
			"a deferred earlier stage gates nothing",
			[]ChangeoverView{
				{Kind: "ProxyGroup", Name: "edge", State: spawneryv1alpha1.ChangeoverDeferred, Stage: -10},
				{Kind: "ServerGroup", Name: "lobby", State: spawneryv1alpha1.ChangeoverWaiting},
			},
			1,
			map[string]bool{"ServerGroup/lobby": true},
		},
		{
			"a failing earlier stage gates nothing",
			[]ChangeoverView{
				{Kind: "ProxyGroup", Name: "edge", State: spawneryv1alpha1.ChangeoverBegun, Stage: -10, Failing: true},
				{Kind: "ServerGroup", Name: "lobby", State: spawneryv1alpha1.ChangeoverWaiting},
			},
			1,
			map[string]bool{"ServerGroup/lobby": true},
		},
		{
			"one stage changes over together within the budget, ordered by name",
			[]ChangeoverView{
				{Kind: "ServerGroup", Name: "lobby", State: spawneryv1alpha1.ChangeoverWaiting, Stage: 5},
				{Kind: "ServerGroup", Name: "arena", State: spawneryv1alpha1.ChangeoverWaiting, Stage: 5},
				{Kind: "ServerGroup", Name: "build", State: spawneryv1alpha1.ChangeoverWaiting, Stage: 5},
			},
			2,
			map[string]bool{"ServerGroup/arena": true, "ServerGroup/build": true},
		},
		{
			"the budget goes by stage before name",
			[]ChangeoverView{
				{Kind: "ServerGroup", Name: "arena", State: spawneryv1alpha1.ChangeoverWaiting, Stage: 1},
				{Kind: "ServerGroup", Name: "zeta", State: spawneryv1alpha1.ChangeoverWaiting, Stage: 1},
				{Kind: "ServerGroup", Name: "lobby", State: spawneryv1alpha1.ChangeoverWaiting, Stage: 0},
			},
			1,
			map[string]bool{"ServerGroup/lobby": true},
		},
		{
			"a begun later stage is not paused by an earlier stage turning stale",
			[]ChangeoverView{
				{Kind: "ProxyGroup", Name: "edge", State: spawneryv1alpha1.ChangeoverWaiting, Stage: -10},
				{Kind: "ServerGroup", Name: "lobby", State: spawneryv1alpha1.ChangeoverBegun},
			},
			2,
			map[string]bool{"ProxyGroup/edge": true, "ServerGroup/lobby": true},
		},
		{
			"a persistent group gates a later stage but takes no budget place",
			[]ChangeoverView{
				{Kind: "ServerGroup", Name: "world", State: spawneryv1alpha1.ChangeoverBegun, Persistent: true},
				{Kind: "ServerGroup", Name: "lobby", State: spawneryv1alpha1.ChangeoverWaiting},
				{Kind: "ServerGroup", Name: "arena", State: spawneryv1alpha1.ChangeoverWaiting, Stage: 10},
			},
			1,
			map[string]bool{"ServerGroup/world": true, "ServerGroup/lobby": true},
		},
		{
			"a waiting persistent group is admitted past a full budget",
			[]ChangeoverView{
				{Kind: "ServerGroup", Name: "lobby", State: spawneryv1alpha1.ChangeoverBegun},
				{Kind: "ServerGroup", Name: "world", State: spawneryv1alpha1.ChangeoverWaiting, Persistent: true},
			},
			1,
			map[string]bool{"ServerGroup/lobby": true, "ServerGroup/world": true},
		},
```

And a new test:

```go
func TestDescribeWait(t *testing.T) {
	edge := ChangeoverView{Kind: "ProxyGroup", Name: "edge", State: spawneryv1alpha1.ChangeoverBegun, Stage: -10}
	edge2 := ChangeoverView{Kind: "ProxyGroup", Name: "edge-b", State: spawneryv1alpha1.ChangeoverWaiting, Stage: -10}
	deeper := ChangeoverView{Kind: "ProxyGroup", Name: "outer", State: spawneryv1alpha1.ChangeoverBegun, Stage: -20}
	arena := ChangeoverView{Kind: "ServerGroup", Name: "arena", State: spawneryv1alpha1.ChangeoverBegun}
	lobby := ChangeoverView{Kind: "ServerGroup", Name: "lobby", State: spawneryv1alpha1.ChangeoverWaiting}
	for _, tc := range []struct {
		name   string
		groups []ChangeoverView
		budget int32
		want   ChangeoverWait
	}{
		{"an earlier stage names that stage and its groups", []ChangeoverView{edge2, edge, lobby}, 0,
			ChangeoverWait{spawneryv1alpha1.ReasonWaitingForEarlierStage, "waiting for stage -10: edge, edge-b"}},
		{"the lowest earlier stage is the one named", []ChangeoverView{edge, deeper, lobby}, 0,
			ChangeoverWait{spawneryv1alpha1.ReasonWaitingForEarlierStage, "waiting for stage -20: outer"}},
		{"no earlier stage: the budget is named", []ChangeoverView{arena, lobby}, 1,
			ChangeoverWait{spawneryv1alpha1.ReasonWaitingForChangeoverBudget, "waiting for a changeover place; changing over: arena"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeWait(tc.groups, tc.budget, lobby); got != tc.want {
				t.Errorf("describeWait = %+v, want %+v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run, expect compile failure** (`Stage`, `Persistent`, `describeWait` undefined)

Run: `nd go test ./internal/controller/ -run 'TestAdmitChangeovers|TestDescribeWait' -count=1`

- [ ] **Step 3: Implement** in `changeover.go`

```go
type ChangeoverView struct {
	Kind       string // "ServerGroup" or "ProxyGroup"
	Name       string
	State      spawneryv1alpha1.ChangeoverState
	Failing    bool
	Stage      int32
	Persistent bool // gated by stage, takes no budget place: it never surges
}

func changeoverInFlight(g ChangeoverView) bool {
	return !g.Failing && (g.State == spawneryv1alpha1.ChangeoverWaiting || g.State == spawneryv1alpha1.ChangeoverBegun)
}

// earliestStageBefore is the lowest stage below stage with a group in flight,
// and that stage's groups by name.
func earliestStageBefore(groups []ChangeoverView, stage int32) (int32, []string, bool) {
	found := false
	var lowest int32
	for _, g := range groups {
		if changeoverInFlight(g) && g.Stage < stage && (!found || g.Stage < lowest) {
			lowest, found = g.Stage, true
		}
	}
	if !found {
		return 0, nil, false
	}
	seen := map[string]bool{}
	var names []string
	for _, g := range groups {
		if changeoverInFlight(g) && g.Stage == lowest && !seen[g.Name] {
			seen[g.Name] = true
			names = append(names, g.Name)
		}
	}
	sort.Strings(names)
	return lowest, names, true
}

// AdmitChangeovers returns the groups, keyed "Kind/Name", that may change over
// now. budget < 1 means no cap; the stage gate applies either way.
func AdmitChangeovers(groups []ChangeoverView, budget int32) map[string]bool {
	admitted := map[string]bool{}
	var waiting []ChangeoverView
	var holders int32
	for _, g := range groups {
		if !changeoverInFlight(g) {
			continue
		}
		if g.State == spawneryv1alpha1.ChangeoverBegun {
			admitted[changeoverKey(g.Kind, g.Name)] = true
			if !g.Persistent {
				holders++
			}
			continue
		}
		if _, _, gated := earliestStageBefore(groups, g.Stage); gated {
			continue
		}
		if g.Persistent || budget < 1 {
			admitted[changeoverKey(g.Kind, g.Name)] = true
			continue
		}
		waiting = append(waiting, g)
	}
	sort.Slice(waiting, func(i, j int) bool {
		a, b := waiting[i], waiting[j]
		if a.Stage != b.Stage {
			return a.Stage < b.Stage
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Kind < b.Kind
	})
	for _, g := range waiting {
		if holders >= budget {
			break
		}
		admitted[changeoverKey(g.Kind, g.Name)] = true
		holders++
	}
	return admitted
}

// ChangeoverWait is why a group that must change over was not admitted; the
// zero value means it was.
type ChangeoverWait struct {
	Reason  string
	Message string
}

func describeWait(groups []ChangeoverView, budget int32, self ChangeoverView) ChangeoverWait {
	if stage, names, ok := earliestStageBefore(groups, self.Stage); ok {
		return ChangeoverWait{
			Reason:  spawneryv1alpha1.ReasonWaitingForEarlierStage,
			Message: fmt.Sprintf("waiting for stage %d: %s", stage, strings.Join(names, ", ")),
		}
	}
	return ChangeoverWait{
		Reason: spawneryv1alpha1.ReasonWaitingForChangeoverBudget,
		Message: "waiting for a changeover place; changing over: " +
			strings.Join(changeoverHolders(groups, AdmitChangeovers(groups, budget), self.Kind, self.Name), ", "),
	}
}
```

Note: the old loop skipped `ChangeoverDeferred` explicitly; `changeoverInFlight` does that now. Add `fmt` and `strings` to the imports.

- [ ] **Step 4: Run** — `nd go test ./internal/controller/ -run 'TestAdmitChangeovers|TestDescribeWait' -count=1` → PASS (old and new cases).

- [ ] **Step 5: Commit** — `feat(controller): a stage gate in front of the changeover budget`

---

### Task 3: Server-side states — `RollingUpdate` `Deferred`, persistent state, persistent refusal

**Files:**
- Modify: `internal/controller/changeover.go` (`ownServerChangeover`, new `ownPersistentChangeover`)
- Modify: `internal/controller/persistent.go` (`PersistentInputs`, stale nomination ~line 211)
- Test: `internal/controller/changeover_test.go`, `internal/controller/persistent_test.go`

**Interfaces:**
- Produces:
  - `ownServerChangeover(views []ServerView, podHash string, pendingCreates int32, whenEmpty bool, was ChangeoverState) ChangeoverState` (same signature, wider `Deferred`)
  - `ownPersistentChangeover(views []ServerView, podHash string, takedown bool, was ChangeoverState) ChangeoverState`
  - `PersistentInputs.ChangeoverRefused bool`

- [ ] **Step 1: Failing cases.** Add to the `TestOwnServerChangeover` table (existing cases stay; note the existing case "RollingUpdate with a Ready current server" passes `old` which is not leaving, so it stays `Begun`):

```go
		{"RollingUpdate with every stale server retiring and the current one Ready", []ServerView{retiringOld, current}, 0, false, spawneryv1alpha1.ChangeoverBegun, spawneryv1alpha1.ChangeoverDeferred},
		{"RollingUpdate with a stale server not yet asked to leave", []ServerView{retiringOld, old, current}, 0, false, spawneryv1alpha1.ChangeoverBegun, spawneryv1alpha1.ChangeoverBegun},
		{"RollingUpdate with a replacement still starting", []ServerView{retiringOld, current, startingCurrent}, 0, false, spawneryv1alpha1.ChangeoverBegun, spawneryv1alpha1.ChangeoverBegun},
		{"RollingUpdate with a create in flight", []ServerView{retiringOld, current}, 1, false, spawneryv1alpha1.ChangeoverBegun, spawneryv1alpha1.ChangeoverBegun},
		{"RollingUpdate deferred stays deferred through a readiness blip", []ServerView{retiringOld, startingCurrent}, 0, false, spawneryv1alpha1.ChangeoverDeferred, spawneryv1alpha1.ChangeoverDeferred},
		{"RollingUpdate deferred falls back once a stale server is serving again", []ServerView{old, current}, 0, false, spawneryv1alpha1.ChangeoverDeferred, spawneryv1alpha1.ChangeoverBegun},
```

with, beside the existing fixtures:

```go
	retiringOld := ServerView{Name: "old-r", PodHash: "old", Phase: phase.Retiring, Retire: true}
```

New test:

```go
func TestOwnPersistentChangeover(t *testing.T) {
	stale := ServerView{Name: "w-0", PodHash: "old", Phase: phase.Ready, Ordinal: ptr.To[int32](0)}
	current := ServerView{Name: "w-1", PodHash: "current", Phase: phase.Ready, Ordinal: ptr.To[int32](1)}
	held := ServerView{Name: "w-0", PodHash: "old", Phase: phase.Ready, Ordinal: ptr.To[int32](0), Hold: true}
	for _, tc := range []struct {
		name     string
		views    []ServerView
		takedown bool
		was      spawneryv1alpha1.ChangeoverState
		want     spawneryv1alpha1.ChangeoverState
	}{
		{"nothing stale", []ServerView{current}, false, "", spawneryv1alpha1.ChangeoverNone},
		{"stale, nothing down", []ServerView{stale}, false, "", spawneryv1alpha1.ChangeoverWaiting},
		{"a takedown in flight has begun", []ServerView{stale}, true, spawneryv1alpha1.ChangeoverWaiting, spawneryv1alpha1.ChangeoverBegun},
		{"begun stays begun between takedowns", []ServerView{stale, current}, false, spawneryv1alpha1.ChangeoverBegun, spawneryv1alpha1.ChangeoverBegun},
		{"a new current ordinal beside stale ones while Waiting stays Waiting", []ServerView{stale, current}, false, spawneryv1alpha1.ChangeoverWaiting, spawneryv1alpha1.ChangeoverWaiting},
		{"a held stale server is no changeover", []ServerView{held, current}, false, spawneryv1alpha1.ChangeoverBegun, spawneryv1alpha1.ChangeoverNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownPersistentChangeover(tc.views, "current", tc.takedown, tc.was); got != tc.want {
				t.Errorf("ownPersistentChangeover = %q, want %q", got, tc.want)
			}
		})
	}
}
```

In `persistent_test.go`, a refusal test (use the `ordinalView` helper and set `PodHash` as line ~199 does):

```go
func TestDecidePersistentSizeRefusedNominatesNoStaleOrdinal(t *testing.T) {
	stale := func(name string, o int32) ServerView {
		v := ordinalView(name, o, phase.Ready)
		v.PodHash = "old"
		return v
	}
	in := PersistentInputs{
		Group: "survival", Replicas: 3, PodHash: "new", ChangeoverRefused: true,
		Views: []ServerView{stale("survival-0", 0), stale("survival-2", 2)},
	}
	got := DecidePersistentSize(in)
	if len(got.Delete) != 0 {
		t.Fatalf("Delete = %v, want none: the changeover is refused", got.Delete)
	}
	if !equalOrdinals(got.CreateOrdinals, []int32{1}) {
		t.Fatalf("CreateOrdinals = %v, want [1]: a missing ordinal is still replaced", got.CreateOrdinals)
	}
	in.ChangeoverRefused = false
	in.Views = append(in.Views, stale("survival-1", 1))
	if got := DecidePersistentSize(in); got.DeleteReason != "StaleSpec" {
		t.Fatalf("unrefused DeleteReason = %q, want StaleSpec", got.DeleteReason)
	}
}
```

- [ ] **Step 2: Run, expect failures** — `nd go test ./internal/controller/ -run 'TestOwnServerChangeover|TestOwnPersistentChangeover|TestDecidePersistentSizeRefused' -count=1`

- [ ] **Step 3: Implement**

`ownServerChangeover`:

```go
func ownServerChangeover(views []ServerView, podHash string, pendingCreates int32, whenEmpty bool, was spawneryv1alpha1.ChangeoverState) spawneryv1alpha1.ChangeoverState {
	var stale, staleServing, current, readyCurrent, unreadyCurrent bool
	for _, v := range views {
		if v.Hold {
			continue
		}
		if staleSpec(v, podHash) {
			if !phase.Terminal(v.Phase) {
				stale = true
				if !v.leaving() && !v.Retire {
					staleServing = true
				}
			}
		} else if v.countsTowardSize() {
			current = true
			if v.Phase == phase.Ready {
				readyCurrent = true
			} else {
				unreadyCurrent = true
			}
		}
	}
	wasDeferred := was == spawneryv1alpha1.ChangeoverDeferred
	switch {
	case !stale:
		return spawneryv1alpha1.ChangeoverNone
	case whenEmpty && (readyCurrent || (wasDeferred && current)):
		return spawneryv1alpha1.ChangeoverDeferred
	case !whenEmpty && !staleServing && current &&
		(wasDeferred || (pendingCreates == 0 && !unreadyCurrent)):
		return spawneryv1alpha1.ChangeoverDeferred
	case current || pendingCreates > 0:
		return spawneryv1alpha1.ChangeoverBegun
	default:
		return spawneryv1alpha1.ChangeoverWaiting
	}
}
```

Update its doc comment to name the `RollingUpdate` case in one sentence; drop nothing else that stays true.

`ownPersistentChangeover`:

```go
// ownPersistentChangeover is a persistent group's changeover state. It begins
// with its first stale takedown and stays begun while stale ordinals remain; a
// current ordinal added meanwhile does not begin it.
func ownPersistentChangeover(views []ServerView, podHash string, takedown bool, was spawneryv1alpha1.ChangeoverState) spawneryv1alpha1.ChangeoverState {
	stale := false
	for _, v := range views {
		if !v.Hold && staleSpec(v, podHash) && !phase.Terminal(v.Phase) {
			stale = true
		}
	}
	switch {
	case !stale:
		return spawneryv1alpha1.ChangeoverNone
	case takedown || was == spawneryv1alpha1.ChangeoverBegun:
		return spawneryv1alpha1.ChangeoverBegun
	default:
		return spawneryv1alpha1.ChangeoverWaiting
	}
}
```

`persistent.go`: add to `PersistentInputs`:

```go
	// ChangeoverRefused withholds the stale nomination: an earlier stage of
	// the network is still changing over.
	ChangeoverRefused bool
```

and in `DecidePersistentSize`, change `if len(stale) > 0 {` (line ~232) to `if len(stale) > 0 && !in.ChangeoverRefused {`. The claim-resize class below it stays reachable on a refused pass — that is intended (it is not a changeover).

- [ ] **Step 4: Run** the three tests, then `nd go test ./internal/controller/ -run 'Persistent|Changeover' -count=1` → PASS.

- [ ] **Step 5: Commit** — `feat(controller): RollingUpdate and persistent groups report their changeover`

---

### Task 4: Blue/green proxy roll in `DecideRollout`

**Files:**
- Modify: `internal/controller/rollout.go` (`DecideRollout`, its doc comment, lines 48–136)
- Test: `internal/controller/rollout_test.go`
- Modify (expected fallout): `internal/controller/proxygroup_controller_test.go` tests that encode one-at-a-time replacement of *stale* pods (around lines 1270, 1335, 1896–1980, 2052, 2089–2160)

**Interfaces:**
- Produces: `DecideRollout(pods []ProxyView, replicas int32, surgeAllowed bool) RolloutDecision` (same signature).

- [ ] **Step 1: Failing cases.** Add to the `TestDecideRollout` table (field style as the existing cases):

```go
		{
			name: "blue/green: every stale pod gets its replacement up front",
			pods: []ProxyView{
				{Name: "a", Stale: true, Ready: true, CreatedAt: at(0)},
				{Name: "b", Stale: true, Ready: true, CreatedAt: at(1)},
				{Name: "c", Stale: true, Ready: true, CreatedAt: at(2)},
			},
			replicas: 3,
			want:     RolloutDecision{Create: 3},
		},
		{
			name: "blue/green: replacements not ready: nothing marked",
			pods: []ProxyView{
				{Name: "a", Stale: true, Ready: true, CreatedAt: at(0)},
				{Name: "b", Stale: true, Ready: true, CreatedAt: at(1)},
				{Name: "n1", Ready: true, CreatedAt: at(3)},
				{Name: "n2", CreatedAt: at(4)},
			},
			replicas: 2,
			want:     RolloutDecision{},
		},
		{
			name: "blue/green: replicas current pods Ready marks every stale pod at once",
			pods: []ProxyView{
				{Name: "a", Stale: true, Ready: true, Players: 1, CreatedAt: at(0)},
				{Name: "b", Stale: true, Ready: true, Players: 20, CreatedAt: at(1)},
				{Name: "n1", Ready: true, CreatedAt: at(3)},
				{Name: "n2", Ready: true, CreatedAt: at(4)},
			},
			replicas: 2,
			want:     RolloutDecision{Drain: []string{"a", "b"}},
		},
		{
			name: "blue/green: a stale pod not yet marked is marked beside one already draining",
			pods: []ProxyView{
				{Name: "a", Stale: true, Draining: true, Players: 1, CreatedAt: at(0)},
				{Name: "b", Stale: true, Ready: true, CreatedAt: at(1)},
				{Name: "n1", Ready: true, CreatedAt: at(3)},
				{Name: "n2", Ready: true, CreatedAt: at(4)},
			},
			replicas: 2,
			want:     RolloutDecision{Drain: []string{"b"}},
		},
		{
			name: "blue/green: a replacement dying mid-drain is rebuilt",
			pods: []ProxyView{
				{Name: "a", Stale: true, Draining: true, Players: 1, CreatedAt: at(0)},
				{Name: "b", Stale: true, Draining: true, Players: 3, CreatedAt: at(1)},
				{Name: "n1", Ready: true, CreatedAt: at(3)},
			},
			replicas: 2,
			want:     RolloutDecision{Create: 1},
		},
		{
			name: "blue/green: a stale pod serving nobody is marked before the replacements are Ready",
			pods: []ProxyView{
				{Name: "a", Stale: true, CreatedAt: at(0)},
				{Name: "b", Stale: true, Ready: true, CreatedAt: at(1)},
				{Name: "n1", CreatedAt: at(3)},
				{Name: "n2", CreatedAt: at(4)},
			},
			replicas: 2,
			want:     RolloutDecision{Drain: []string{"a"}},
		},
		{
			name: "a reverted spec mid-roll marks nothing while the old pods are the current ones",
			pods: []ProxyView{
				{Name: "a", Ready: true, Players: 5, CreatedAt: at(0)},
				{Name: "b", Ready: true, Players: 5, CreatedAt: at(1)},
				{Name: "n1", Stale: true, CreatedAt: at(3)},
				{Name: "n2", Stale: true, CreatedAt: at(4)},
			},
			replicas: 2,
			want:     RolloutDecision{Drain: []string{"n1", "n2"}},
		},
		{
			name: "a lowered replicas with nothing stale drains the surplus as today",
			pods: []ProxyView{
				{Name: "a", Ready: true, Players: 1, CreatedAt: at(0)},
				{Name: "b", Ready: true, Players: 2, CreatedAt: at(1)},
				{Name: "c", Ready: true, Players: 3, CreatedAt: at(2)},
			},
			replicas: 1,
			want:     RolloutDecision{Drain: []string{"a", "b"}},
		},
```

Note on the reverted-spec case: after a revert, `n1`/`n2` are stale and the old pods are current and Ready, so both new pods are marked at once (they serve nobody). The case pins that no pod now carrying players is marked.

The existing cases "the surge pod is ready, so exactly one stale pod is marked", "one already draining: no second replacement begins", "the surge pod dying mid-drain is replaced, because surge outlives the mark", "all stale: the surge pod is created before anything is marked", "the surge pod is not ready yet, so nothing is marked" encode the surge of one. Rewrite each to the blue/green answer for the same pods (run them, read the new output, check it against the four rules of spec §3.4, then set `want`). Keep their names truthful (rename where "surge pod"/"one" no longer holds). List every changed case by old name in the commit body.

- [ ] **Step 2: Run, expect failures** — `nd go test ./internal/controller/ -run TestDecideRollout -count=1`

- [ ] **Step 3: Implement**

```go
func DecideRollout(pods []ProxyView, replicas int32, surgeAllowed bool) RolloutDecision {
	var stale, draining, currentReady int32
	var markable []string
	for _, p := range pods {
		if p.Stale {
			stale++
			if !p.Draining {
				markable = append(markable, p.Name)
			}
		} else if p.Ready && !p.Draining {
			currentReady++
		}
		if p.Draining {
			draining++
		}
	}

	var surge int32
	if surgeAllowed {
		surge = stale
	}
	target := replicas + surge
	total := int32(len(pods))

	if total < target {
		return RolloutDecision{Create: target - total}
	}

	if surgeAllowed && len(markable) > 0 && currentReady >= replicas {
		return RolloutDecision{Drain: markable}
	}

	if anyStaleNotReady(pods) {
		return RolloutDecision{Drain: pick(staleOnly(pods), 1)}
	}

	if draining > 0 {
		return RolloutDecision{}
	}

	if total > target {
		return RolloutDecision{Drain: pick(pods, total-target)}
	}

	if !surgeAllowed && stale > 0 && readyBeyond(pods, replicas) {
		return RolloutDecision{Drain: pick(staleOnly(pods), 1)}
	}
	return RolloutDecision{}
}
```

Check `staleOnly` + `pick` with a draining stale pod present: `anyStaleNotReady` excludes draining pods but `staleOnly` does not; `pick` sorts not-Ready first, and a draining pod is usually not Ready. Make the not-serving branch pick only from stale, not-Ready, not-draining pods:

```go
	if anyStaleNotReady(pods) {
		var idle []ProxyView
		for _, p := range pods {
			if p.Stale && !p.Ready && !p.Draining {
				idle = append(idle, p)
			}
		}
		return RolloutDecision{Drain: pick(idle, 1)}
	}
```

Rewrite the doc comment of `DecideRollout` in a few lines: blue/green while it may surge (§3.4 of the spec), surplus as today, without surge only a stale pod serving nobody (or one with ready capacity to spare) is replaced in place. Drop the paragraphs about §3.2's surge-of-one argument; the history stays in git. Update `anyStaleNotReady`'s comment where it claims the draining guard runs before it.

- [ ] **Step 4: Run the table**, then the whole proxy suite: `nd go test ./internal/controller/ -run 'Rollout|Proxy' -count=1`. Every failing envtest in `proxygroup_controller_test.go` is read, not patched blindly: if it asserts one-at-a-time replacement of stale pods (surge pod count 3 for replicas 2, "exactly one marked"), update the expectation to blue/green and its comment; if it asserts anything else (surplus, node drain deadline, marks surviving a revert), the implementation is wrong — fix the code. Then update the comment at `proxygroup_controller.go:1041-1046` ("DecideRollout deliberately names nobody while another pod is draining — that is what makes the update one proxy at a time") to say this holds for surplus only.

- [ ] **Step 5: Commit** — `feat(controller): proxy groups roll blue/green`, body lists every changed test case by name and says a proxy changeover now runs up to `replicas` extra pods.

---

### Task 5: Proxy `Deferred`

**Files:**
- Modify: `internal/controller/changeover.go` (`ownProxyChangeover`)
- Test: `internal/controller/changeover_test.go` (`TestOwnProxyChangeover`)

**Interfaces:**
- Produces: `ownProxyChangeover(pods []corev1.Pod, wantHash string, pendingCreates, replicas int32, was ChangeoverState) ChangeoverState`

- [ ] **Step 1: Failing cases.** Change the existing call to `ownProxyChangeover(tc.pods, "new", tc.pending, tc.replicas, tc.was)` and add `replicas int32; was ChangeoverState` to the case struct, with `replicas: 2` for every existing case (re-check each still gives the same answer; "stale and current" with one current pod of two stays `Begun`). New mutators and cases:

```go
	ready := func(p *corev1.Pod) {
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	}
	draining := func(p *corev1.Pod) {
		p.Annotations = map[string]string{ProxyDrainingSinceAnnotation: "2026-10-01T12:00:00Z"}
	}
```

```go
		{"replicas current Ready and every stale pod draining is deferred",
			[]corev1.Pod{pod("old", draining), pod("old", draining), pod("new", ready), pod("new", ready)}, 0, 2, "Begun",
			spawneryv1alpha1.ChangeoverDeferred},
		{"one current pod short of replicas is begun",
			[]corev1.Pod{pod("old", draining), pod("old", draining), pod("new", ready), pod("new")}, 0, 2, "Begun",
			spawneryv1alpha1.ChangeoverBegun},
		{"a stale pod not yet draining is begun",
			[]corev1.Pod{pod("old", draining), pod("old", ready), pod("new", ready), pod("new", ready)}, 0, 2, "Begun",
			spawneryv1alpha1.ChangeoverBegun},
		{"deferred stays deferred through a readiness blip",
			[]corev1.Pod{pod("old", draining), pod("new", ready), pod("new")}, 0, 2, "Deferred",
			spawneryv1alpha1.ChangeoverDeferred},
		{"terminating stale pods count as leaving",
			[]corev1.Pod{pod("old", terminating), pod("new", ready), pod("new", ready)}, 0, 2, "Begun",
			spawneryv1alpha1.ChangeoverDeferred},
```

(Use `spawneryv1alpha1.ChangeoverBegun`/`ChangeoverDeferred` constants rather than string literals for `was`.)

- [ ] **Step 2: Run, expect compile failure.**

- [ ] **Step 3: Implement**

```go
func ownProxyChangeover(pods []corev1.Pod, wantHash string, pendingCreates, replicas int32, was spawneryv1alpha1.ChangeoverState) spawneryv1alpha1.ChangeoverState {
	var stale, staleServing, current bool
	var readyCurrent int32
	for i := range pods {
		p := &pods[i]
		if p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodSucceeded {
			continue
		}
		if p.Labels[podspec.LabelPodHash] != wantHash {
			stale = true
			if _, marked := drainingSince(p); !marked && p.DeletionTimestamp.IsZero() {
				staleServing = true
			}
		} else if p.DeletionTimestamp.IsZero() {
			current = true
			if isPodReady(p) {
				readyCurrent++
			}
		}
	}
	switch {
	case !stale:
		return spawneryv1alpha1.ChangeoverNone
	case !staleServing && current &&
		(was == spawneryv1alpha1.ChangeoverDeferred || (readyCurrent >= replicas && pendingCreates == 0)):
		return spawneryv1alpha1.ChangeoverDeferred
	case current || pendingCreates > 0:
		return spawneryv1alpha1.ChangeoverBegun
	default:
		return spawneryv1alpha1.ChangeoverWaiting
	}
}
```

Update the call in `proxyChangeover` to pass `group.Spec.Replicas, group.Status.Changeover` (it reads the status before `reconcileReplicas` overwrites it — verify at `proxygroup_controller.go:962-966`).

- [ ] **Step 4: Run** `nd go test ./internal/controller/ -run TestOwnProxyChangeover -count=1` → PASS.

- [ ] **Step 5: Commit** — `feat(controller): a proxy group whose new pods stand is Deferred`

---

### Task 6: ServerGroup wiring, the lost `was`, gauges

**Files:**
- Modify: `internal/controller/changeover.go` (`changeoverSiblings`)
- Modify: `internal/controller/servergroup_controller.go` (lines 522–539, `size()` 901–1010, `reportProgressing` 1289–1345)
- Modify: `internal/controller/network_controller.go` (`countGroups`, 515–558)
- Modify: `internal/controller/metrics.go` (gauge help texts)
- Test: `internal/controller/changeover_envtest_test.go`
- Generated: `docs/reference/metrics-and-alerts.md` (help text change; `nd make manifests`)

**Interfaces:**
- Consumes: Tasks 2, 3.
- Produces: `reportProgressing(group, views, podHash string, wait ChangeoverWait, floor FloorReport)`.

- [ ] **Step 1: Failing envtests** in `changeover_envtest_test.go`. Use `createPersistentGroup` (servergroup_controller_test.go:3430), `createEphemeralGroupLike`, `setImage`, `readyAllServersOf`, `reconcileNamedGroup`, `finishChangeover`, `progressing`, `serverGroup`. Add a helper:

```go
func (f *fixture) setStage(t *testing.T, group string, stage int32) {
	t.Helper()
	g := f.serverGroup(t, group)
	g.Spec.ChangeoverStage = stage
	if err := f.c.Update(f.ctx, g); err != nil {
		t.Fatalf("update ServerGroup %s: %v", group, err)
	}
}
```

Tests:

```go
func TestALaterStageWaitsWithTheBudgetUnset(t *testing.T) {
	f := newFixture(t)
	r := groupReconciler(f)
	f.createEphemeralGroupLike(t, "arena")
	for _, name := range []string{"arena", "lobby"} {
		f.reconcileNamedGroup(t, r, name)
		f.readyAllServersOf(t, name)
	}
	f.setStage(t, "arena", 10)
	f.setImage(t, "arena", nextImage)
	f.setImage(t, "lobby", nextImage)

	f.reconcileNamedGroup(t, r, "lobby")
	f.reconcileNamedGroup(t, r, "arena")

	if n := len(f.serverNamesOfGroup(t, "arena")); n != 1 {
		t.Fatalf("arena has %d servers, want 1: stage 10 waits for lobby", n)
	}
	if c := f.progressing(t, "arena"); c.Reason != spawneryv1alpha1.ReasonWaitingForEarlierStage ||
		c.Message != "waiting for stage 0: lobby" {
		t.Fatalf("arena Progressing = %s %q", c.Reason, c.Message)
	}

	f.finishChangeover(t, r, "lobby")
	f.reconcileNamedGroup(t, r, "arena")
	if n := len(f.serverNamesOfGroup(t, "arena")); n != 2 {
		t.Fatalf("arena has %d servers, want 2 once lobby is through", n)
	}
}

func TestARollingUpdateGroupReleasesTheNextStageOnceDeferred(t *testing.T) {
	// lobby: RollingUpdate, 1 server. Change it, reconcile until its stale
	// server is Retiring and the replacement Ready (do NOT let the retiring
	// server finish). Assert lobby status.changeover == Deferred and that
	// arena (stage 10) creates its replacement on the next reconcile.
}

func TestAPersistentGroupWaitsForItsStage(t *testing.T) {
	// lobby (stage 0, ephemeral) and world (persistent, replicas 1, stage 10).
	// Change both. Reconcile world: no Server deleted, status.changeover
	// Waiting, Progressing reason WaitingForEarlierStage naming lobby.
	// finishChangeover lobby; reconcile world: the stale ordinal's Server is
	// deleted (or Draining) and status.changeover == Begun.
}

func TestAWhenEmptyGroupStaysDeferredThroughAReadinessLoss(t *testing.T) {
	// The `was` input was always "" in production: size() cleared
	// status.changeover before reading it. A WhenEmpty group reaches Deferred
	// (rolloutfloor_envtest_test.go:60-95 shows how), then its current server
	// is set back to Starting; one reconcile later status.changeover must
	// still be Deferred, not Begun.
}
```

Write the three sketched tests out in full in the style of `TestALaterStageWaitsWithTheBudgetUnset`, reading `rolloutfloor_envtest_test.go` for how a WhenEmpty group and a server phase change are driven. The last test is the proof for the `was` fix: run it **before** Step 3 and record its failure (`status.changeover = "Begun", want "Deferred"`) in the commit body.

- [ ] **Step 2: Run, expect failures** — `nd go test ./internal/controller/ -run 'Stage|Deferred|ReadinessLoss' -count=1`

- [ ] **Step 3: Implement**

`changeoverSiblings`: skip on-demand groups (`if g.IsOnDemand() { continue }`), and fill the new fields:

```go
		views = append(views, ChangeoverView{
			Kind: "ServerGroup", Name: g.Name,
			State: g.Status.Changeover, Failing: changeoverFailing(g.Status.Conditions),
			Stage: g.Spec.ChangeoverStage, Persistent: !g.IsEphemeral(),
		})
```

and `Stage: g.Spec.ChangeoverStage` for proxy groups.

Reconcile (line 522): `if !group.IsOnDemand() && mayResize {` instead of `group.IsEphemeral() && mayResize`.

`size()`: capture `was := group.Status.Changeover` **before** `group.Status.Changeover = spawneryv1alpha1.ChangeoverNone` and pass `was` to `ownServerChangeover`. Build the self view once per branch:

```go
	self := ChangeoverView{
		Kind: "ServerGroup", Name: group.Name,
		Failing: changeoverFailing(group.Status.Conditions),
		Stage: group.Spec.ChangeoverStage, Persistent: !group.IsEphemeral(),
	}
```

Ephemeral branch: `self.State = own`; `admitted := AdmitChangeovers(append(slices.Clip(siblings), self), budget)`; `ChangeoverRefused: own == spawneryv1alpha1.ChangeoverWaiting && !admitted[changeoverKey("ServerGroup", group.Name)]` (the `budget > 0 &&` goes).

Persistent (`default:`) branch:

```go
		own := ownPersistentChangeover(views, podHash,
			takedownInFlight(PersistentInputs{Views: views, PendingDeletes: pendingDeletes}), was)
		self.State = own
		admitted := AdmitChangeovers(append(slices.Clip(siblings), self), budget)
		refused := own == spawneryv1alpha1.ChangeoverWaiting && !admitted[changeoverKey("ServerGroup", group.Name)]
		decision = DecidePersistentSize(PersistentInputs{
			// existing fields unchanged
			ChangeoverRefused: refused,
		})
		decision.ChangeoverWaiting = refused
		group.Status.Changeover = own
		if own == spawneryv1alpha1.ChangeoverWaiting && decision.DeleteReason == "StaleSpec" {
			group.Status.Changeover = spawneryv1alpha1.ChangeoverBegun
		}
```

Reconcile (lines 534–539): replace `waitingFor` with

```go
	var wait ChangeoverWait
	if decision.ChangeoverWaiting {
		wait = describeWait(append(slices.Clip(siblings), ChangeoverView{
			Kind: "ServerGroup", Name: group.Name, State: spawneryv1alpha1.ChangeoverWaiting,
			Stage: group.Spec.ChangeoverStage, Persistent: !group.IsEphemeral(),
		}), budget, ChangeoverView{Kind: "ServerGroup", Name: group.Name, Stage: group.Spec.ChangeoverStage})
	}
```

and pass `wait` to `reportProgressing`, whose first case becomes:

```go
	case wait.Reason != "":
		condition.Status = metav1.ConditionTrue
		condition.Reason = wait.Reason
		condition.Message = wait.Message
```

`countGroups`: count a server group into `inFlight` only when `g.IsEphemeral()` (persistent groups hold no place). Help texts in `metrics.go`: in-flight "Groups of the network holding a changeover budget place."; waiting "Groups of the network waiting for a changeover budget place or an earlier stage."

- [ ] **Step 4: Run** the new tests, then the controller package: `nd go test ./internal/controller/ -count=1` → PASS. `nd make manifests` and check that only `docs/reference/metrics-and-alerts.md` moved.

- [ ] **Step 5: Commit** — `feat(controller): server groups wait for their stage`, and a separate commit first for the `was` fix: `fix(controller): WhenEmpty keeps Deferred through a readiness loss`, body with the recorded failure. (Order: write the readiness-loss test, record failure, fix `was`, commit the fix; then the rest.)

---

### Task 7: ProxyGroup wiring

**Files:**
- Modify: `internal/controller/changeover.go` (`proxyChangeover`)
- Modify: `internal/controller/proxygroup_controller.go` (call at 962–966, `reportChangingOver` 1529–1555)
- Test: `internal/controller/changeover_envtest_test.go`

**Interfaces:**
- Consumes: Tasks 2, 5.
- Produces: `proxyChangeover(...) (ChangeoverState, bool, ChangeoverWait, error)`; `reportChangingOver(group, pods, wantHash string, wait ChangeoverWait)`.

- [ ] **Step 1: Failing envtest**

```go
func TestAServerGroupWaitsForTheProxyStageUntilItsNewPodsStand(t *testing.T) {
	f := newFixture(t)
	gr := groupReconciler(f)
	pr := proxyGroupReconciler(f)
	f.reconcileNamedGroup(t, gr, "lobby")
	f.readyAllServersOf(t, "lobby")
	f.readyProxyGroup(t, pr, "gateway", func(g *spawneryv1alpha1.ProxyGroup) { g.Spec.ChangeoverStage = -10 })

	f.setProxyImage(t, "gateway", nextProxyImage)
	f.setImage(t, "lobby", nextImage)
	f.reconcileProxyGroup(pr, "gateway")
	f.reconcileNamedGroup(t, gr, "lobby")

	if n := len(f.serverNamesOfGroup(t, "lobby")); n != 1 {
		t.Fatalf("lobby has %d servers, want 1: the proxy stage is in flight", n)
	}
	if c := f.progressing(t, "lobby"); c.Reason != spawneryv1alpha1.ReasonWaitingForEarlierStage ||
		c.Message != "waiting for stage -10: gateway" {
		t.Fatalf("lobby Progressing = %s %q", c.Reason, c.Message)
	}

	// The new proxy pods come up; reconcile so the old ones are marked. Leave
	// the old pods in place, draining, as if a player stayed on them.
	pods := f.proxyPods("gateway")
	for i := range pods {
		if pods[i].Spec.Containers[0].Image == nextProxyImage {
			f.markProxyPodReady(t, &pods[i])
		}
	}
	f.reconcileProxyGroup(pr, "gateway")
	f.reconcileProxyGroup(pr, "gateway")
	if got := f.proxyGroup("gateway").Status.Changeover; got != spawneryv1alpha1.ChangeoverDeferred {
		t.Fatalf("gateway status.changeover = %q, want Deferred while its old pod drains", got)
	}

	f.reconcileNamedGroup(t, gr, "lobby")
	if n := len(f.serverNamesOfGroup(t, "lobby")); n != 2 {
		t.Fatalf("lobby has %d servers, want 2: the proxy stage stands", n)
	}
}
```

Adapt the "which pods are new" selection to how `readyProxyGroup`/`proxyPods` expose image or hash in this fixture (read `proxygroup_controller_test.go:95` and the helpers around `markProxyPodReady`); the old pod must stay present and must carry a player count >0 if the fixture's agent registry decides draining by players — otherwise it is deleted at once and the test would not show the idle-player case. Also add:

```go
func TestAProxyStageGatesWithTheBudgetUnset(t *testing.T) {
	// A proxy group in stage 10 and a server group in stage 0, both stale,
	// no budget: the proxy group creates no surge pod and its ChangingOver
	// message is "waiting for stage 0: lobby".
}
```

written out in full like the one above.

- [ ] **Step 2: Run, expect failures.**

- [ ] **Step 3: Implement**

`proxyChangeover`:

```go
func proxyChangeover(
	ctx context.Context, c client.Reader, network *spawneryv1alpha1.Network,
	group *spawneryv1alpha1.ProxyGroup, wantHash string, pendingCreates int32,
) (spawneryv1alpha1.ChangeoverState, bool, ChangeoverWait, error) {
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods, client.InNamespace(group.Namespace),
		client.MatchingLabels(podspec.ProxyLabels(network.Name, group.Name))); err != nil {
		return "", false, ChangeoverWait{}, err
	}
	own := ownProxyChangeover(pods.Items, wantHash, pendingCreates, group.Spec.Replicas, group.Status.Changeover)
	if own != spawneryv1alpha1.ChangeoverWaiting {
		return own, true, ChangeoverWait{}, nil
	}
	siblings, err := changeoverSiblings(ctx, c, group.Namespace, network.Name, "ProxyGroup", group.Name)
	if err != nil {
		return "", false, ChangeoverWait{}, err
	}
	self := ChangeoverView{
		Kind: "ProxyGroup", Name: group.Name, State: own,
		Failing: changeoverFailing(group.Status.Conditions), Stage: group.Spec.ChangeoverStage,
	}
	budget := network.ChangeoverBudget()
	groups := append(slices.Clip(siblings), self)
	if AdmitChangeovers(groups, budget)[changeoverKey("ProxyGroup", group.Name)] {
		return own, true, ChangeoverWait{}, nil
	}
	return own, false, describeWait(groups, budget, self), nil
}
```

Caller (line 962): `own, surgeAllowed, wait, err := proxyChangeover(...)`; `reportChangingOver(group, pods, wantHash, wait)`; in `reportChangingOver` replace the `waitingFor` branch with `if wait.Message != "" { cond.Message = wait.Message }`. Update the existing proxy budget envtest expectations only if their message text changed (it must not: the budget message is identical).

- [ ] **Step 4: Run** `nd go test ./internal/controller/ -count=1` → PASS.

- [ ] **Step 5: Commit** — `feat(controller): proxy groups wait for their stage`

---

### Task 8: Docs

**Files:**
- Modify: `docs/guides/updates-and-drain.md` (sections "Changing over a whole network" ~line 121 and "Proxies wait for their players too" ~line 194)

- [ ] **Step 1:** In "Changing over a whole network", after the budget's race paragraph, add a subsection `### Stages` with: the YAML of spec §2 (`edge` -10, `arena` 10), the rule (a group waits while a lower stage is changing over; one stage changes over together within the budget; works without a budget), when a group stops gating (`Deferred`: its new generation stands — for proxies when `replicas` new pods are Ready, for `RollingUpdate` when every old server is retiring), that failing groups do not gate, the `WaitingForEarlierStage` example output (`waiting for stage -10: edge`), and the memory cost of `Deferred` releasing a place while old pods still run. Fix the sentence "server groups and proxy groups are admitted together, by name" → "by stage, then by name". Update "Such a group takes a network changeover place only for its first new server" (WhenEmpty paragraph) only if it now reads wrong.

- [ ] **Step 2:** Rewrite "Proxies wait for their players too": a stale proxy is replaced blue/green — every old proxy gets its replacement first, and once `replicas` new ones are Ready all old ones drain together; up to `replicas` extra pods during a roll; a lowered `replicas` drains its surplus as today. Delete "Proxies drain one at a time, so a roll of a group of N proxies waits N times…" and the following sentence about holding the place; replace with: the group is `Deferred` once its new pods stand, so it holds neither a budget place nor a later stage while its old proxies drain. Keep `maxStaleSeconds` as the bound on the drain itself.

- [ ] **Step 3:** `nd make test` (runs the docs length linter) → PASS. If `hack/docs-length.sh` complains, cut elsewhere in the same guide rather than raising the limit.

- [ ] **Step 4: Commit** — `feat(docs): changeover stages and the blue/green proxy roll`

---

### Task 9: Real-system check (tutorial e2e) and the bite proof

**Files:**
- Modify: `test/e2e/tutorial_test.go` (new test beside `TestTutorialPlayableSlots`)

- [ ] **Step 1: Write the test**, gated like its neighbours (`SPAWNERY_E2E_TUTORIAL=1`). Read `TestTutorialPath` and `TestTutorialPlayableSlots` for `applyManifest`, `eventuallyIn`, the client and namespace constants, and how they wait. Shape:

```go
func TestTutorialChangeoverStages(t *testing.T) {
	if os.Getenv("SPAWNERY_E2E_TUTORIAL") != "1" {
		t.Skip("set SPAWNERY_E2E_TUTORIAL=1; hack/e2e-tutorial.sh does this nightly")
	}
	// 1. Wait for lobby Ready and gateway's pod Ready (as TestTutorialPath does).
	// 2. Patch gateway: spec.changeoverStage = -10.
	// 3. In one go, append env STAGE_PROBE=<now> to gateway and to lobby.
	// 4. Poll every second for up to 5 minutes, recording:
	//    - proxyStood: the first time gateway's status.changeover is Deferred or "".
	//    - lobbyBegan: the creation time of the first lobby Server whose
	//      spec carries the new pod hash (Server.status/labels — whichever
	//      carries podHash; read internal/controller for the field).
	//    - while lobbyBegan is unset, assert lobby's Progressing reason is
	//      WaitingForEarlierStage whenever gateway is Begun or Waiting.
	// 5. Assert lobbyBegan is after proxyStood, and that both groups end with
	//    status.changeover "".
}
```

Write it out fully with the package's helpers; record timestamps with `t.Logf` so the run log shows the order.

- [ ] **Step 2: Run it on paul-desktop:**

```bash
hostname   # must be paul-desktop
systemd-run --scope --user --property=Delegate=yes -- \
  nix --extra-experimental-features 'nix-command flakes' develop /home/paul/git/spawnery \
  -c env KIND_EXPERIMENTAL_PROVIDER=podman CONTAINER=podman \
  go test -tags e2e ... # use exactly what hack/e2e-tutorial.sh runs; prefer running the script itself with -run narrowed if it supports it
```

Read `hack/e2e-tutorial.sh` for how it selects tests; run it in the background and read its output file when it finishes. Expected: PASS, the log shows the gateway standing before the lobby's first new server.

- [ ] **Step 3: Bite proof in a throwaway worktree:**

```bash
git worktree add --detach /tmp/claude-1000/stages-mutant HEAD
# in the worktree: make earliestStageBefore return (0, nil, false) always
# run the same tutorial e2e from the worktree; record the FAIL lines
git worktree remove --force /tmp/claude-1000/stages-mutant
```

Paste the failing assertion into the commit body.

- [ ] **Step 4: Full suite** — `nd make test` and `nd make lint` → PASS.

- [ ] **Step 5: Commit** — `test(e2e): the tutorial network changes over by stage`, body with the PASS log excerpt and the mutant's FAIL.

---

## After the plan

Open a PR (`feat/changeover-stages` → `master`), not merged without Paul's word. The release is a **minor** step (new CRD field, changed proxy roll): `operatorVersion` in `flake.nix`, chart `version`/`appVersion`, `make manifests` for `docs/reference/chart-values.md` — done in the release commit when Paul asks for the release, not in this branch. Point out `/code-review ultra` for this branch (Go API, controller semantics across two reconcilers, e2e).
