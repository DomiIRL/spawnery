# /cloud status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An admin in game types `/cloud status [group|server|proxy]` and sees the network's CPU and memory against its requests and limits, per group and per instance, with each server's TPS and MSPT.

**Architecture:** The Paper agent adds TPS and MSPT to its periodic `PlayerCount`; the operator keeps them in `agent.Registry` beside the player count. A new `StatusRequest` is answered by a new package `internal/netstatus`, which reads groups, servers and pods from the manager's cache, ticks from the registry, and one live `PodMetrics` list from `metrics.k8s.io` over a raw REST call (no new Go module). The agents turn the answer into Java records and `/cloud status` renders them.

**Tech Stack:** Go (controller-runtime, envtest), protobuf, Java 21 API, Kotlin agents (Gradle via Nix).

**Spec:** `docs/superpowers/specs/2026-09-26-cloud-status-design.md`

## Global Constraints

- Scope is the requesting network's namespace. Nothing cluster-scoped (nodes, other namespaces) appears in any answer.
- The answer honours the requester's audience exactly as `netstate.Build` does: for a server agent (`netstate.ForServers`) on-demand groups and private servers (`netstate.IsPrivateServer`) are neither listed nor resolvable as a target; their pods still count in the network total and in `other`.
- Permission: `spawnery.cloud.status` for all three forms; the `/cloud` root accepts it too.
- Name resolution order: server group, proxy group, server, proxy pod. Unknown: NOT_FOUND "no group, server or proxy by that name is on this network".
- TPS colours: green at ≥ 19, yellow at ≥ 15, red below. A group's TPS is the lowest of its servers that reported one.
- TPS/MSPT: 0 means "not reported". Values are shown only while the report is fresh (`!Snapshot.PlayersStale`). Accepted ranges: finite, `0 ≤ tps ≤ 100`, `0 ≤ mspt ≤ 600000`; a report outside them is discarded (players still recorded).
- TPS/MSPT are never written to `Server.status`.
- A failing metrics call never fails the request: `metrics_available = false`, usage zero, `pods_measured = 0`.
- Pods counted for usage: phase `Pending` or `Running`. Requests and limits sum `spec.containers` (not init containers); a container without a CPU (memory) limit sets `cpu_unlimited` (`memory_unlimited`).
- RBAC: `metrics.k8s.io` `pods` `list` only (the code never gets one); marker, `rbacaudit.RequiredCluster` entry, regenerated `config/rbac/role.yaml` and chart `rbac.yaml`.
- Proto field numbers: `PlayerCount.tps = 3`, `PlayerCount.mspt = 4`, `CloudRequest.status = 11`, `CloudResponse.status = 12`.
- `agent/api` keeps only `java.*` and `cloud.spawnery.agent.api.*` in public signatures (`PackagingInvariantTest`).
- Generated files are committed: `make manifests generate proto`; new files are `git add`ed before `make agent` (Nix reads the index).
- Commits: Conventional Commits with scope, body wrapped at 72, signed, ending with the session trailers.
- Nothing about a particular network or consumer goes into this public repo; examples use `lobby`, `arena`, `gateway`.
- New files carry the `Copyright paul_wtf.` Apache header used everywhere in the repo.

## Rulings against the spec (made while planning)

- §5 lists `get` and `list` on `pods.metrics.k8s.io`; only `list` is used, so only `list` is granted.
- §5's `pods_without_metrics` is replaced by `pods` and `pods_measured` on every `ResourceUsage`, so each group line can say "usage of 8 of 9 pods" too; `StatusResult` keeps `metrics_available`.
- §5's single "no limit" flag becomes `cpu_unlimited` and `memory_unlimited`.
- New: `StatusResult.other`, the usage of namespace pods outside every listed group (databases, sidecar workloads, and for a server agent the hidden on-demand groups), so the group lines add up to the total. Rendered as an `other pods` line when `other.pods > 0`.
- New: `StatusResult.players/servers/proxies` for the header line. `players` is the sum of the listed proxy groups' connected players (every player is on a proxy).
- §6's `OptionalDouble` for usage becomes `ResourceUsage.measured()` (`podsMeasured > 0`); TPS and MSPT are `OptionalDouble`.

## Commands

```bash
NIX="nix --extra-experimental-features 'nix-command flakes' develop /home/paul/git/spawnery -c"
$NIX go -C /home/paul/git/spawnery test ./internal/netstatus/ -run 'TestName' -count=1
$NIX make -C /home/paul/git/spawnery manifests generate proto
$NIX make -C /home/paul/git/spawnery agent          # both plugins and their JUnit suites (git add first)
# whole suite on the development VM (8 cores, 12 GB):
$NIX make -C /home/paul/git/spawnery manifests generate fmt vet chart-lint toolchain-lint image-tag-lint docs-length-lint crd-docs-test chart-values-docs-test metrics-docs-test
$NIX go -C /home/paul/git/spawnery test -race -p 1 ./...
```

## Review Focus

1. **A server agent asks about a private server or an on-demand group by name** — it must get NOT_FOUND, exactly as if the name did not exist, and the network form must not list them. Pinned in Task 3 (`TestStatusHidesPrivateFromServers`).
2. **metrics-server absent or returning 404/503** — the answer still arrives with groups, servers, TPS, and `metrics_available = false`; the command says usage is unavailable instead of printing zeros as if measured. Pinned in Task 3 (`TestStatusWithoutMetrics`) and Task 7 (`status says when usage is unavailable`).
3. **A pod with no metrics sample yet (just started)** — totals leave it out and say "of N pods"; it is not shown as using 0. Pinned in Task 3 (`TestStatusCountsUnmeasuredPods`) and Task 7.
4. **An older agent or a proxy reports `tps = 0`** — shown as `TPS –` and ignored for the group's lowest TPS, never as a red 0.0. Pinned in Task 3 (`TestLowestTPSIgnoresUnreported`) and Task 7.
5. **A stale report (agent stream gone for longer than twice the report interval)** — its TPS is dropped, not shown as the last value forever. Pinned in Task 3 (`TestStaleTicksAreDropped`).

---

### Task 1: TPS and MSPT on the wire and in the registry

**Files:**
- Modify: `proto/spawnery/agent/v1alpha1/agent.proto` (PlayerCount; new status messages; oneof entries)
- Regenerate: `internal/agentpb/agent.pb.go`, `agent/common/src/proto/java/**`
- Modify: `internal/agent/registry.go` (entry, Snapshot, `ReportTicks`, `Lookup`)
- Modify: `internal/agentserver/server.go:603-610` (server `PlayerCount` branch)
- Test: `internal/agent/registry_test.go`

**Interfaces:**
- Produces: `func (r *Registry) ReportTicks(key string, tps, mspt float64) error`; `Snapshot.TPS float64`, `Snapshot.MSPT float64`; Go types `agentpb.StatusRequest`, `agentpb.StatusResult`, `agentpb.ResourceUsage`, `agentpb.GroupStatus`, `agentpb.InstanceStatus`, `agentpb.CloudRequest_Status`, `agentpb.CloudResponse_Status`; Java `cloud.spawnery.agent.pb.StatusRequest` etc.

The whole proto change lands here so the generated code moves once.

- [ ] **Step 1: Extend the proto**

In `PlayerCount`:

```proto
message PlayerCount {
  int32 players = 1;
  int32 slots = 2;
  // Server agents only: the server's one-minute TPS average and its mean tick
  // duration in milliseconds. 0 means not reported -- what a proxy and an
  // agent older than these fields send.
  double tps = 3;
  double mspt = 4;
}
```

In `CloudRequest`'s oneof after `unretire = 10`: `StatusRequest status = 11;`. In `CloudResponse`'s oneof after `unretire = 11`: `StatusResult status = 12;`.

After `UnretireResult`, add:

```proto
// StatusRequest asks how the network is doing: its CPU and memory against
// what it asked for, and each server's tick rate. Namespace-bound like every
// request; nothing cluster-scoped is ever in the answer.
message StatusRequest {
  // Empty for the whole network, else a server group, a proxy group, a
  // server or a proxy, looked up in that order.
  string target = 1;
}

// ResourceUsage is a set of pods: what they use, and what they asked for.
message ResourceUsage {
  int64 cpu_used_millicores = 1;
  int64 cpu_requested_millicores = 2;
  int64 cpu_limit_millicores = 3;
  // Some container has no CPU limit, so cpu_limit_millicores is a floor.
  bool cpu_unlimited = 4;
  int64 memory_used_bytes = 5;
  int64 memory_requested_bytes = 6;
  int64 memory_limit_bytes = 7;
  bool memory_unlimited = 8;
  int32 pods = 9;
  // Pods with a metrics sample; the used figures cover these only.
  int32 pods_measured = 10;
}

message GroupStatus {
  string name = 1;
  GroupState.Kind kind = 2;
  string phase = 3;
  int32 replicas = 4;
  int32 ready_replicas = 5;
  int32 players = 6;
  // The lowest TPS among the group's servers that reported one; 0 if none did.
  double lowest_tps = 7;
  ResourceUsage usage = 8;
}

message InstanceStatus {
  string name = 1;
  string group = 2;
  bool proxy = 3;
  // A server's phase; empty for a proxy, whose state is ready and draining.
  string phase = 4;
  bool ready = 5;
  int32 players = 6;
  int32 slots = 7;
  double tps = 8;   // 0 = not reported
  double mspt = 9;  // 0 = not reported
  int64 age_seconds = 10;
  bool retiring = 11;
  bool held = 12;
  bool draining = 13;
  ResourceUsage usage = 14;
}

message StatusResult {
  // The whole network, the target group, or the target instance.
  ResourceUsage total = 1;
  repeated GroupStatus groups = 2;
  repeated InstanceStatus instances = 3;
  // Pods of the namespace outside every listed group. Network form only.
  ResourceUsage other = 4;
  bool metrics_available = 5;
  int32 players = 6;
  int32 servers = 7;
  int32 proxies = 8;
}
```

- [ ] **Step 2: Regenerate**

Run: `$NIX make -C /home/paul/git/spawnery proto && $NIX go -C /home/paul/git/spawnery build ./...`
Expected: exit 0; `git -C /home/paul/git/spawnery status --short` lists `internal/agentpb/agent.pb.go` and new files under `agent/common/src/proto/java/cloud/spawnery/agent/pb/` (`StatusRequest.java`, `StatusResult.java`, `ResourceUsage.java`, `GroupStatus.java`, `InstanceStatus.java` and their `OrBuilder`s).

- [ ] **Step 3: Write the failing registry tests**

