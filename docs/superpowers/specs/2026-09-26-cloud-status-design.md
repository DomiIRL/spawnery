# /cloud status: a network's health in game

**Status:** design, decided 2026-09-26
**Date:** 2026-09-26

## 1. What is missing

An admin in game can list servers and proxies (`/cloud list`, `/cloud info`)
but cannot see how the network is doing: how much CPU and memory it uses
against what it asked for, and whether its servers keep up with the tick rate.
Both answers exist today only outside the game, in a metrics stack, if the
network has one.

`/cloud status` answers them in chat, for the whole network or for one group,
server or proxy.

## 2. Scope

**The network's own namespace, nothing else.** Nodes, their capacity and the
load of other namespaces stay out: spawnery serves one network per namespace,
and on a shared cluster a network must not see another tenant's load. No
cluster-scoped figure appears anywhere in the answer.

`/cloud info` stays what it is: the network picture every agent already holds,
answered locally without a round trip. `/cloud status` is live and asks the
operator.

## 3. What an admin sees

Gated by the new permission `spawnery.cloud.status`, for all three forms.

### 3.1 `/cloud status`

```
Network: 12 players · 9 servers · 2 proxies
CPU 3.1 / 8.0 cores requested (limit 16.0)   RAM 11.2 / 24.0 GiB requested
lobby     Ready  1/1          4 players  TPS 20.0  CPU 0.4  RAM 1.9 GiB
arena     Ready  2/2          6 players  TPS 17.8  CPU 1.1  RAM 3.2 GiB
gateway   Ready  2/2 proxies 12 players            CPU 0.2  RAM 0.6 GiB
```

- The totals are the network's pods: usage summed, against the summed
  requests, and the summed limits where every container has one.
- One line per server group and per proxy group. A group's TPS is the
  **lowest** of its servers: one lagging server is the one players notice.
  TPS is green at 19 and above, yellow from 15, red below.
- Proxies have no TPS.

### 3.2 `/cloud status <group>`

The group's line from 3.1, then one line per server or proxy: phase,
players/slots, TPS and MSPT (servers), CPU, RAM, age, and the markers
`retiring`, `held` and `draining` where they apply.

### 3.3 `/cloud status <server|proxy>`

One instance in full: the fields of 3.2, with CPU and RAM also against the
pod's requests and limits.

### 3.4 Resolving the name

A group name first (server group, then proxy group), then a server, then a
proxy pod. A server or proxy is named after its group plus a suffix, so a
clash needs a group named like another group's member; the order settles it
in the group's favour. Nothing found: "no group, server or proxy called
<name>". Suggestions offer all three kinds.

### 3.5 What is missing is shown as missing

- No metrics API, or no sample for a pod yet: `CPU –`, `RAM –` for that pod,
  and totals that leave it out say so ("usage of 8 of 9 pods"). The rest of the
  answer stands.
- A server whose agent reports no TPS (an older agent, or one that has not
  reported since it connected): `TPS –`. It is left out of its group's lowest
  TPS rather than counted as zero.

## 4. TPS and MSPT

The server agent adds two fields to its periodic report:

```proto
message PlayerCount {
  int32 players = 1;
  int32 slots = 2;
  double tps = 3;   // server agents: the one-minute average; 0 = not reported
  double mspt = 4;  // server agents: mean tick duration in ms; 0 = not reported
}
```

The Paper-based agent reads them from the server's own tick statistics
(`Bukkit.getTPS()[0]`, `Bukkit.getAverageTickTime()`). Proxy agents send 0.

The operator keeps the latest values in memory beside the player count it
already keeps, and **does not write them to `Server.status`**: a status write
per report interval per server buys nothing a live question does not answer.
A value older than the report staleness threshold is treated as not reported.

## 5. The request

A new pair in `CloudRequest`/`CloudResponse`, namespace-bound like `retire`:

```proto
message StatusRequest {
  string target = 1;  // empty: the whole network
}

message StatusResult {
  ResourceUsage total = 1;
  repeated GroupStatus groups = 2;      // every group, or only the target's
  repeated InstanceStatus instances = 3; // empty for the network form
  int32 pods_without_metrics = 4;
  bool metrics_available = 5;
}
```

`ResourceUsage` carries CPU in millicores and memory in bytes, each as used,
requested and limit, with a flag for "no limit on some container".
`GroupStatus` carries name, kind (server or proxy), phase, ready and desired
replicas, players, lowest TPS, and usage. `InstanceStatus` carries name,
group, kind, phase, players, slots, TPS, MSPT, age, the three markers, and
usage.

The operator builds the answer from:

- its cache: groups, servers, proxy pods, and the requests and limits in the
  pod specs;
- the registry: players, TPS, MSPT;
- one live list of `PodMetrics` in the namespace (`metrics.k8s.io/v1beta1`).
  The ClusterRole gains `get` and `list` on `pods` in `metrics.k8s.io`, with an
  rbacaudit reason. A failing metrics call sets `metrics_available: false`;
  it never fails the request.

## 6. Plugin API

On both platforms, beside the other cloud calls:

```java
CompletionStage<NetworkStatus> status();
CompletionStage<NetworkStatus> status(String target);
```

with records `NetworkStatus`, `GroupStatus`, `InstanceStatus` and
`ResourceUsage` mirroring §5, and `OptionalDouble` where a value can be
missing (TPS, MSPT, usage). The command renders from these records, so a
plugin can build its own view from the same data.

## 7. Versions

New proto messages and fields, a new API surface and a new command: a minor
step for the operator, the chart and the images by the rule of 2026-09-08,
so **0.9.0**. Existing objects are untouched; an older agent keeps working
and shows up with `TPS –`.

## 8. Testing

- **agentserver:** `StatusRequest` for the network, a server group, a proxy
  group, a server, a proxy, and an unknown name; namespace-bound (a name in
  another namespace is unknown); metrics API failing → answer without usage
  and `metrics_available: false`; a pod without a sample counted in
  `pods_without_metrics`.
- **registry:** TPS/MSPT stored from the report, zero kept as missing, stale
  values dropped.
- **aggregation:** lowest TPS ignores servers without TPS; requests and limits
  summed, "no limit" when one container lacks one.
- **agent (Kotlin):** the three renderings, the missing-value forms, the TPS
  colours at 19/15, suggestions offering groups, servers and proxies, the
  permission.
- **agent (Paper):** the report carries TPS and MSPT.
- **envtest:** one `/cloud status <server>` round trip through the reconciler
  with a fake metrics source.
- **On the live network after release:** `/cloud status`, one group, one
  server, one proxy; compare CPU/RAM against `kubectl top pods`.

## 9. Not in this design

- Nodes and anything cluster-scoped (§2).
- TPS as a Prometheus metric: cheap, and useful for dashboards, but its own
  step.
- Caching the metrics call.
- A live view (scoreboard, boss bar).
