# Network metrics and a dashboard — Design

**Status:** approved in conversation 2026-09-30 (JVM heap included; the
dashboard ships in the chart).

## Goal

An operator of a network sees, in Grafana, where players are, how every
server group and server is doing — tick rate, tick time, memory — and can
drill from the network to a single server. Today the operator exports only
its own health (agent connections, certificates, changeovers); everything a
player-facing view needs is in its memory and in the CR status, but not in
Prometheus.

## Metrics

Computed at scrape time from the operator's cache and its agent registry, by
one `prometheus.Collector`: nothing is stored between scrapes, so a server
that is gone leaves no series behind. Labels are `namespace`, `network`,
`group`, and for per-server series `server` (which is also the pod name) and
`node`.

| Metric | Labels | Value |
|---|---|---|
| `spawnery_network_players` | namespace, network | players on the network's proxies |
| `spawnery_group_servers` | namespace, network, group, type | servers of the group (status.replicas) |
| `spawnery_group_servers_ready` | same | ready servers |
| `spawnery_group_players` | same | status.onlinePlayers |
| `spawnery_group_free_slots` | same | status.freeSlots (playable seats) |
| `spawnery_server_players` | namespace, network, group, server, node | last reported players |
| `spawnery_server_slots` | same | reported slots |
| `spawnery_server_playable_slots` | same | effective playable slots |
| `spawnery_server_tps` | same | one-minute TPS; absent when not reported |
| `spawnery_server_mspt` | same | mean tick time in ms; absent when not reported |
| `spawnery_server_heap_used_bytes` | same | JVM heap in use |
| `spawnery_server_heap_max_bytes` | same | JVM max heap |
| `spawnery_server_phase` | same + phase | 1 for the server's current phase |
| `spawnery_proxy_players` | namespace, network, group, proxy, node | players on the proxy |
| `spawnery_proxy_heap_used_bytes` / `_heap_max_bytes` | same | the proxy's JVM heap |

A server without an agent report yet has its phase and nothing else. CPU and
container memory are not exported: the kubelet's cAdvisor series already carry
them per pod, and `server` equals the pod name, which is what the dashboard
joins on.

`docs/reference/metrics-and-alerts.md` is generated from the registered
metrics, as today.

## Heap

`PlayerCount` gains `int64 heap_used_bytes = 6` and `int64 heap_max_bytes = 7`,
sent by both agents with every periodic report (`Runtime.totalMemory() -
freeMemory()` and `maxMemory()`). 0 means not reported — an agent older than
the fields. The registry keeps them beside TPS and MSPT; they are not
validated beyond `used >= 0`, `max >= 0`. They are not added to the CR status
(which is throttled for observers and would churn on every GC).

## Dashboard

`charts/spawnery/dashboards/network.json`, rendered into a ConfigMap by
`templates/dashboard.yaml` when `metrics.dashboard.enabled` (default false,
like the ServiceMonitor), with `metrics.dashboard.labels` (default
`grafana_dashboard: "1"`) and `metrics.dashboard.annotations` for the
sidecar's folder. Datasource is a variable, so it fits any Prometheus.

Variables: datasource, network (from `spawnery_network_players`), group
(multi, from `spawnery_group_servers`).

- **Row "Network":** players now; servers ready / total; players over time
  per group (stacked).
- **Row "Groups":** a table per group — players, free slots, servers ready /
  total, worst TPS, worst MSPT.
- **Row "Servers":** a table per server — group, phase, players / playable /
  slots, TPS, MSPT, heap used / max, container memory, CPU, node.
- **Row "Health over time":** minimum TPS and maximum MSPT per group; heap
  used per server; container memory per server
  (`container_memory_working_set_bytes` joined on namespace and pod).

## Testing

- Collector: a unit test with a fake cache and registry — a group with two
  servers (one reported, one not) and a proxy produces exactly the expected
  series and values (`prometheus/testutil.CollectAndCompare`); a deleted
  server's series are gone on the next collect.
- Registry: heap fields are stored and read back; a negative value is
  rejected like an impossible tick report.
- Agents: both put the heap into their report (JUnit on the report builder).
- Chart: rendered with the value on, the ConfigMap carries the JSON, labels
  and annotations; off by default (rbacaudit's rendered-objects list is for
  defaults, so it stays unchanged).
- Dashboard JSON parses and every panel's query names only metrics that exist
  (a test that extracts `spawnery_*` names from the JSON and compares them
  with the registered ones).
- On a cluster: Grafana shows both networks, player counts match `/cloud`.

## Not in this design

- Alerts on these metrics (TPS below a threshold and the like).
- Per-player metrics.