Append to `internal/agent/registry_test.go` (use the file's existing clock helper; if it has none, the literal below works):

```go
func TestReportTicksShowsInSnapshot(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(func() time.Time { return now }, 5*time.Second, now)
	r.Connect("pod-a", RoleServer)
	if err := r.ReportPlayers("pod-a", 2, 20); err != nil {
		t.Fatal(err)
	}
	if err := r.ReportTicks("pod-a", 19.5, 12.25); err != nil {
		t.Fatal(err)
	}
	snap := r.Lookup("pod-a")
	if snap.TPS != 19.5 || snap.MSPT != 12.25 {
		t.Fatalf("snapshot TPS/MSPT = %v/%v, want 19.5/12.25", snap.TPS, snap.MSPT)
	}
}

func TestReportTicksRefusesImpossibleValues(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(func() time.Time { return now }, 5*time.Second, now)
	r.Connect("pod-a", RoleServer)
	for _, c := range []struct{ tps, mspt float64 }{
		{-1, 10}, {101, 10}, {20, -1}, {20, 600001}, {math.NaN(), 10}, {20, math.Inf(1)},
	} {
		if err := r.ReportTicks("pod-a", c.tps, c.mspt); err == nil {
			t.Errorf("ReportTicks(%v, %v) accepted", c.tps, c.mspt)
		}
	}
	if snap := r.Lookup("pod-a"); snap.TPS != 0 || snap.MSPT != 0 {
		t.Fatalf("a refused report was kept: %v/%v", snap.TPS, snap.MSPT)
	}
}

func TestReportTicksNeedsALiveStream(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(func() time.Time { return now }, 5*time.Second, now)
	if err := r.ReportTicks("nobody", 20, 10); err == nil {
		t.Fatal("ReportTicks accepted a pod with no stream")
	}
}
```

Add `"math"` to the imports if missing.

- [ ] **Step 4: Run them to verify they fail**

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/agent/ -run 'TestReportTicks' -count=1`
Expected: FAIL to compile, `r.ReportTicks undefined` and `snap.TPS undefined`.

- [ ] **Step 5: Implement**

In `internal/agent/registry.go`:

Add to `Snapshot` after `Slots`:

```go
	// TPS and MSPT are what a server agent last reported about its tick rate;
	// zero when it never did. Fresh exactly when the player count is: they
	// arrive in the same report.
	TPS  float64
	MSPT float64
```

Add to `entry` after `slots`:

```go
	tps            float64
	mspt           float64
```

After `ReportPlayers`:

```go
// ReportTicks records a server's tick rate from the same report as its player
// count. Values no server can produce are refused, as a count above capacity is.
func (r *Registry) ReportTicks(key string, tps, mspt float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.entries[key]
	if !ok || !e.connected {
		return fmt.Errorf("no live stream for %q", key)
	}
	if math.IsNaN(tps) || math.IsNaN(mspt) || tps < 0 || tps > 100 || mspt < 0 || mspt > 600000 {
		return fmt.Errorf("impossible tick report for %q: %v TPS, %v ms", key, tps, mspt)
	}
	e.tps = tps
	e.mspt = mspt
	return nil
}
```

(`math.IsInf(x, 1)` is caught by the upper bounds; `-Inf` by the lower.) Add `"math"` to the imports.

In `Lookup`'s known-entry `Snapshot{...}` literal add `TPS: e.tps, MSPT: e.mspt,`.

In `internal/agentserver/server.go`, the server `PlayerCount` branch becomes:

```go
	case *agentpb.ServerMessage_PlayerCount:
		if err := s.opts.Agents.ReportPlayers(id.PodUID,
			m.PlayerCount.GetPlayers(), m.PlayerCount.GetSlots()); err != nil {
			// Discard, keep the stream. Spec 5.2: dropping it would be a
			// reconnect loop the agent could trigger at will.
			RejectedReports.WithLabelValues(string(agent.RoleServer)).Inc()
			logger.V(1).Info("discarded a player count", "reason", err.Error())
		} else if err := s.opts.Agents.ReportTicks(id.PodUID,
			m.PlayerCount.GetTps(), m.PlayerCount.GetMspt()); err != nil {
			RejectedReports.WithLabelValues(string(agent.RoleServer)).Inc()
			logger.V(1).Info("discarded a tick report", "reason", err.Error())
		}
```

The proxy branch stays as it is: proxies send 0 and nothing reads it.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/agent/ ./internal/agentserver/ -count=1 -p 1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git -C /home/paul/git/spawnery add proto internal/agentpb agent/common/src/proto internal/agent internal/agentserver/server.go
git -C /home/paul/git/spawnery commit -m "feat(agentserver): servers report TPS and MSPT, and the status messages exist

PlayerCount gains tps and mspt. The registry keeps them beside the
player count and refuses values outside 0-100 TPS and 0-600000 ms;
a refused tick report keeps the player count. The status request and
its result are declared here so the generated code moves once.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 2: Pod metrics over the metrics API

**Files:**
- Create: `internal/netstatus/metrics.go`
- Test: `internal/netstatus/metrics_test.go`

**Interfaces:**
- Produces:
  ```go
  type Usage struct{ CPUMilli, MemoryBytes int64 }
  type MetricsReader interface {
      PodUsage(ctx context.Context, namespace string) (map[string]Usage, error) // keyed by pod name
  }
  type APIMetrics struct{ REST rest.Interface }
  ```

- [ ] **Step 1: Write the failing test**

`internal/netstatus/metrics_test.go` (header as every Go file):

```go
package netstatus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func metricsFrom(t *testing.T, status int, body string) APIMetrics {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/metrics.k8s.io/v1beta1/namespaces/games/pods" {
			t.Errorf("asked %s, want the games namespace's pod metrics", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return APIMetrics{REST: cs.Discovery().RESTClient()}
}

func TestPodUsageSumsContainers(t *testing.T) {
	m := metricsFrom(t, http.StatusOK, `{"items":[
	  {"metadata":{"name":"lobby-a"},"containers":[
	    {"name":"server","usage":{"cpu":"250m","memory":"1Gi"}},
	    {"name":"sidecar","usage":{"cpu":"1500000n","memory":"64Mi"}}]},
	  {"metadata":{"name":"gateway-b"},"containers":[
	    {"name":"proxy","usage":{"cpu":"1","memory":"512Mi"}}]}]}`)

	got, err := m.PodUsage(context.Background(), "games")
	if err != nil {
		t.Fatal(err)
	}
	// 250m + 1.5m, summed as quantities and rounded up once.
	if u := got["lobby-a"]; u.CPUMilli != 252 || u.MemoryBytes != (1<<30)+(64<<20) {
		t.Errorf("lobby-a = %+v, want 252m and 1Gi+64Mi", u)
	}
	if u := got["gateway-b"]; u.CPUMilli != 1000 || u.MemoryBytes != 512<<20 {
		t.Errorf("gateway-b = %+v, want 1000m and 512Mi", u)
	}
}

func TestPodUsageFailsWithoutTheAPI(t *testing.T) {
	m := metricsFrom(t, http.StatusNotFound, `{"kind":"Status","code":404}`)
	if _, err := m.PodUsage(context.Background(), "games"); err == nil {
		t.Fatal("a missing metrics API read as an empty network")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/netstatus/ -run TestPodUsage -count=1`
Expected: FAIL to compile, `undefined: APIMetrics`.

- [ ] **Step 3: Implement**

`internal/netstatus/metrics.go`:

```go
// Package netstatus answers /cloud status: what a network's pods use against
// what they asked for, and how its servers keep up with the tick rate.
package netstatus

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/rest"
)

// Usage is what one pod, or a set of them, uses right now.
type Usage struct {
	CPUMilli    int64
	MemoryBytes int64
}

// MetricsReader lists the current usage of every pod in a namespace, keyed by
// pod name. A pod without a sample is absent, which is not the same as zero.
type MetricsReader interface {
	PodUsage(ctx context.Context, namespace string) (map[string]Usage, error)
}

// APIMetrics reads metrics.k8s.io with a plain GET rather than the typed
// client from k8s.io/metrics, which would be a new module for one list call.
// The list is live and never cached: the manager's cache cannot watch it.
type APIMetrics struct {
	REST rest.Interface
}

type podMetricsList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Containers []struct {
			Usage corev1.ResourceList `json:"usage"`
		} `json:"containers"`
	} `json:"items"`
}

func (m APIMetrics) PodUsage(ctx context.Context, namespace string) (map[string]Usage, error) {
	raw, err := m.REST.Get().
		AbsPath("/apis/metrics.k8s.io/v1beta1/namespaces", namespace, "pods").
		DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pod metrics in %s: %w", namespace, err)
	}
	var list podMetricsList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode pod metrics in %s: %w", namespace, err)
	}
	out := make(map[string]Usage, len(list.Items))
	for _, item := range list.Items {
		var cpu, mem resource.Quantity
		for _, c := range item.Containers {
			if q, ok := c.Usage[corev1.ResourceCPU]; ok {
				cpu.Add(q)
			}
			if q, ok := c.Usage[corev1.ResourceMemory]; ok {
				mem.Add(q)
			}
		}
		out[item.Metadata.Name] = Usage{CPUMilli: cpu.MilliValue(), MemoryBytes: mem.Value()}
	}
	return out, nil
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/netstatus/ -run TestPodUsage -count=1`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git -C /home/paul/git/spawnery add internal/netstatus
git -C /home/paul/git/spawnery commit -m "feat(netstatus): read a namespace's pod metrics

metrics.k8s.io is read with a plain GET and decoded into a small
struct, so no new Go module is needed for one list call. Container
usage is summed as quantities and rounded once per pod.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 3: The status answer

**Files:**
- Create: `internal/netstatus/status.go`
- Test: `internal/netstatus/status_test.go`

**Interfaces:**
- Consumes: `Usage`, `MetricsReader` (Task 2); `agent.Snapshot.TPS/MSPT` (Task 1); `agentpb.StatusResult` and friends (Task 1); `netstate.Audience`, `netstate.ForServers`, `netstate.ForProxies`, `netstate.IsPrivateServer`, `podspec.LabelRole/LabelGroup/RoleServer/RoleProxy`, `podspec.AnnotationProxyDrainingSince`, `podspec.AnnotationRetireRequested`, `phase.Retiring`.
- Produces:
  ```go
  var ErrUnknownTarget = errors.New("no group, server or proxy by that name")
  type Source struct {
      Reader  client.Reader
      Agents  *agent.Registry
      Metrics MetricsReader
      Clock   func() time.Time
  }
  func (s Source) Status(ctx context.Context, namespace string, audience netstate.Audience, target string) (*agentpb.StatusResult, error)
  ```

- [ ] **Step 1: Write the failing tests**

`internal/netstatus/status_test.go`:

```go
package netstatus

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	spawneryv1alpha1 "github.com/spawnery/spawnery/api/v1alpha1"
	"github.com/spawnery/spawnery/internal/agent"
	"github.com/spawnery/spawnery/internal/agentpb"
	"github.com/spawnery/spawnery/internal/netstate"
	"github.com/spawnery/spawnery/internal/podspec"
)

const ns = "games"

var t0 = time.Unix(10_000, 0)

type fixedMetrics struct {
	usage map[string]Usage
	err   error
}

func (f fixedMetrics) PodUsage(context.Context, string) (map[string]Usage, error) {
	return f.usage, f.err
}

func pod(name, role, group, cpuReq, cpuLim, memReq, memLim string, uid string) *corev1.Pod {
	res := corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
	if cpuReq != "" {
		res.Requests[corev1.ResourceCPU] = resource.MustParse(cpuReq)
	}
	if cpuLim != "" {
		res.Limits[corev1.ResourceCPU] = resource.MustParse(cpuLim)
	}
	if memReq != "" {
		res.Requests[corev1.ResourceMemory] = resource.MustParse(memReq)
	}
	if memLim != "" {
		res.Limits[corev1.ResourceMemory] = resource.MustParse(memLim)
	}
	labels := map[string]string{}
	if role != "" {
		labels[podspec.LabelRole] = role
		labels[podspec.LabelGroup] = group
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels, UID: k8stypes.UID(uid),
			CreationTimestamp: metav1.NewTime(t0.Add(-90 * time.Minute))},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Resources: res}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}
```

The network every test starts from, built by one helper:

```go
// network: server group lobby (lobby-a, lobby-b), on-demand group rooms with
// the private server rooms-x, proxy group gateway (gateway-a), and one pod
// that belongs to no group (db-0).
func network(t *testing.T) (client.Client, *agent.Registry) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := spawneryv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	lobby := &spawneryv1alpha1.ServerGroup{ObjectMeta: metav1.ObjectMeta{Name: "lobby", Namespace: ns}}
	lobby.Spec.Type = spawneryv1alpha1.ServerGroupEphemeral
	lobby.Status.Phase = "Ready"
	lobby.Status.Replicas, lobby.Status.ReadyReplicas, lobby.Status.OnlinePlayers = 2, 2, 5
	rooms := &spawneryv1alpha1.ServerGroup{ObjectMeta: metav1.ObjectMeta{Name: "rooms", Namespace: ns}}
	rooms.Spec.Type = spawneryv1alpha1.ServerGroupOnDemand
	gateway := &spawneryv1alpha1.ProxyGroup{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: ns}}
	gateway.Spec.Replicas = 1
	gateway.Status.Phase, gateway.Status.ReadyReplicas, gateway.Status.ConnectedPlayers = "Ready", 1, 6

	server := func(name, group, podUID, ph string) *spawneryv1alpha1.Server {
		s := &spawneryv1alpha1.Server{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
			CreationTimestamp: metav1.NewTime(t0.Add(-2 * time.Hour))}}
		s.Spec.GroupRef.Name = group
		s.Status.Phase, s.Status.PodName, s.Status.PodUID = ph, name, podUID
		s.Status.Players, s.Status.Slots = 2, 20
		return s
	}
	lobbyA := server("lobby-a", "lobby", "uid-la", "Ready")
	lobbyB := server("lobby-b", "lobby", "uid-lb", "Retiring")
	lobbyB.Spec.Retire = true
	roomsX := server("rooms-x", "rooms", "uid-rx", "Ready")
	roomsX.Spec.Key = "somebody"

	gw := pod("gateway-a", podspec.RoleProxy, "gateway", "100m", "500m", "256Mi", "512Mi", "uid-ga")
	gw.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		lobby, rooms, gateway, lobbyA, lobbyB, roomsX,
		pod("lobby-a", podspec.RoleServer, "lobby", "500m", "1", "1Gi", "2Gi", "uid-la"),
		pod("lobby-b", podspec.RoleServer, "lobby", "500m", "1", "1Gi", "2Gi", "uid-lb"),
		pod("rooms-x", podspec.RoleServer, "rooms", "250m", "", "512Mi", "1Gi", "uid-rx"),
		gw,
		pod("db-0", "", "", "200m", "", "256Mi", "", "uid-db"),
	).Build()

	reg := agent.New(func() time.Time { return t0 }, 5*time.Second, t0)
	for _, key := range []string{"uid-la", "uid-lb", "uid-rx", "uid-ga"} {
		reg.Connect(key, agent.RoleServer)
	}
	report := func(key string, players, slots int32, tps, mspt float64) {
		if err := reg.ReportPlayers(key, players, slots); err != nil {
			t.Fatal(err)
		}
		if err := reg.ReportTicks(key, tps, mspt); err != nil {
			t.Fatal(err)
		}
	}
	report("uid-la", 2, 20, 19.9, 8)
	report("uid-lb", 3, 20, 16.5, 42)
	report("uid-rx", 1, 10, 20, 3)
	report("uid-ga", 6, 500, 0, 0)
	return c, reg
}

