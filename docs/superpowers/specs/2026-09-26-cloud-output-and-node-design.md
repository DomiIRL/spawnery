# /cloud output in sections and bars, and the node a server runs on

**Status:** design, decided 2026-09-26
**Date:** 2026-09-26

Two changes, one release (0.10.0).

- **A — every `/cloud` answer is laid out in sections**, with bars for what has a
  capacity (§1). Rendering only.
- **B — `/cloud info` and `/cloud status` name the node** a server or proxy runs
  on (§2). This one reaches the proto, the operator and the Java API.

## 1. Sections and bars

### 1.1 What goes wrong today

Every answer is one run-on line per object: `lobby-a in lobby: Ready, 12/100
players, taking joins, held`. A network with ten groups answers `/cloud list`
with ten such lines, and `/cloud status` packs usage, requests, limits and TPS
into one line per group. Nothing is grouped, and nothing shows at a glance how
full something is.

### 1.2 The rules

- **A heading line** opens every multi-line answer: a bold title, then a grey
  one-line summary (`Network status  12 players · 9 servers · 2 proxies`).
- **Sections** under it have bold grey headings (`Resources`, `Server groups`,
  `Proxy groups`, `Other pods`, …). Entries are indented under their section;
  members of an entry one step further.
- **No columns.** Chat is not monospaced, so nothing is padded to line up.
  Fields in a line are separated by ` · ` in a fixed order per kind of line.
- **Bars** show what has a capacity: 20 segments of `|`, the filled part
  coloured, the empty part dark grey, the figure after it.
  - Colour by fill: green below 70 %, yellow below 90 %, red from 90 %.
  - RAM and CPU: used against the limit, or against the request when no
    container has a limit. The text gives all three
    (`3.1 of 16.0 cores · 8.0 requested`).
  - Players against slots, for a server and for a server group.
  - TPS against 20, coloured by the TPS thresholds that already exist (green at
    19, yellow from 15, red below), because a full TPS bar is good.
  - A value that is missing gets no bar: `TPS –`, `CPU –`.
- **One-line answers** (`retire`, `unretire`, `start`, `stop`, `events`) stay one
  line and begin with `✔` (green) or `✘` (red). A refusal still gives the
  operator's reason.
- Every line is still wrapped in the network's own reply format.

### 1.3 The answers

`/cloud list`:

```
Network  3 groups · 7 servers · 2 proxies
 Server groups
   lobby (ephemeral) · 2/2 ready · 12/200 players · 188 free
   arena (ephemeral) · 2/2 ready · 6/40 players · 34 free
 Proxy groups
   gateway · 2/2 ready · 12 players
     gateway-a · ready · 7 players
     gateway-b · ready · draining · 5 players
```

`/cloud info <server>`:

```
arena-a  server in arena · Ready · taking joins
 Node     node-2
 Players  ||||||||------------  8 / 20
 Says     waiting for players
 Marked   held
```

`Says` and `Marked` appear only when there is something to say. `/cloud info
<proxy>` and `/cloud info <group>` follow the same shape: the heading, then one
labelled line per fact.

`/cloud status`:

```
Network status  12 players · 9 servers · 2 proxies
 Resources
   CPU  |||||||||||---------  3.1 of 16.0 cores · 8.0 requested
   RAM  |||||||-------------  11.2 of 32.0 GiB · 24.0 requested
 Server groups
   lobby · Ready · 1/1 · 4 players · TPS 20.0 · CPU 0.4 · RAM 1.9 GiB
   arena · Ready · 2/2 · 6 players · TPS 17.8 · CPU 1.1 · RAM 3.2 GiB
 Proxy groups
   gateway · Ready · 2/2 · 12 players · CPU 0.2 · RAM 0.6 GiB
 Other pods
   CPU 0.5 · RAM 3.0 GiB
```

The notes the 0.9.0 answer carries stay, on the Resources lines: `usage of 8 of 9
pods`, `usage unavailable (metrics API not answering)`, `no limit`, `limit ≥`.

`/cloud status <group>`: the group's line as a heading, then a `Servers` (or
`Proxies`) section with one line per member: name, phase or ready, players,
TPS and MSPT, CPU, RAM, node, age, markers.

`/cloud status <server|proxy>`:

```
arena-a  server in arena · Ready · up 2h13m
 Node     node-2
 Players  ||||||||------------  8 / 20
 TPS      ||||||||||||||||||--  17.8 · MSPT 42.1
 CPU      ||||||--------------  0.6 of 2.0 cores · 1.0 requested
 RAM      ||||||||||||--------  1.9 of 3.0 GiB · 2.0 requested
 Marked   retiring · held
```

## 2. The node

### 2.1 Scope

Only the name of the node a server or proxy of this network runs on. No
node's capacity, load or other pods: the 0.9.0 rule that nothing
cluster-scoped is shown stands, and a node's name tells a network where its own
pod is, not what else is there.

### 2.2 On the wire

- `ServerState.node = 12` and `ProxyState.node = 6` in the network picture, so
  `/cloud info` answers from the mirror as before.
- `InstanceStatus.node = 15` in the status answer.
- Empty while the pod is not scheduled, and for a server without a pod. The
  command then says `not scheduled`.

### 2.3 Where the operator reads it

`pod.spec.nodeName`. `netstate.Build` already lists the proxy pods; it also
lists the server pods of the namespace (role label `server`) and takes the node
of the pod named in `Server.status.podName`. `netstatus` already has every pod.
The audience rules do not change: a server hidden from a backend is not listed,
so neither is its node.

### 2.4 Java API

`ServerInfo.node()`, `ProxyInfo.node()`, `InstanceStatus.node()`, each a
`String`, empty when not scheduled. Each record keeps a constructor with its
previous components, so code built against 0.9.0 still compiles.

## 3. Versions

New proto fields, operator behaviour and Java API methods: a minor step for
the operator, the chart and the images by the rule of 2026-09-08, so **0.10.0**
for all three. The CRDs are unchanged. Nothing rolls on the operator upgrade.

## 4. Testing

- **agent (Kotlin):** each answer's shape (heading, section headings,
  indentation, `·` order), bars at 0 %, 69 %, 70 %, 89 %, 90 % and 100 %, the TPS
  bar's colours, a missing value without a bar, `✔`/`✘` on each one-liner,
  `Node` in `info` and `status`, `not scheduled` for an empty node.
- **netstate:** a server's node from its pod, empty without a pod; a proxy's
  node.
- **netstatus:** an instance's node.
- **Java API:** the previous constructors still exist.
- **On the live network after release:** `/cloud list`, `/cloud info` and
  `/cloud status` in all three forms, and the node against `kubectl get pods -o
  wide`.

## 5. Not in this design

- Colours or bar characters configurable per network.
- Paging long answers.
- Node load or capacity (§2.1).