func allMeasured() fixedMetrics {
	return fixedMetrics{usage: map[string]Usage{
		"lobby-a":   {CPUMilli: 400, MemoryBytes: 1 << 30},
		"lobby-b":   {CPUMilli: 700, MemoryBytes: 3 << 29},
		"rooms-x":   {CPUMilli: 100, MemoryBytes: 1 << 28},
		"gateway-a": {CPUMilli: 50, MemoryBytes: 1 << 28},
		"db-0":      {CPUMilli: 20, MemoryBytes: 1 << 27},
	}}
}

func status(t *testing.T, m MetricsReader, audience netstate.Audience, target string) (*agentpb.StatusResult, error) {
	t.Helper()
	c, reg := network(t)
	return Source{Reader: c, Agents: reg, Metrics: m, Clock: func() time.Time { return t0 }}.
		Status(context.Background(), ns, audience, target)
}

func groupNamed(res *agentpb.StatusResult, name string) *agentpb.GroupStatus {
	for _, g := range res.GetGroups() {
		if g.GetName() == name {
			return g
		}
	}
	return nil
}
```

The tests:

```go
func TestStatusNetworkForProxies(t *testing.T) {
	res, err := status(t, allMeasured(), netstate.ForProxies, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetMetricsAvailable() {
		t.Fatal("metrics_available false with a working metrics API")
	}
	tot := res.GetTotal()
	if tot.GetPods() != 5 || tot.GetPodsMeasured() != 5 {
		t.Errorf("total pods %d/%d, want 5/5", tot.GetPodsMeasured(), tot.GetPods())
	}
	if tot.GetCpuUsedMillicores() != 1270 || tot.GetCpuRequestedMillicores() != 1550 {
		t.Errorf("total cpu used/requested %d/%d, want 1270/1550",
			tot.GetCpuUsedMillicores(), tot.GetCpuRequestedMillicores())
	}
	if !tot.GetCpuUnlimited() || !tot.GetMemoryUnlimited() {
		t.Error("rooms-x and db-0 have no CPU limit and db-0 no memory limit, yet the total claims limits")
	}
	if len(res.GetGroups()) != 3 {
		t.Fatalf("groups = %v, want lobby, rooms and gateway", res.GetGroups())
	}
	lobby := groupNamed(res, "lobby")
	if lobby.GetLowestTps() != 16.5 || lobby.GetUsage().GetPods() != 2 || lobby.GetPlayers() != 5 {
		t.Errorf("lobby = %+v, want lowest TPS 16.5, 2 pods, 5 players", lobby)
	}
	gw := groupNamed(res, "gateway")
	if gw.GetKind() != agentpb.GroupState_PROXY || gw.GetLowestTps() != 0 {
		t.Errorf("gateway = %+v, want a proxy group without TPS", gw)
	}
	if o := res.GetOther(); o.GetPods() != 1 || o.GetCpuUsedMillicores() != 20 {
		t.Errorf("other = %+v, want db-0 alone", o)
	}
	if res.GetPlayers() != 6 || res.GetServers() != 3 || res.GetProxies() != 1 {
		t.Errorf("header %d players, %d servers, %d proxies; want 6, 3, 1",
			res.GetPlayers(), res.GetServers(), res.GetProxies())
	}
	if len(res.GetInstances()) != 0 {
		t.Error("the network form listed instances")
	}
}

func TestStatusHidesPrivateFromServers(t *testing.T) {
	res, err := status(t, allMeasured(), netstate.ForServers, "")
	if err != nil {
		t.Fatal(err)
	}
	if groupNamed(res, "rooms") != nil {
		t.Error("a server agent's answer lists the on-demand group")
	}
	if res.GetServers() != 2 {
		t.Errorf("servers = %d, want 2 without the private one", res.GetServers())
	}
	if res.GetTotal().GetPods() != 5 {
		t.Errorf("total pods = %d; hiding a name must not hide its usage from the total", res.GetTotal().GetPods())
	}
	if res.GetOther().GetPods() != 2 {
		t.Errorf("other pods = %d, want db-0 and rooms-x", res.GetOther().GetPods())
	}
	for _, target := range []string{"rooms", "rooms-x"} {
		if _, err := status(t, allMeasured(), netstate.ForServers, target); !errors.Is(err, ErrUnknownTarget) {
			t.Errorf("target %q from a server agent: err = %v, want ErrUnknownTarget", target, err)
		}
	}
	if _, err := status(t, allMeasured(), netstate.ForProxies, "rooms-x"); err != nil {
		t.Errorf("a proxy may ask about a private server: %v", err)
	}
}

func TestStatusGroupTarget(t *testing.T) {
	res, err := status(t, allMeasured(), netstate.ForProxies, "lobby")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetGroups()) != 1 || res.GetGroups()[0].GetName() != "lobby" {
		t.Fatalf("groups = %v, want lobby alone", res.GetGroups())
	}
	if len(res.GetInstances()) != 2 || res.GetInstances()[0].GetName() != "lobby-a" {
		t.Fatalf("instances = %v, want lobby-a and lobby-b in order", res.GetInstances())
	}
	b := res.GetInstances()[1]
	if !b.GetRetiring() || b.GetTps() != 16.5 || b.GetMspt() != 42 || b.GetPlayers() != 2 || b.GetSlots() != 20 {
		t.Errorf("lobby-b = %+v", b)
	}
	if b.GetAgeSeconds() != 90*60 {
		t.Errorf("age = %ds, want the pod's 5400", b.GetAgeSeconds())
	}
	if res.GetTotal().GetPods() != 2 {
		t.Errorf("a group's total covers its own pods: %d", res.GetTotal().GetPods())
	}
}

func TestStatusProxyTargets(t *testing.T) {
	res, err := status(t, allMeasured(), netstate.ForProxies, "gateway-a")
	if err != nil {
		t.Fatal(err)
	}
	in := res.GetInstances()
	if len(in) != 1 || !in[0].GetProxy() || !in[0].GetReady() || in[0].GetPlayers() != 6 {
		t.Fatalf("instances = %v, want gateway-a, ready, 6 players", in)
	}
	if in[0].GetUsage().GetCpuLimitMillicores() != 500 {
		t.Errorf("gateway-a limit = %d, want 500", in[0].GetUsage().GetCpuLimitMillicores())
	}
	res, err = status(t, allMeasured(), netstate.ForProxies, "gateway")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetInstances()) != 1 || res.GetGroups()[0].GetKind() != agentpb.GroupState_PROXY {
		t.Errorf("the proxy group form = %+v", res)
	}
}

func TestStatusUnknownTarget(t *testing.T) {
	if _, err := status(t, allMeasured(), netstate.ForProxies, "nowhere"); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("err = %v, want ErrUnknownTarget", err)
	}
}

func TestStatusWithoutMetrics(t *testing.T) {
	res, err := status(t, fixedMetrics{err: errors.New("the server could not find the requested resource")},
		netstate.ForProxies, "")
	if err != nil {
		t.Fatalf("a missing metrics API failed the request: %v", err)
	}
	if res.GetMetricsAvailable() {
		t.Error("metrics_available true without a metrics API")
	}
	if res.GetTotal().GetPodsMeasured() != 0 || res.GetTotal().GetCpuRequestedMillicores() != 1550 {
		t.Errorf("total = %+v, want requests but nothing measured", res.GetTotal())
	}
	if groupNamed(res, "lobby").GetLowestTps() != 16.5 {
		t.Error("TPS went missing with the metrics")
	}
}

func TestStatusCountsUnmeasuredPods(t *testing.T) {
	m := allMeasured()
	delete(m.usage, "lobby-b")
	res, err := status(t, m, netstate.ForProxies, "")
	if err != nil {
		t.Fatal(err)
	}
	if tot := res.GetTotal(); tot.GetPods() != 5 || tot.GetPodsMeasured() != 4 || tot.GetCpuUsedMillicores() != 570 {
		t.Errorf("total = %+v, want 4 of 5 measured and 570m used", tot)
	}
	if l := groupNamed(res, "lobby").GetUsage(); l.GetPods() != 2 || l.GetPodsMeasured() != 1 {
		t.Errorf("lobby usage = %+v, want 1 of 2 measured", l)
	}
}

func TestLowestTPSIgnoresUnreported(t *testing.T) {
	c, reg := network(t)
	if err := reg.ReportTicks("uid-lb", 0, 0); err != nil {
		t.Fatal(err)
	}
	res, err := Source{Reader: c, Agents: reg, Metrics: allMeasured(), Clock: func() time.Time { return t0 }}.
		Status(context.Background(), ns, netstate.ForProxies, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := groupNamed(res, "lobby").GetLowestTps(); got != 19.9 {
		t.Errorf("lowest TPS = %v, want 19.9: a server that reports none is not a server at 0", got)
	}
}

func TestStaleTicksAreDropped(t *testing.T) {
	c, _ := network(t)
	now := t0
	reg := agent.New(func() time.Time { return now }, 5*time.Second, t0)
	reg.Connect("uid-la", agent.RoleServer)
	if err := reg.ReportPlayers("uid-la", 1, 20); err != nil {
		t.Fatal(err)
	}
	if err := reg.ReportTicks("uid-la", 19, 9); err != nil {
		t.Fatal(err)
	}
	now = t0.Add(time.Minute)
	res, err := Source{Reader: c, Agents: reg, Metrics: allMeasured(), Clock: func() time.Time { return now }}.
		Status(context.Background(), ns, netstate.ForProxies, "lobby-a")
	if err != nil {
		t.Fatal(err)
	}
	if tps := res.GetInstances()[0].GetTps(); tps != 0 {
		t.Errorf("TPS = %v from a report a minute old, want 0 (not reported)", tps)
	}
}
```

Check the arithmetic against the fixture before running: CPU used 400+700+100+50+20 = 1270; requested 500+500+250+100+200 = 1550; with lobby-b unmeasured, used 1270−700 = 570.

- [ ] **Step 2: Run them to verify they fail**

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/netstatus/ -count=1`
Expected: FAIL to compile, `undefined: Source` and `undefined: ErrUnknownTarget`.

- [ ] **Step 3: Implement**

`internal/netstatus/status.go`:

```go
package netstatus

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	spawneryv1alpha1 "github.com/spawnery/spawnery/api/v1alpha1"
	"github.com/spawnery/spawnery/internal/agent"
	"github.com/spawnery/spawnery/internal/agentpb"
	"github.com/spawnery/spawnery/internal/netstate"
	"github.com/spawnery/spawnery/internal/phase"
	"github.com/spawnery/spawnery/internal/podspec"
)

// ErrUnknownTarget is a target that names nothing this audience may see.
var ErrUnknownTarget = errors.New("no group, server or proxy by that name")

// Source builds status answers from the manager's cache, the registry and the
// metrics API.
type Source struct {
	Reader  client.Reader
	Agents  *agent.Registry
	Metrics MetricsReader
	Clock   func() time.Time
}

type view struct {
	serverGroups []spawneryv1alpha1.ServerGroup
	proxyGroups  []spawneryv1alpha1.ProxyGroup
	servers      []spawneryv1alpha1.Server
	pods         map[string]*corev1.Pod
	usage        map[string]Usage
	available    bool
	now          time.Time
}

func (s Source) Status(ctx context.Context, namespace string, audience netstate.Audience, target string) (*agentpb.StatusResult, error) {
	v, err := s.read(ctx, namespace, audience)
	if err != nil {
		return nil, err
	}
	if target == "" {
		return v.network(s.Agents), nil
	}
	for i := range v.serverGroups {
		if g := &v.serverGroups[i]; g.Name == target {
			return v.serverGroup(s.Agents, g), nil
		}
	}
	for i := range v.proxyGroups {
		if g := &v.proxyGroups[i]; g.Name == target {
			return v.proxyGroup(s.Agents, g), nil
		}
	}
	for i := range v.servers {
		if srv := &v.servers[i]; srv.Name == target {
			in := v.server(s.Agents, srv)
			return &agentpb.StatusResult{Total: in.Usage, Instances: []*agentpb.InstanceStatus{in},
				MetricsAvailable: v.available}, nil
		}
	}
	if p, ok := v.pods[target]; ok && p.Labels[podspec.LabelRole] == podspec.RoleProxy && p.DeletionTimestamp.IsZero() {
		in := v.proxy(s.Agents, p)
		return &agentpb.StatusResult{Total: in.Usage, Instances: []*agentpb.InstanceStatus{in},
			MetricsAvailable: v.available}, nil
	}
	return nil, ErrUnknownTarget
}

// read lists everything once and drops what this audience may not see. Pods
// are never dropped: hiding a name must not hide its usage from the total.
func (s Source) read(ctx context.Context, namespace string, audience netstate.Audience) (*view, error) {
	v := &view{pods: map[string]*corev1.Pod{}, now: s.Clock()}
	var sgs spawneryv1alpha1.ServerGroupList
	if err := s.Reader.List(ctx, &sgs, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list server groups in %s: %w", namespace, err)
	}
	for _, g := range sgs.Items {
		if audience == netstate.ForServers && g.IsOnDemand() {
			continue
		}
		v.serverGroups = append(v.serverGroups, g)
	}
	var pgs spawneryv1alpha1.ProxyGroupList
	if err := s.Reader.List(ctx, &pgs, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list proxy groups in %s: %w", namespace, err)
	}
	v.proxyGroups = pgs.Items
	var servers spawneryv1alpha1.ServerList
	if err := s.Reader.List(ctx, &servers, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list servers in %s: %w", namespace, err)
	}
	for _, srv := range servers.Items {
		if audience == netstate.ForServers && netstate.IsPrivateServer(&srv) {
			continue
		}
		v.servers = append(v.servers, srv)
	}
	var pods corev1.PodList
	if err := s.Reader.List(ctx, &pods, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", namespace, err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase == corev1.PodPending || p.Status.Phase == corev1.PodRunning {
			v.pods[p.Name] = p
		}
	}
	usage, err := s.Metrics.PodUsage(ctx, namespace)
	v.usage, v.available = usage, err == nil
	sort.Slice(v.serverGroups, func(i, j int) bool { return v.serverGroups[i].Name < v.serverGroups[j].Name })
	sort.Slice(v.proxyGroups, func(i, j int) bool { return v.proxyGroups[i].Name < v.proxyGroups[j].Name })
	sort.Slice(v.servers, func(i, j int) bool { return v.servers[i].Name < v.servers[j].Name })
	return v, nil
}

// add counts one pod into u.
func (v *view) add(u *agentpb.ResourceUsage, p *corev1.Pod) {
	u.Pods++
	for _, c := range p.Spec.Containers {
		u.CpuRequestedMillicores += c.Resources.Requests.Cpu().MilliValue()
		u.MemoryRequestedBytes += c.Resources.Requests.Memory().Value()
		if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
			u.CpuLimitMillicores += q.MilliValue()
		} else {
			u.CpuUnlimited = true
		}
		if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			u.MemoryLimitBytes += q.Value()
		} else {
			u.MemoryUnlimited = true
		}
	}
	if m, ok := v.usage[p.Name]; ok {
		u.PodsMeasured++
		u.CpuUsedMillicores += m.CPUMilli
		u.MemoryUsedBytes += m.MemoryBytes
	}
}

func (v *view) groupPods(role, group string) []*corev1.Pod {
	var out []*corev1.Pod
	for _, p := range v.pods {
		if p.Labels[podspec.LabelRole] == role && p.Labels[podspec.LabelGroup] == group {
			out = append(out, p)
		}
	}
	return out
}

// ticks is what a server reported, or zeros once the report is stale.
func ticks(agents *agent.Registry, podUID string) (float64, float64) {
	if podUID == "" {
		return 0, 0
	}
	snap := agents.Lookup(podUID)
	if !snap.Known || snap.PlayersStale {
		return 0, 0
	}
	return snap.TPS, snap.MSPT
}

func (v *view) server(agents *agent.Registry, srv *spawneryv1alpha1.Server) *agentpb.InstanceStatus {
	tps, mspt := ticks(agents, srv.Status.PodUID)
	in := &agentpb.InstanceStatus{
		Name: srv.Name, Group: srv.Spec.GroupRef.Name, Phase: srv.Status.Phase,
		Ready:    srv.Status.Phase == string(phase.Ready),
		Players:  srv.Status.Players, Slots: srv.Status.Slots, Tps: tps, Mspt: mspt,
		Retiring: srv.Spec.Retire || srv.Status.Phase == string(phase.Retiring),
		Held:     srv.Spec.Hold,
		Draining: srv.Status.Phase == string(phase.Draining),
		Usage:    &agentpb.ResourceUsage{},
	}
	started := srv.CreationTimestamp.Time
	if p, ok := v.pods[srv.Name]; ok {
		v.add(in.Usage, p)
		started = p.CreationTimestamp.Time
	}
	in.AgeSeconds = int64(v.now.Sub(started) / time.Second)
	return in
}

func (v *view) proxy(agents *agent.Registry, p *corev1.Pod) *agentpb.InstanceStatus {
	snap := agents.Lookup(string(p.UID))
	in := &agentpb.InstanceStatus{
		Name: p.Name, Group: p.Labels[podspec.LabelGroup], Proxy: true,
		Ready:      podReady(p),
		Players:    snap.Players, Slots: snap.Slots,
		Retiring:   p.Annotations[podspec.AnnotationRetireRequested] != "",
		Draining:   p.Annotations[podspec.AnnotationProxyDrainingSince] != "",
		AgeSeconds: int64(v.now.Sub(p.CreationTimestamp.Time) / time.Second),
		Usage:      &agentpb.ResourceUsage{},
	}
	v.add(in.Usage, p)
	return in
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (v *view) serverGroupLine(agents *agent.Registry, g *spawneryv1alpha1.ServerGroup) (*agentpb.GroupStatus, []*agentpb.InstanceStatus) {
	line := &agentpb.GroupStatus{
		Name: g.Name, Kind: kindOf(g), Phase: g.Status.Phase,
		Replicas: g.Status.Replicas, ReadyReplicas: g.Status.ReadyReplicas, Players: g.Status.OnlinePlayers,
		Usage: &agentpb.ResourceUsage{},
	}
	var members []*agentpb.InstanceStatus
	for i := range v.servers {
		if srv := &v.servers[i]; srv.Spec.GroupRef.Name == g.Name {
			in := v.server(agents, srv)
			members = append(members, in)
			if in.Tps > 0 && (line.LowestTps == 0 || in.Tps < line.LowestTps) {
				line.LowestTps = in.Tps
			}
		}
	}
	for _, p := range v.groupPods(podspec.RoleServer, g.Name) {
		v.add(line.Usage, p)
	}
	return line, members
}

func (v *view) proxyGroupLine(agents *agent.Registry, g *spawneryv1alpha1.ProxyGroup) (*agentpb.GroupStatus, []*agentpb.InstanceStatus) {
	line := &agentpb.GroupStatus{
		Name: g.Name, Kind: agentpb.GroupState_PROXY, Phase: g.Status.Phase,
		Replicas: g.Spec.Replicas, ReadyReplicas: g.Status.ReadyReplicas, Players: g.Status.ConnectedPlayers,
		Usage: &agentpb.ResourceUsage{},
	}
	var members []*agentpb.InstanceStatus
	for _, p := range v.groupPods(podspec.RoleProxy, g.Name) {
		v.add(line.Usage, p)
		if p.DeletionTimestamp.IsZero() {
			members = append(members, v.proxy(agents, p))
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	return line, members
}

func (v *view) serverGroup(agents *agent.Registry, g *spawneryv1alpha1.ServerGroup) *agentpb.StatusResult {
	line, members := v.serverGroupLine(agents, g)
	return &agentpb.StatusResult{Total: line.Usage, Groups: []*agentpb.GroupStatus{line},
		Instances: members, MetricsAvailable: v.available}
}

func (v *view) proxyGroup(agents *agent.Registry, g *spawneryv1alpha1.ProxyGroup) *agentpb.StatusResult {
	line, members := v.proxyGroupLine(agents, g)
	return &agentpb.StatusResult{Total: line.Usage, Groups: []*agentpb.GroupStatus{line},
		Instances: members, MetricsAvailable: v.available}
}

func (v *view) network(agents *agent.Registry) *agentpb.StatusResult {
	res := &agentpb.StatusResult{Total: &agentpb.ResourceUsage{}, Other: &agentpb.ResourceUsage{},
		MetricsAvailable: v.available, Servers: int32(len(v.servers))}
	listed := map[string]bool{}
	for i := range v.serverGroups {
		line, _ := v.serverGroupLine(agents, &v.serverGroups[i])
		res.Groups = append(res.Groups, line)
		listed[podspec.RoleServer+"/"+line.Name] = true
	}
	for i := range v.proxyGroups {
		line, members := v.proxyGroupLine(agents, &v.proxyGroups[i])
		res.Groups = append(res.Groups, line)
		res.Players += line.Players
		res.Proxies += int32(len(members))
		listed[podspec.RoleProxy+"/"+line.Name] = true
	}
	for _, p := range v.pods {
		v.add(res.Total, p)
		if !listed[p.Labels[podspec.LabelRole]+"/"+p.Labels[podspec.LabelGroup]] {
			v.add(res.Other, p)
		}
	}
	return res
}

func kindOf(g *spawneryv1alpha1.ServerGroup) agentpb.GroupState_Kind {
	switch g.Spec.Type {
	case spawneryv1alpha1.ServerGroupEphemeral:
		return agentpb.GroupState_EPHEMERAL
	case spawneryv1alpha1.ServerGroupPersistent:
		return agentpb.GroupState_PERSISTENT
	case spawneryv1alpha1.ServerGroupOnDemand:
		return agentpb.GroupState_ON_DEMAND
	default:
		return agentpb.GroupState_KIND_UNSPECIFIED
	}
}
```

Notes for the implementer, each checked against the code before relying on it:
- `phase.Ready` and `phase.Draining` must exist as `phase.Phase` constants in `internal/phase/phase.go` (`phase.Retiring` does, line 42). If a name differs, use the file's spelling.
- `ServerGroup.IsOnDemand()` exists (netstate uses it).
- `GroupState_KIND_UNSPECIFIED` is the zero value's Go name in `agent.pb.go`; check the generated name and use it.
- `netstate.serverGroupKind` is unexported; `kindOf` is the same switch. Do not export netstate's to share it — two packages, one switch each, is what the repo does for small mappings.
- The pod map iterates in random order; every slice that reaches the answer is sorted (servers are pre-sorted, proxy members sorted in `proxyGroupLine`). Sums do not depend on order.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/netstatus/ -count=1`
Expected: PASS (all tests in both files).

- [ ] **Step 5: Mutation check (throwaway worktree)**

```bash
git -C /home/paul/git/spawnery worktree add --detach /tmp/claude-1000/ns-mut HEAD
```
Copy the uncommitted `internal/netstatus` into it, then in the worktree delete the `if audience == netstate.ForServers && netstate.IsPrivateServer(&srv) { continue }` block and run `$NIX go -C /tmp/claude-1000/ns-mut test ./internal/netstatus/ -run TestStatusHidesPrivateFromServers -count=1`.
Expected: FAIL. Then `git -C /home/paul/git/spawnery worktree remove --force /tmp/claude-1000/ns-mut`.

- [ ] **Step 6: Commit**

```bash
git -C /home/paul/git/spawnery add internal/netstatus
git -C /home/paul/git/spawnery commit -m "feat(netstatus): a network's usage, groups and tick rates in one answer

netstatus.Source reads groups, servers and pods from the cache, tick
rates from the registry, and the namespace's pod metrics. It honours
the agent's audience the way netstate does: a server agent never sees
a private server or an on-demand group, while their pods still count
in the total. A missing metrics API leaves usage unmeasured instead of
failing the answer; a stale report drops its TPS.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 4: The operator answers StatusRequest

**Files:**
- Modify: `internal/agentserver/server.go` (Options: `Status StatusSource`; interface)
- Modify: `internal/agentserver/requests.go` (dispatch case, `answerStatus`)
- Modify: `cmd/spawnery-operator/main.go:475-495` (wire `netstatus.Source`)
- Modify: `internal/netstatus/metrics.go` (RBAC marker)
- Modify: `internal/rbacaudit/required.go` (entry)
- Regenerate: `config/rbac/role.yaml`, `charts/spawnery/templates/rbac.yaml` (`make manifests`)
- Modify: `internal/agentserver/server_envtest_test.go` (fixture wires `Status`)
- Test: `internal/agentserver/status_test.go`, `internal/agentserver/status_envtest_test.go`

**Interfaces:**
- Consumes: `netstatus.Source`, `netstatus.ErrUnknownTarget`, `netstatus.Usage`, `netstatus.MetricsReader` (Tasks 2-3); `netstate.AudienceOf`.
- Produces: `type StatusSource interface { Status(ctx context.Context, namespace string, audience netstate.Audience, target string) (*agentpb.StatusResult, error) }`; `Options.Status`.

- [ ] **Step 1: Write the failing unit test**

`internal/agentserver/status_test.go` (package `agentserver`, so it reaches `answerStatus`). Follow `unretire_test.go`'s construction of a `*Server` (read it first; build the Server the same way it does, with `Options.Status` set to the fake below):

```go
type fakeStatus struct {
	gotNamespace string
	gotAudience  netstate.Audience
	gotTarget    string
	res          *agentpb.StatusResult
	err          error
}

func (f *fakeStatus) Status(_ context.Context, namespace string, audience netstate.Audience, target string) (*agentpb.StatusResult, error) {
	f.gotNamespace, f.gotAudience, f.gotTarget = namespace, audience, target
	return f.res, f.err
}

func TestAnswerStatusIsNamespaceAndAudienceBound(t *testing.T) {
	f := &fakeStatus{res: &agentpb.StatusResult{Servers: 3}}
	s := &Server{opts: Options{Status: f}}
	id := grpcauth.Identity{Namespace: "games", Role: agent.RoleServer}
	resp := s.answerStatus(context.Background(), logr.Discard(), id, 7, &agentpb.StatusRequest{Target: "lobby"})
	if resp.GetId() != 7 || resp.GetStatus().GetServers() != 3 {
		t.Fatalf("answer = %+v", resp)
	}
	if f.gotNamespace != "games" || f.gotAudience != netstate.ForServers || f.gotTarget != "lobby" {
		t.Errorf("asked %q/%v/%q, want games/ForServers/lobby", f.gotNamespace, f.gotAudience, f.gotTarget)
	}
}

func TestAnswerStatusRefusals(t *testing.T) {
	for _, c := range []struct {
		err    error
		reason agentpb.RequestError_Reason
		says   string
	}{
		{netstatus.ErrUnknownTarget, agentpb.RequestError_NOT_FOUND, "no group, server or proxy by that name is on this network"},
		{errors.New("cache not synced"), agentpb.RequestError_UNAVAILABLE, "the operator could not read the network just now"},
	} {
		s := &Server{opts: Options{Status: &fakeStatus{err: c.err}}}
		resp := s.answerStatus(context.Background(), logr.Discard(), grpcauth.Identity{Namespace: "games"}, 1, &agentpb.StatusRequest{})
		if resp.GetError().GetReason() != c.reason || resp.GetError().GetMessage() != c.says {
			t.Errorf("%v -> %+v, want %v %q", c.err, resp.GetError(), c.reason, c.says)
		}
	}
	s := &Server{opts: Options{}}
	if resp := s.answerStatus(context.Background(), logr.Discard(), grpcauth.Identity{}, 1, &agentpb.StatusRequest{}); resp.GetError().GetReason() != agentpb.RequestError_UNAVAILABLE {
		t.Errorf("no status source -> %+v, want UNAVAILABLE", resp)
	}
}
```

If `answerCloudRequest`'s rate limiter makes a bare `&Server{opts: ...}` unusable, call `answerStatus` directly as above — it does not touch the limiter.

- [ ] **Step 2: Run it to verify it fails**

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/agentserver/ -run TestAnswerStatus -count=1`
Expected: FAIL to compile, `s.answerStatus undefined`, `unknown field Status`.

- [ ] **Step 3: Implement the answer**

In `server.go`, next to `ServerFanout`:

```go
// StatusSource answers /cloud status for one namespace and one audience.
// *netstatus.Source in production; an interface so the request path is
// testable without a metrics API.
type StatusSource interface {
	Status(ctx context.Context, namespace string, audience netstate.Audience, target string) (*agentpb.StatusResult, error)
}
```

In `Options`, after `Writer`:

```go
	// Status answers StatusRequest. Nil refuses it as unavailable.
	Status StatusSource
```

In `requests.go`, a dispatch case after unretire:

```go
	case req.GetStatus() != nil:
		return s.answerStatus(ctx, logger, id, req.GetId(), req.GetStatus())
```

and the answer, after `answerUnretire`:

```go
// answerStatus reports the network's usage and tick rates, bound to the
// token's namespace and to the picture the agent's role is allowed to see.
func (s *Server) answerStatus(
	ctx context.Context,
	logger logr.Logger,
	id grpcauth.Identity,
	reqID uint64,
	req *agentpb.StatusRequest,
) *agentpb.CloudResponse {
	if s.opts.Status == nil {
		return refuse(reqID, agentpb.RequestError_UNAVAILABLE, "this operator cannot report status")
	}
	res, err := s.opts.Status.Status(ctx, id.Namespace, netstate.AudienceOf(id.Role), req.GetTarget())
	switch {
	case errors.Is(err, netstatus.ErrUnknownTarget):
		return refuse(reqID, agentpb.RequestError_NOT_FOUND,
			"no group, server or proxy by that name is on this network")
	case err != nil:
		logger.V(1).Info("could not build a status answer", "reason", err.Error())
		return refuse(reqID, agentpb.RequestError_UNAVAILABLE,
			"the operator could not read the network just now")
	}
	return &agentpb.CloudResponse{Id: reqID, Result: &agentpb.CloudResponse_Status{Status: res}}
}
```

Import `github.com/spawnery/spawnery/internal/netstatus`. If this creates an import cycle (netstatus imports agentserver? it must not — check), keep `ErrUnknownTarget` where it is.

- [ ] **Step 4: Run it to verify it passes**

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/agentserver/ -run TestAnswerStatus -count=1`
Expected: PASS.

- [ ] **Step 5: RBAC**

Directly above `func (m APIMetrics) PodUsage` in `internal/netstatus/metrics.go` (immediately before the declaration, not inside its doc comment — see `required.go`'s warning):

```go
// +kubebuilder:rbac:groups=metrics.k8s.io,resources=pods,verbs=list
```

In `internal/rbacaudit/required.go`, after the `tokenreviews` entry:

```go
	// Pod metrics for /cloud status. metrics.k8s.io is an aggregated API a
	// cluster may not serve at all; the grant is harmless there and the
	// answer says usage is unavailable.
	{Group: "metrics.k8s.io", Resource: "pods", Verb: "list",
		Why: "netstatus.APIMetrics.PodUsage lists a namespace's pod metrics for /cloud status"},
```

Run: `$NIX make -C /home/paul/git/spawnery manifests && git -C /home/paul/git/spawnery diff --stat config/rbac charts/spawnery/templates`
Expected: `config/rbac/role.yaml` and `charts/spawnery/templates/rbac.yaml` each gain a `metrics.k8s.io` / `pods` / `list` rule. If they do not, the marker is in the wrong place.

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/rbacaudit/ -count=1 -p 1`
Expected: PASS.

- [ ] **Step 6: Wire it in main**

In `cmd/spawnery-operator/main.go`, in `agentserver.Options{...}` after `Writer:`:

```go
		Status: netstatus.Source{
			Reader:  mgr.GetClient(),
			Agents:  registry,
			Metrics: netstatus.APIMetrics{REST: clientset.Discovery().RESTClient()},
			Clock:   time.Now,
		},
```

Run: `$NIX go -C /home/paul/git/spawnery build ./...`
Expected: exit 0.

- [ ] **Step 7: Write the failing envtest**

In `internal/agentserver/server_envtest_test.go`, `newFixtureWithProxies`: build a fake metrics reader and set `Status` in `agentserver.Options`:

```go
	statusMetrics := fixtureMetrics{}
	// ... in agentserver.Options{...}:
		Status: netstatus.Source{Reader: c, Agents: registry, Metrics: statusMetrics, Clock: now},
```

with, in the same file:

```go
// fixtureMetrics stands in for metrics-server, which envtest does not run:
// every pod uses 100m and 256Mi.
type fixtureMetrics struct{}

func (fixtureMetrics) PodUsage(ctx context.Context, namespace string) (map[string]netstatus.Usage, error) {
	return map[string]netstatus.Usage{"lobby-aaaa": {CPUMilli: 100, MemoryBytes: 256 << 20}}, nil
}
```

(Use the fixture's existing pod name if the test below creates a different one.)

`internal/agentserver/status_envtest_test.go`. The report and the request go on **one** stream, in that order: the server handles a stream's messages in sequence, so the ticks are in the registry before the request is answered, and no second stream can supersede the first. No reconciler runs here, so the test writes the Server's `status.podUID` itself — the answer finds a server's ticks by that UID.

```go
func TestStatusOverTheWireCarriesReportedTicks(t *testing.T) {
	f := newServerFixture(t)
	pod := f.pod("lobby-aaaa")
	makeServer(t, f, "lobby-aaaa")
	var srv spawneryv1alpha1.Server
	if err := f.c.Get(f.ctx, client.ObjectKey{Namespace: f.ns, Name: "lobby-aaaa"}, &srv); err != nil {
		t.Fatal(err)
	}
	srv.Status.PodName, srv.Status.PodUID = pod.Name, string(pod.UID)
	if err := f.c.Status().Update(f.ctx, &srv); err != nil {
		t.Fatal(err)
	}

	stream, done := dialAgent(t, f.ctx, f.addr, f.ca,
		f.token(podspec.ServerServiceAccountName, []string{podspec.AgentTokenAudience}, pod))
	defer done()
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("the opening message never arrived: %v", err)
	}
	ask := func(id uint64, target string) *agentpb.CloudResponse {
		t.Helper()
		if err := stream.Send(&agentpb.ServerMessage{Message: &agentpb.ServerMessage_CloudRequest{
			CloudRequest: &agentpb.CloudRequest{Id: id,
				Request: &agentpb.CloudRequest_Status{Status: &agentpb.StatusRequest{Target: target}}},
		}}); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			msg, err := stream.Recv()
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			if resp := msg.GetCloudResponse(); resp != nil && resp.GetId() == id {
				return resp
			}
			if time.Now().After(deadline) {
				t.Fatal("no answer within ten seconds")
			}
		}
	}

	if err := stream.Send(&agentpb.ServerMessage{Message: &agentpb.ServerMessage_PlayerCount{
		PlayerCount: &agentpb.PlayerCount{Players: 1, Slots: 20, Tps: 18.5, Mspt: 31},
	}}); err != nil {
		t.Fatal(err)
	}
	resp := ask(21, "lobby-aaaa")
	in := resp.GetStatus().GetInstances()
	if len(in) != 1 || in[0].GetTps() != 18.5 || in[0].GetMspt() != 31 {
		t.Fatalf("answer = %+v, want lobby-aaaa at 18.5 TPS and 31 ms", resp)
	}
	if !resp.GetStatus().GetMetricsAvailable() || in[0].GetUsage().GetCpuUsedMillicores() != 100 {
		t.Errorf("usage = %+v, want the fixture's 100m", in[0].GetUsage())
	}
	if unknown := ask(22, "nowhere"); unknown.GetError().GetReason() != agentpb.RequestError_NOT_FOUND {
		t.Errorf("unknown target -> %+v, want NOT_FOUND", unknown)
	}
}
```

Match the imports to `retire_envtest_test.go` (`client`, `spawneryv1alpha1`, `podspec`, `agentpb`, `time`). envtest's API server sets a new pod's phase to `Pending`, so the pod counts for usage.

- [ ] **Step 8: Run it to verify it fails, then passes**

With the `Status:` line temporarily removed from the fixture: run `$NIX go -C /home/paul/git/spawnery test ./internal/agentserver/ -run TestStatusOverTheWire -count=1 -p 1`
Expected: FAIL, "answer = ... want lobby-aaaa at 18.5 TPS" with an UNAVAILABLE error in the answer.

Restore the line and run again.
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git -C /home/paul/git/spawnery add internal/agentserver internal/netstatus internal/rbacaudit cmd config/rbac charts/spawnery/templates
git -C /home/paul/git/spawnery commit -m "feat(agentserver): answer /cloud status, bound to the namespace and the agent's picture

StatusRequest is answered namespace-bound and with the picture the
agent's role may see. The operator may now list pods.metrics.k8s.io;
the grant is audited and harmless on a cluster without metrics-server.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 5: The Paper agent reports TPS and MSPT

**Files:**
- Modify: `agent/paper/src/main/kotlin/cloud/spawnery/agent/paper/ServerState.kt`
- Modify: `agent/paper/src/main/kotlin/cloud/spawnery/agent/paper/ServerRole.kt:55-60`
- Modify: `agent/paper/src/main/kotlin/cloud/spawnery/agent/paper/AgentPlugin.kt:200-201`
- Test: `agent/paper/src/test/kotlin/cloud/spawnery/agent/paper/ServerRoleTest.kt`

**Interfaces:**
- Consumes: `PlayerCount.setTps(double)`, `setMspt(double)` (Task 1, generated Java).
- Produces: `ServerState.sampleTicks(tps: Double, mspt: Double)`, `ServerState.tps`, `ServerState.mspt`.

- [ ] **Step 1: Write the failing test**

In `ServerRoleTest.kt`, after `the report carries the sampled players and slots`:

```kotlin
    @Test
    fun `the report carries the sampled tick rate`() {
        val state = ServerState()
        val role = ServerRole(state, NetworkMirror(), dormantConnector(), aFeed(), CloudEvents())
        assertEquals(0.0, role.playerCount().playerCount.tps, "a server that has not sampled reports none")

        state.sampleTicks(tps = 19.7, mspt = 23.5)
        val report = role.playerCount().playerCount
        assertEquals(19.7, report.tps)
        assertEquals(23.5, report.mspt)
    }
```

- [ ] **Step 2: Run it to verify it fails**

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: FAIL, Kotlin compile error `Unresolved reference: sampleTicks`.

- [ ] **Step 3: Implement**

`ServerState.kt`, beside the counters:

```kotlin
    private val tpsBits = AtomicLong(0)
    private val msptBits = AtomicLong(0)

    val tps: Double get() = java.lang.Double.longBitsToDouble(tpsBits.get())
    val mspt: Double get() = java.lang.Double.longBitsToDouble(msptBits.get())

    fun sampleTicks(tps: Double, mspt: Double) {
        tpsBits.set(java.lang.Double.doubleToLongBits(tps))
        msptBits.set(java.lang.Double.doubleToLongBits(mspt))
    }
```

(import `java.util.concurrent.atomic.AtomicLong`; there is no `AtomicDouble` in the JDK.)

`ServerRole.playerCount()`:

```kotlin
    override fun playerCount(): ServerMessage =
        ServerMessage.newBuilder()
            .setPlayerCount(
                PlayerCount.newBuilder()
                    .setPlayers(state.players)
                    .setSlots(state.slots)
                    .setTps(state.tps)
                    .setMspt(state.mspt),
            )
            .build()
```

`AgentPlugin.kt`, in the sampling timer after `state.sample(...)`:

```kotlin
                    // Paper's own one-minute average; it caps at 20.
                    state.sampleTicks(Bukkit.getTPS()[0], Bukkit.getAverageTickTime())
```

- [ ] **Step 4: Run it to verify it passes**

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: build succeeds; the Paper test report lists `the report carries the sampled tick rate` as passed.

- [ ] **Step 5: Commit**

```bash
git -C /home/paul/git/spawnery commit -m "feat(agent): the Paper agent reports its tick rate

The sampling timer reads Paper's one-minute TPS and mean tick time
and the report carries both.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 6: status() in the plugin API

**Files:**
- Create: `agent/api/src/main/java/cloud/spawnery/agent/api/ResourceUsage.java`
- Create: `agent/api/src/main/java/cloud/spawnery/agent/api/GroupStatus.java`
- Create: `agent/api/src/main/java/cloud/spawnery/agent/api/InstanceStatus.java`
- Create: `agent/api/src/main/java/cloud/spawnery/agent/api/NetworkStatus.java`
- Modify: `agent/api/src/main/java/cloud/spawnery/agent/api/SpawneryApi.java`
- Modify: `agent/api/src/test/java/cloud/spawnery/agent/api/FakeApi.java`
- Create: `agent/common/src/main/kotlin/cloud/spawnery/agent/StatusConversion.kt`
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/NetworkMirror.kt:126-135` (make `kindOf` a top-level `internal fun`)
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/CloudConnector.kt` (`status`, response case)
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/MirrorApi.kt`
- Test: `agent/common/src/test/kotlin/cloud/spawnery/agent/CloudConnectorTest.kt`

**Interfaces:**
- Consumes: generated `StatusRequest`, `StatusResult`, `ResourceUsage` (pb), `GroupStatus` (pb), `InstanceStatus` (pb) (Task 1).
- Produces (Java, package `cloud.spawnery.agent.api`):
  ```java
  public record ResourceUsage(long cpuUsedMillicores, long cpuRequestedMillicores, long cpuLimitMillicores, boolean cpuUnlimited,
                              long memoryUsedBytes, long memoryRequestedBytes, long memoryLimitBytes, boolean memoryUnlimited,
                              int pods, int podsMeasured) { public boolean measured(); public boolean complete(); }
  public record GroupStatus(String name, Group.Kind kind, String phase, int replicas, int readyReplicas, int players,
                            OptionalDouble lowestTps, ResourceUsage usage) {}
  public record InstanceStatus(String name, String group, boolean proxy, String phase, boolean ready, int players, int slots,
                               OptionalDouble tps, OptionalDouble mspt, Duration age, boolean retiring, boolean held,
                               boolean draining, ResourceUsage usage) {}
  public record NetworkStatus(ResourceUsage total, List<GroupStatus> groups, List<InstanceStatus> instances,
                              ResourceUsage other, boolean metricsAvailable, int players, int servers, int proxies) {}
  // SpawneryApi:
  CompletionStage<NetworkStatus> status();
  CompletionStage<NetworkStatus> status(String target);
  ```
  Kotlin: `fun CloudConnector.status(target: String): CompletionStage<NetworkStatus>`; `internal fun toNetworkStatus(pb: StatusResult): NetworkStatus`.

- [ ] **Step 1: Write the failing test**

In `CloudConnectorTest.kt` (read its existing request/answer helpers first and use them; the shape below assumes a `requested` list and `connector.answer(...)` as in `CloudCommandTest`):

```kotlin
    @Test
    fun `status asks with the target and turns the answer into records`() {
        val future = connector.status("lobby")
        assertEquals("lobby", requested.single().status.target)

        connector.answer(
            CloudResponse.newBuilder().setId(requested.single().id).setStatus(
                StatusResult.newBuilder()
                    .setMetricsAvailable(true)
                    .setTotal(pbUsage(cpuUsed = 400, pods = 2, measured = 1))
                    .addGroups(
                        cloud.spawnery.agent.pb.GroupStatus.newBuilder()
                            .setName("lobby").setKind(GroupState.Kind.EPHEMERAL).setPhase("Ready")
                            .setReplicas(2).setReadyReplicas(2).setPlayers(5).setLowestTps(16.5)
                            .setUsage(pbUsage(cpuUsed = 400, pods = 2, measured = 1)),
                    )
                    .addInstances(
                        cloud.spawnery.agent.pb.InstanceStatus.newBuilder()
                            .setName("lobby-a").setGroup("lobby").setPhase("Ready").setReady(true)
                            .setPlayers(2).setSlots(20).setTps(0.0).setMspt(0.0).setAgeSeconds(5400)
                            .setUsage(pbUsage(cpuUsed = 0, pods = 1, measured = 0)),
                    ),
            ).build(),
        )

        val status = future.toCompletableFuture().get(1, TimeUnit.SECONDS)
        assertTrue(status.metricsAvailable())
        assertEquals(1, status.total().podsMeasured())
        assertFalse(status.total().complete())
        val group = status.groups().single()
        assertEquals(Group.Kind.EPHEMERAL, group.kind())
        assertEquals(OptionalDouble.of(16.5), group.lowestTps())
        val instance = status.instances().single()
        assertEquals(OptionalDouble.empty(), instance.tps(), "0 on the wire is not a TPS of zero")
        assertEquals(OptionalDouble.empty(), instance.mspt())
        assertEquals(Duration.ofSeconds(5400), instance.age())
        assertFalse(instance.usage().measured())
        assertEquals(0, status.other().pods(), "an absent other is an empty usage, not null")
    }

    private fun pbUsage(cpuUsed: Long, pods: Int, measured: Int) =
        cloud.spawnery.agent.pb.ResourceUsage.newBuilder()
            .setCpuUsedMillicores(cpuUsed).setPods(pods).setPodsMeasured(measured).build()
```

Imports: `cloud.spawnery.agent.api.Group`, `cloud.spawnery.agent.pb.GroupState`, `cloud.spawnery.agent.pb.StatusResult`, `java.time.Duration`, `java.util.OptionalDouble`, `java.util.concurrent.TimeUnit`.

- [ ] **Step 2: Run it to verify it fails**

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: FAIL, `Unresolved reference: status`.

- [ ] **Step 3: Implement the records**

`ResourceUsage.java` (header as `ProxyInfo.java`):

```java
package cloud.spawnery.agent.api;

/**
 * What a set of pods uses right now, and what they asked for. CPU in
 * millicores, memory in bytes. The used figures cover {@code podsMeasured}
 * of {@code pods}; a pod the metrics API has no sample for is left out
 * rather than counted as idle.
 *
 * <p>{@code cpuUnlimited} (and {@code memoryUnlimited}) means some container
 * has no limit, so the limit figure is a floor.
 */
public record ResourceUsage(
        long cpuUsedMillicores, long cpuRequestedMillicores, long cpuLimitMillicores, boolean cpuUnlimited,
        long memoryUsedBytes, long memoryRequestedBytes, long memoryLimitBytes, boolean memoryUnlimited,
        int pods, int podsMeasured) {

    /** Whether any pod in the set was measured. */
    public boolean measured() {
        return podsMeasured > 0;
    }

    /** Whether every pod in the set was measured. */
    public boolean complete() {
        return podsMeasured == pods;
    }
}
```

`GroupStatus.java`:

```java
package cloud.spawnery.agent.api;

import java.util.Objects;
import java.util.OptionalDouble;

/** One group in a {@link NetworkStatus}. {@code lowestTps} is empty for a proxy group and for a group none of whose servers reported. */
public record GroupStatus(String name, Group.Kind kind, String phase, int replicas, int readyReplicas, int players,
                          OptionalDouble lowestTps, ResourceUsage usage) {
    public GroupStatus {
        Objects.requireNonNull(name, "name");
        Objects.requireNonNull(kind, "kind");
        Objects.requireNonNull(phase, "phase");
        Objects.requireNonNull(lowestTps, "lowestTps");
        Objects.requireNonNull(usage, "usage");
    }
}
```

`InstanceStatus.java`:

```java
package cloud.spawnery.agent.api;

import java.time.Duration;
import java.util.Objects;
import java.util.OptionalDouble;

/**
 * One server or proxy in a {@link NetworkStatus}. {@code phase} is empty for
 * a proxy, whose state is {@code ready} and {@code draining}. {@code tps} and
 * {@code mspt} are empty for a proxy and for a server that has not reported.
 */
public record InstanceStatus(String name, String group, boolean proxy, String phase, boolean ready,
                             int players, int slots, OptionalDouble tps, OptionalDouble mspt, Duration age,
                             boolean retiring, boolean held, boolean draining, ResourceUsage usage) {
    public InstanceStatus {
        Objects.requireNonNull(name, "name");
        Objects.requireNonNull(group, "group");
        Objects.requireNonNull(phase, "phase");
        Objects.requireNonNull(tps, "tps");
        Objects.requireNonNull(mspt, "mspt");
        Objects.requireNonNull(age, "age");
        Objects.requireNonNull(usage, "usage");
    }
}
```

`NetworkStatus.java`:

```java
package cloud.spawnery.agent.api;

import java.util.List;
import java.util.Objects;

/**
 * The answer to {@link SpawneryApi#status()}: this network's usage and tick
 * rates, never anything outside its namespace.
 *
 * <p>For the whole network, {@code groups} has every group and
 * {@code instances} is empty; {@code other} is the namespace's pods outside
 * every listed group. For a group, {@code groups} is that group and
 * {@code instances} its members. For one server or proxy, {@code groups} is
 * empty and {@code instances} is that one. {@code total} is always the usage
 * of what was asked about.
 *
 * <p>{@code metricsAvailable} false means the cluster serves no metrics API;
 * every usage then has requests and limits but nothing measured.
 */
public record NetworkStatus(ResourceUsage total, List<GroupStatus> groups, List<InstanceStatus> instances,
                            ResourceUsage other, boolean metricsAvailable, int players, int servers, int proxies) {
    public NetworkStatus {
        Objects.requireNonNull(total, "total");
        groups = List.copyOf(groups);
        instances = List.copyOf(instances);
        Objects.requireNonNull(other, "other");
    }
}
```

In `SpawneryApi.java`, after `unretire`:

```java
    /**
     * How this network is doing: CPU and memory of its pods against what they
     * asked for, per group, and each server's tick rate. Asks the operator;
     * never anything outside this network's namespace.
     */
    CompletionStage<NetworkStatus> status();

    /**
     * {@link #status()} for one server group, proxy group, server or proxy,
     * looked up in that order. Fails when nothing on this network has the name.
     */
    CompletionStage<NetworkStatus> status(String target);
```

`FakeApi.java`:

```java
    @Override
    public CompletionStage<NetworkStatus> status() {
        return CompletableFuture.failedFuture(new UnsupportedOperationException("fake"));
    }

    @Override
    public CompletionStage<NetworkStatus> status(String target) {
        return CompletableFuture.failedFuture(new UnsupportedOperationException("fake"));
    }
```

- [ ] **Step 4: Implement conversion, connector and mirror API**

`NetworkMirror.kt`: move `private fun kindOf(kind: GroupState.Kind): Group.Kind` out of the class, unchanged, as a top-level `internal fun kindOf(...)` in the same file; its call site in the class keeps working.

`StatusConversion.kt` (header as the other Kotlin files):

```kotlin
package cloud.spawnery.agent

import cloud.spawnery.agent.api.GroupStatus
import cloud.spawnery.agent.api.InstanceStatus
import cloud.spawnery.agent.api.NetworkStatus
import cloud.spawnery.agent.api.ResourceUsage
import cloud.spawnery.agent.pb.StatusResult
import java.time.Duration
import java.util.OptionalDouble

internal fun toNetworkStatus(pb: StatusResult): NetworkStatus =
    NetworkStatus(
        usage(pb.total),
        pb.groupsList.map { g ->
            GroupStatus(g.name, kindOf(g.kind), g.phase, g.replicas, g.readyReplicas, g.players,
                reported(g.lowestTps), usage(g.usage))
        },
        pb.instancesList.map { i ->
            InstanceStatus(i.name, i.group, i.proxy, i.phase, i.ready, i.players, i.slots,
                reported(i.tps), reported(i.mspt), Duration.ofSeconds(i.ageSeconds),
                i.retiring, i.held, i.draining, usage(i.usage))
        },
        usage(pb.other),
        pb.metricsAvailable,
        pb.players,
        pb.servers,
        pb.proxies,
    )

/** Zero on the wire means "not reported". */
private fun reported(value: Double): OptionalDouble =
    if (value > 0) OptionalDouble.of(value) else OptionalDouble.empty()

private fun usage(u: cloud.spawnery.agent.pb.ResourceUsage): ResourceUsage =
    ResourceUsage(
        u.cpuUsedMillicores, u.cpuRequestedMillicores, u.cpuLimitMillicores, u.cpuUnlimited,
        u.memoryUsedBytes, u.memoryRequestedBytes, u.memoryLimitBytes, u.memoryUnlimited,
        u.pods, u.podsMeasured,
    )
```

(An unset message field reads as its default instance, so `usage(pb.other)` is an all-zero usage, which is what the test asserts.)

`CloudConnector.kt`: import `cloud.spawnery.agent.api.NetworkStatus` and `cloud.spawnery.agent.pb.StatusRequest`; after `unretire`:

```kotlin
    /** The network's usage and tick rates, for the whole network or one target. */
    fun status(target: String): CompletionStage<NetworkStatus> =
        requests.start<NetworkStatus> { id ->
            sendRequest(
                CloudRequest.newBuilder()
                    .setId(id)
                    .setStatus(StatusRequest.newBuilder().setTarget(target))
                    .build(),
            )
        }
```

and in the response `when`, after `hasUnretire()`:

```kotlin
            response.hasStatus() -> requests.complete(response.id, toNetworkStatus(response.status))
```

`MirrorApi.kt`, after `unretire`:

```kotlin
    override fun status(): CompletionStage<NetworkStatus> = connector.status("")

    override fun status(target: String): CompletionStage<NetworkStatus> = connector.status(target)
```

- [ ] **Step 5: Run it to verify it passes**

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: build succeeds; `status asks with the target and turns the answer into records` passes, and `PackagingInvariantTest` passes (the new records use only `java.*` and `cloud.spawnery.agent.api.*`).

- [ ] **Step 6: Commit**

```bash
git -C /home/paul/git/spawnery commit -m "feat(api): status() and status(target) in the plugin API

status() and status(target) ask the operator and return records:
NetworkStatus, GroupStatus, InstanceStatus and ResourceUsage. A TPS or
MSPT of 0 on the wire is an empty OptionalDouble.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 7: /cloud status

**Files:**
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/Style.kt` (`warn`)
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/CloudCommand.kt` (permission, root, branch)
- Create: `agent/common/src/main/kotlin/cloud/spawnery/agent/StatusLines.kt` (rendering)
- Test: `agent/common/src/test/kotlin/cloud/spawnery/agent/CloudCommandTest.kt`, `CloudCompletionTest` in the same file

**Interfaces:**
- Consumes: `SpawneryApi.status()`, `status(target)`, the records (Task 6).
- Produces: `const val PERMISSION_STATUS = "spawnery.cloud.status"`; `internal fun statusLines(status: NetworkStatus, target: String): List<String>`.

Rendering, one chat line per list entry:

- Network form (`target` empty):
  1. `Network: <players> players · <servers> servers · <proxies> proxies`
  2. usage line of `total` (below)
  3. one group line per group
  4. `other pods  CPU a  RAM b` when `other.pods() > 0`
- Group form: the group line, then one instance line per member.
- Instance form: the instance line, then the usage line of its `usage`.

Usage line: `CPU <used> / <requested> cores requested (limit <limit>[+]) · RAM <used> / <requested> GiB requested (limit <limit>[+] GiB)`; `+` after a limit when unlimited is set. When `!metricsAvailable`: `CPU – · RAM – (no metrics API on this cluster)` followed by the requested figures. When measured but not complete: append ` · usage of <podsMeasured> of <pods> pods`.

Group line: `<name>  <phase>  <ready>/<replicas>[ proxies]  <players> players  [TPS <x>  ]CPU <used>  RAM <used>`. TPS only for non-proxy groups: `TPS –` when empty. Usage figures `–` when `!usage.measured()`.

Instance line: `<name>  <phase or ready/not ready>  <players>/<slots>  [TPS <x>  MSPT <y>  ]CPU <used>  RAM <used>  age <age>` plus ` [retiring]`, ` [held]`, ` [draining]` markers.

Numbers: cores `"%.1f".format(Locale.ROOT, milli / 1000.0)`; memory `"%.1f GiB"` of `bytes / 2^30`; TPS `"%.1f"`, MSPT `"%.1f"`. TPS colour: `Style.good` at ≥ 19, `Style.warn` at ≥ 15, `Style.bad` below. Age: `<60s` → `Ns`, `<1h` → `Nm`, `<1d` → `XhYm`, else `XdYh`.

- [ ] **Step 1: Write the failing tests**

In `CloudCommandTest`, add `PERMISSION_STATUS` to the default `permissions` set, and:

```kotlin
    private fun statusAnswer(build: StatusResult.Builder.() -> Unit) =
        answer { setStatus(StatusResult.newBuilder().apply(build)) }

    private fun usage(cpuUsed: Long, cpuReq: Long, memUsed: Long, memReq: Long, pods: Int, measured: Int) =
        cloud.spawnery.agent.pb.ResourceUsage.newBuilder()
            .setCpuUsedMillicores(cpuUsed).setCpuRequestedMillicores(cpuReq).setCpuLimitMillicores(2 * cpuReq)
            .setMemoryUsedBytes(memUsed).setMemoryRequestedBytes(memReq).setMemoryLimitBytes(2 * memReq)
            .setPods(pods).setPodsMeasured(measured)

    @Test
    fun `status shows the network, its groups and the rest`() {
        run("cloud status", api(aNetworkWithProxies()))
        assertEquals("", requested.single().status.target)
        assertTrue(sent.isEmpty(), "answered before the operator did: $sent")
        statusAnswer {
            metricsAvailable = true
            players = 12; servers = 9; proxies = 2
            total = usage(3100, 8000, 12L shl 30, 24L shl 30, 11, 11).build()
            addGroups(
                cloud.spawnery.agent.pb.GroupStatus.newBuilder().setName("arena").setKind(GroupState.Kind.EPHEMERAL)
                    .setPhase("Ready").setReplicas(2).setReadyReplicas(2).setPlayers(6).setLowestTps(17.8)
                    .setUsage(usage(1100, 2000, 3L shl 30, 4L shl 30, 2, 2)),
            )
            other = usage(500, 400, 1L shl 30, 1L shl 30, 2, 2).build()
        }
        assertTrue(sent[0].contains("12") && sent[0].contains("9") && sent[0].contains("2"), sent[0])
        assertTrue(sent[1].contains("3.1") && sent[1].contains("8.0") && sent[1].contains("12.0 GiB"), sent[1])
        val arena = sent.single { it.contains("arena") }
        assertTrue(arena.contains("17.8") && arena.contains("<yellow>"), arena)
        assertTrue(sent.last().contains("other"), sent.last())
    }

    @Test
    fun `status says when usage is unavailable`() {
        run("cloud status", api(aNetworkWithProxies()))
        statusAnswer {
            metricsAvailable = false
            total = usage(0, 8000, 0, 24L shl 30, 11, 0).build()
        }
        assertTrue(sent[1].contains("–") && sent[1].contains("no metrics API"), sent[1])
        assertFalse(sent[1].contains("0.0 /"), "unmeasured usage printed as zero: ${sent[1]}")
    }

    @Test
    fun `status says how many pods the usage covers`() {
        run("cloud status", api(aNetworkWithProxies()))
        statusAnswer {
            metricsAvailable = true
            total = usage(3100, 8000, 12L shl 30, 24L shl 30, 9, 8).build()
        }
        assertTrue(sent[1].contains("8 of 9 pods"), sent[1])
    }

    @Test
    fun `status of a server shows its ticks, its markers and its limits`() {
        run("cloud status lobby-r", api(aNetworkWithProxies()))
        assertEquals("lobby-r", requested.single().status.target)
        statusAnswer {
            metricsAvailable = true
            total = usage(400, 500, 1L shl 30, 2L shl 30, 1, 1).build()
            addInstances(
                cloud.spawnery.agent.pb.InstanceStatus.newBuilder().setName("lobby-r").setGroup("lobby")
                    .setPhase("Retiring").setPlayers(3).setSlots(20).setTps(12.0).setMspt(80.0)
                    .setAgeSeconds(3 * 3600 + 20 * 60).setRetiring(true).setHeld(true)
                    .setUsage(usage(400, 500, 1L shl 30, 2L shl 30, 1, 1)),
            )
        }
        val line = sent[0]
        assertTrue(line.contains("<red>") && line.contains("12.0") && line.contains("80.0"), line)
        assertTrue(line.contains("3h20m") && line.contains("retiring") && line.contains("held"), line)
        assertTrue(sent[1].contains("limit 1.0"), sent[1])
    }

    @Test
    fun `status shows a server without a reported TPS as missing`() {
        run("cloud status lobby-r", api(aNetworkWithProxies()))
        statusAnswer {
            metricsAvailable = true
            total = usage(400, 500, 1L shl 30, 2L shl 30, 1, 1).build()
            addInstances(
                cloud.spawnery.agent.pb.InstanceStatus.newBuilder().setName("lobby-r").setGroup("lobby")
                    .setPhase("Ready").setUsage(usage(400, 500, 1L shl 30, 2L shl 30, 1, 1)),
            )
        }
        assertTrue(sent[0].contains("TPS –"), sent[0])
        assertFalse(sent[0].contains("0.0"), "an unreported TPS printed as zero: ${sent[0]}")
    }

    @Test
    fun `status says why the operator refused`() {
        run("cloud status nowhere", api(aNetworkWithProxies()))
        answer {
            setError(RequestError.newBuilder().setReason(RequestError.Reason.NOT_FOUND)
                .setMessage("no group, server or proxy by that name is on this network"))
        }
        assertTrue(sent.single().contains("no group, server or proxy by that name"), sent.single())
    }

    @Test
    fun `status needs its own permission`() {
        permissions = setOf(PERMISSION_READ)
        assertThrows(CommandSyntaxException::class.java) { run("cloud status", api(aNetworkWithProxies())) }
    }
```

Use whatever assertion the file already uses for a branch the source cannot see (read the existing permission tests first; if it asserts on the return value or on `sent`, do the same).

In `CloudCompletionTest`:

```kotlin
    @Test
    fun `status suggests groups, servers and proxies`() {
        val offered = completions("cloud status ")
        assertTrue(offered.contains("lobby") && offered.any { it.startsWith("lobby-") } && offered.any { it.startsWith("gateway-") }, "$offered")
    }
```

Adjust the names to the fixture network `CloudCompletionTest` builds (read `api()` there).

- [ ] **Step 2: Run them to verify they fail**

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: FAIL, `Unresolved reference: PERMISSION_STATUS`.

- [ ] **Step 3: Implement**

`Style.kt`, after `bad`:

```kotlin
    fun warn(value: String): String = "<yellow>${escape(value)}</yellow>"
```

`CloudCommand.kt`: after `PERMISSION_SCALE`:

```kotlin
/** `/cloud status`, which asks the operator and so is not part of reading the mirror. */
const val PERMISSION_STATUS: String = "spawnery.cloud.status"
```

Add `adapter.hasPermission(it, PERMISSION_STATUS) ||` to the root `.requires`. Add the branch after `info`:

```kotlin
        .then(
            LiteralArgumentBuilder.literal<S>("status")
                .requires { adapter.hasPermission(it, PERMISSION_STATUS) }
                .executes { ctx -> askStatus(api.status(), adapter, format, ctx.source, "") }
                .then(
                    RequiredArgumentBuilder.argument<S, String>("name", StringArgumentType.word())
                        .suggests(suggesting {
                            api.groups().map(Group::name) + api.servers().map(ServerInfo::name) +
                                api.proxies().map(ProxyInfo::name)
                        })
                        .executes { ctx ->
                            val name = StringArgumentType.getString(ctx, "name")
                            askStatus(api.status(name), adapter, format, ctx.source, name)
                        },
                ),
        )
```

and, beside `reply`:

```kotlin
private fun <S> askStatus(
    answer: java.util.concurrent.CompletionStage<NetworkStatus>,
    adapter: SourceAdapter<S>,
    format: () -> String,
    source: S,
    target: String,
): Int {
    answer.whenComplete { status, failure ->
        if (failure == null) {
            for (line in statusLines(status, target)) reply(adapter, format, source, line)
        } else {
            reply(adapter, format, source, Style.bad("no status") + Style.quiet(": ") + Style.bad(reason(failure)))
        }
    }
    return 1
}
```

`StatusLines.kt`:

```kotlin
package cloud.spawnery.agent

import cloud.spawnery.agent.api.GroupStatus
import cloud.spawnery.agent.api.Group
import cloud.spawnery.agent.api.InstanceStatus
import cloud.spawnery.agent.api.NetworkStatus
import cloud.spawnery.agent.api.ResourceUsage
import java.time.Duration
import java.util.Locale
import java.util.OptionalDouble

internal fun statusLines(status: NetworkStatus, target: String): List<String> {
    val lines = mutableListOf<String>()
    when {
        target.isEmpty() -> {
            lines += Style.quiet("Network: ") + Style.number(status.players()) + Style.quiet(" players · ") +
                Style.number(status.servers()) + Style.quiet(" servers · ") +
                Style.number(status.proxies()) + Style.quiet(" proxies")
            lines += usageLine(status.total(), status.metricsAvailable())
            status.groups().forEach { lines += groupLine(it) }
            if (status.other().pods() > 0) {
                lines += Style.name("other pods") + "  " + cpuRam(status.other())
            }
        }
        status.groups().isNotEmpty() -> {
            lines += groupLine(status.groups().single())
            status.instances().forEach { lines += instanceLine(it) }
        }
        else -> {
            val instance = status.instances().single()
            lines += instanceLine(instance)
            lines += usageLine(instance.usage(), status.metricsAvailable())
        }
    }
    return lines
}

private fun cores(milli: Long) = String.format(Locale.ROOT, "%.1f", milli / 1000.0)
private fun gib(bytes: Long) = String.format(Locale.ROOT, "%.1f", bytes / (1L shl 30).toDouble())

private fun usageLine(u: ResourceUsage, metrics: Boolean): String {
    val used = if (metrics && u.measured()) null else "–"
    val cpu = Style.quiet("CPU ") + Style.number(used ?: cores(u.cpuUsedMillicores())) + Style.quiet(" / ") +
        Style.number(cores(u.cpuRequestedMillicores())) + Style.quiet(" cores requested (limit ") +
        Style.number(cores(u.cpuLimitMillicores()) + if (u.cpuUnlimited()) "+" else "") + Style.quiet(")")
    val ram = Style.quiet("RAM ") + Style.number(used ?: gib(u.memoryUsedBytes())) + Style.quiet(" / ") +
        Style.number(gib(u.memoryRequestedBytes()) + " GiB") + Style.quiet(" requested (limit ") +
        Style.number(gib(u.memoryLimitBytes()) + (if (u.memoryUnlimited()) "+" else "") + " GiB") + Style.quiet(")")
    var line = cpu + Style.quiet(" · ") + ram
    if (!metrics) {
        line += Style.quiet(" · ") + Style.bad("no metrics API on this cluster")
    } else if (u.measured() && !u.complete()) {
        line += Style.quiet(" · usage of ${u.podsMeasured()} of ${u.pods()} pods")
    }
    return line
}

private fun cpuRam(u: ResourceUsage): String =
    if (!u.measured()) {
        Style.quiet("CPU ") + Style.number("–") + "  " + Style.quiet("RAM ") + Style.number("–")
    } else {
        Style.quiet("CPU ") + Style.number(cores(u.cpuUsedMillicores())) + "  " +
            Style.quiet("RAM ") + Style.number(gib(u.memoryUsedBytes()) + " GiB")
    }

private fun tps(value: OptionalDouble): String {
    if (value.isEmpty) return Style.quiet("TPS ") + Style.number("–")
    val v = value.asDouble
    val text = String.format(Locale.ROOT, "%.1f", v)
    val coloured = when {
        v >= 19 -> Style.good(text)
        v >= 15 -> Style.warn(text)
        else -> Style.bad(text)
    }
    return Style.quiet("TPS ") + coloured
}

private fun groupLine(g: GroupStatus): String {
    val proxy = g.kind() == Group.Kind.PROXY
    return Style.name(g.name()) + "  " + Style.number(g.phase()) + "  " +
        Style.number("${g.readyReplicas()}/${g.replicas()}") + (if (proxy) Style.quiet(" proxies") else "") + "  " +
        Style.number(g.players()) + Style.quiet(" players") + "  " +
        (if (proxy) "" else tps(g.lowestTps()) + "  ") + cpuRam(g.usage())
}

private fun age(d: Duration): String {
    val s = d.seconds
    return when {
        s < 60 -> "${s}s"
        s < 3600 -> "${s / 60}m"
        s < 86400 -> "${s / 3600}h${(s % 3600) / 60}m"
        else -> "${s / 86400}d${(s % 86400) / 3600}h"
    }
}

private fun instanceLine(i: InstanceStatus): String {
    val state = if (i.proxy()) {
        if (i.ready()) Style.good("ready") else Style.bad("not ready")
    } else {
        Style.number(i.phase())
    }
    val ticks = if (i.proxy()) "" else {
        tps(i.tps()) + "  " + Style.quiet("MSPT ") +
            Style.number(if (i.mspt().isEmpty) "–" else String.format(Locale.ROOT, "%.1f", i.mspt().asDouble)) + "  "
    }
    return Style.name(i.name()) + "  " + state + "  " + Style.number("${i.players()}/${i.slots()}") + "  " +
        ticks + cpuRam(i.usage()) + "  " + Style.quiet("age ") + Style.number(age(i.age())) +
        (if (i.retiring()) " " + Style.marker("retiring", "red") else "") +
        (if (i.held()) " " + Style.marker("held", "red") else "") +
        (if (i.draining()) " " + Style.marker("draining", "red") else "")
}
```

Check `Style.marker`'s signature (`fun marker(sign: String, colour: String)`) before using it; it exists at `Style.kt:48`.

- [ ] **Step 4: Run them to verify they pass**

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: build succeeds; all new `status` tests and the completion test pass, and the rest of `CloudCommandTest` still passes (the root now also opens for `PERMISSION_STATUS` alone).

- [ ] **Step 5: Commit**

```bash
git -C /home/paul/git/spawnery commit -m "feat(agent): /cloud status, for the network, a group, a server or a proxy

Gated by the new spawnery.cloud.status. The network form shows the
totals, one line per group with its lowest TPS, and the namespace's
other pods; a group lists its members; a server or proxy shows its
usage against requests and limits. Missing values print as a dash.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 8: Docs, and the spec brought in line

**Files:**
- Modify: `docs/guides/cloud-command.md` (permission table, the three forms, what `–` means, that it needs metrics-server)
- Modify: the plugin-API page under `docs/plugin-api/` that lists `retire`/`unretire` (find it with `grep -rln "unretire" docs/plugin-api`): `status()`, `status(target)`, the four records
- Modify: `docs/superpowers/specs/2026-09-26-cloud-status-design.md` §5 (list only; `pods`/`pods_measured`; the two unlimited flags; `other`; header counts), matching this plan's "Rulings" section

- [ ] **Step 1: Write the docs**

In `cloud-command.md`'s permission table add:

```markdown
| `spawnery.cloud.status` | `/cloud status [group\|server\|proxy]` |
```

and a section beside `unretire`:

```markdown
**`/cloud status`** shows how the network is doing: its pods' CPU and memory
against what they requested, one line per group with the lowest TPS among its
servers, and a line for the namespace's other pods. `/cloud status <group>`
lists the group's servers or proxies with TPS, MSPT, usage and age;
`/cloud status <server|proxy>` shows one of them against its requests and
limits. It asks the operator, so it answers only while the agent is
connected.

Usage comes from the cluster's metrics API (metrics-server). Without one the
answer still arrives, with `–` where usage would be. A pod started within the
last minute may not have a sample yet; the totals then say how many pods they
cover. `TPS –` is a server that has not reported a tick rate, such as one
running an agent older than 0.9.0.

Nothing outside the network's namespace is shown. A backend server's answer
leaves private servers and on-demand groups out, as `/cloud list` does.
```

- [ ] **Step 2: Run the docs checks**

Run: `$NIX make -C /home/paul/git/spawnery docs-length-lint`
Expected: exit 0.

- [ ] **Step 3: Commit**

```bash
git -C /home/paul/git/spawnery add docs
git -C /home/paul/git/spawnery commit -m "docs(guides): /cloud status and the status API

The guide gains the permission, the three forms and what a dash
means; the plugin API page gains status(). The spec follows the
rulings the plan made: list only, pods and pods_measured on every
usage, one unlimited flag per resource, other, and the header counts.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

## After the last task

Whole suite (see Commands), then the final review. The release (0.9.0: `flake.nix` `imageVersion`/`operatorVersion`, `Chart.yaml`, `values.yaml`, image tags in docs and samples, release notes) is its own `chore: 0.9.0, …` commit after review, as for 0.8.0 — not part of this plan.
