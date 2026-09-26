# /cloud Output in Sections and Bars, and the Node — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every `/cloud` answer is laid out in headed sections with spark-style bars, and `/cloud info` and `/cloud status` name the node a server or proxy runs on.

**Architecture:** The node travels as three new proto fields (`ServerState.node`, `ProxyState.node`, `InstanceStatus.node`) filled from `pod.spec.nodeName` by `netstate` and `netstatus`, and reaches plugins as `node()` on `ServerInfo`, `ProxyInfo` and `InstanceStatus`. Rendering moves into one small layout toolkit (`Layout.kt`: heading, section, entry, field, bar, ✔/✘) that `/cloud list`, `info`, `status` and the one-line answers all use.

**Tech Stack:** Go (controller-runtime, fake client), protobuf, Java 21 API, Kotlin agents (Gradle via Nix), MiniMessage markup.

**Spec:** `docs/superpowers/specs/2026-09-26-cloud-output-and-node-design.md`

## Global Constraints

- Bars: 20 segments of `|`; filled part coloured, empty part `dark_gray`; colour by fill: green below 70 %, yellow below 90 %, red from 90 %. TPS bar: fill = tps/20, coloured green at ≥ 19, yellow at ≥ 15, red below.
- RAM/CPU bars: against the limit, or against the request when a container lacks a limit (`*Unlimited`); no bar when the denominator is 0 or nothing was measured.
- A missing value gets no bar: `TPS –`, `CPU –`, `RAM –`.
- Headings: bold title + grey summary. Section headings: bold grey, indented one space. Entries: three spaces. Members: five spaces. Fields separated by ` · `. No padding to columns.
- One-line answers begin with `✔` (green) or `✘` (red). Refusals keep the operator's reason.
- Node: empty string when not scheduled; rendered `not scheduled`.
- Proto field numbers: `ServerState.node = 12`, `ProxyState.node = 6`, `InstanceStatus.node = 15`.
- `ServerInfo`, `ProxyInfo`, `InstanceStatus` keep constructors with their previous component lists.
- `agent/api` keeps only `java.*` and `cloud.spawnery.agent.api.*` in public signatures (`PackagingInvariantTest`).
- Nothing cluster-scoped beyond the node name of this network's own pods.
- Generated files are committed (`make proto`); new files are `git add`ed before `make agent` (Nix reads the index).
- Commits: Conventional Commits with scope, body wrapped at 72, signed, ending with the session trailers. Examples in the public repo stay invented (`lobby`, `arena`, `gateway`, `node-2`).

## Commands

```bash
NIX="nix --extra-experimental-features 'nix-command flakes' develop /home/paul/git/spawnery -c"
$NIX go -C /home/paul/git/spawnery test ./internal/netstate/ ./internal/netstatus/ -count=1
$NIX make -C /home/paul/git/spawnery proto
$NIX make -C /home/paul/git/spawnery agent          # both plugins and their JUnit suites (git add first)
# on failure: nix-store -l <drv from the log> | grep -E '^e: |FAILED|AssertionFailedError'
# test names that ran: nix log .#agents | grep '<test name>'
# whole suite on the development VM:
$NIX make -C /home/paul/git/spawnery manifests generate fmt vet chart-lint toolchain-lint image-tag-lint docs-length-lint crd-docs-test chart-values-docs-test metrics-docs-test
$NIX go -C /home/paul/git/spawnery test -race -p 1 ./...
```

## Review Focus

1. **A name or value containing `<`** (an announced state, a node name, an operator refusal) must print as text, never as markup — every value goes through `Style.*`, which escapes. Pinned in Task 3 (`a label or value with markup prints as text`).
2. **A fraction outside 0..1** (players above slots after a lowered maxPlayers, CPU above limit when throttling lags, TPS above 20) must clamp the bar, not throw or print more than 20 segments. Pinned in Task 3 (`a bar clamps what overflows`).
3. **A server whose pod is not scheduled or does not exist** says `not scheduled` and has no usage bars, instead of an empty `Node` line or `0.0 of 0.0`. Pinned in Task 5 (`status of an unscheduled server says so`) and Task 4 (`info of an unscheduled server says so`).
4. **A network with no groups** still answers `/cloud list` with a heading and the existing "no groups on this network yet" line. Pinned in Task 4.
5. **A plugin built against 0.9.0** that constructs `ServerInfo`/`ProxyInfo`/`InstanceStatus` with the old component list still compiles and gets `node() == ""`. Pinned in Task 2.

---

### Task 1: The node on the wire

**Files:**
- Modify: `proto/spawnery/agent/v1alpha1/agent.proto` (ServerState, ProxyState, InstanceStatus)
- Regenerate: `internal/agentpb/agent.pb.go`, `agent/common/src/proto/java/**`
- Modify: `internal/netstate/netstate.go` (Build)
- Modify: `internal/netstatus/status.go` (server, proxy)
- Test: `internal/netstate/netstate_test.go`, `internal/netstatus/status_test.go`

**Interfaces:**
- Produces: Go `ServerState.Node`, `ProxyState.Node`, `InstanceStatus.Node` (string); Java `getNode()` on the generated classes.

- [ ] **Step 1: Proto**

```proto
// in ServerState, after held = 11:
  // The Kubernetes node this server's pod runs on; empty while it is not
  // scheduled. A name only: nothing about the node itself is ever sent.
  string node = 12;
// in ProxyState, after players = 5:
  string node = 6;  // as ServerState.node
// in InstanceStatus, after usage = 14:
  string node = 15; // as ServerState.node
```

Run: `$NIX make -C /home/paul/git/spawnery proto && $NIX go -C /home/paul/git/spawnery build ./...`
Expected: exit 0.

- [ ] **Step 2: Failing tests**

`internal/netstate/netstate_test.go`:

```go
func TestBuildNamesTheNodeAServerAndAProxyRunOn(t *testing.T) {
	srv := readyServer("ns", "lobby-a", "lobby", 0, 100)
	srv.Status.PodName = "lobby-a"
	unplaced := readyServer("ns", "lobby-b", "lobby", 0, 100)
	unplaced.Status.PodName = "lobby-b"
	serverPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "lobby-a", Namespace: "ns",
			Labels: map[string]string{podspec.LabelRole: podspec.RoleServer, podspec.LabelGroup: "lobby"}},
		Spec: corev1.PodSpec{NodeName: "node-2"},
	}
	pendingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "lobby-b", Namespace: "ns",
			Labels: map[string]string{podspec.LabelRole: podspec.RoleServer, podspec.LabelGroup: "lobby"}},
	}
	proxyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway-a", Namespace: "ns",
			Labels: map[string]string{podspec.LabelRole: podspec.RoleProxy, podspec.LabelGroup: "gateway"}},
		Spec: corev1.PodSpec{NodeName: "node-3"},
	}
	src, _ := source(t, ephemeralGroup("ns", "lobby"), srv, unplaced, serverPod, pendingPod, proxyPod)

	got, err := src.Build(context.Background(), "ns", netstate.ForProxies)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	nodes := map[string]string{}
	for _, s := range got.GetServers() {
		nodes[s.GetName()] = s.GetNode()
	}
	if nodes["lobby-a"] != "node-2" || nodes["lobby-b"] != "" {
		t.Errorf("server nodes = %v, want lobby-a on node-2 and lobby-b unscheduled", nodes)
	}
	if p := got.GetProxies(); len(p) != 1 || p[0].GetNode() != "node-3" {
		t.Errorf("proxies = %v, want gateway-a on node-3", p)
	}
}
```

Add `podspec` to the imports if missing (`github.com/spawnery/spawnery/internal/podspec`).

`internal/netstatus/status_test.go` — in `network()`, give the pods nodes (`gw.Spec.NodeName = "node-3"`, and in `pod(...)` set `Spec.NodeName: "node-1"` for every pod), then:

```go
func TestStatusNamesTheNode(t *testing.T) {
	res, err := status(t, allMeasured(), netstate.ForProxies, "lobby")
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range res.GetInstances() {
		if in.GetNode() != "node-1" {
			t.Errorf("%s node = %q, want node-1", in.GetName(), in.GetNode())
		}
	}
	res, err = status(t, allMeasured(), netstate.ForProxies, "gateway-a")
	if err != nil {
		t.Fatal(err)
	}
	if n := res.GetInstances()[0].GetNode(); n != "node-3" {
		t.Errorf("gateway-a node = %q, want node-3", n)
	}
}
```

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/netstate/ ./internal/netstatus/ -count=1`
Expected: FAIL — nodes empty.

- [ ] **Step 3: Implement**

`netstate.Build`, before the servers loop, list the server pods once:

```go
	var serverPods corev1.PodList
	if err := s.Reader.List(ctx, &serverPods, client.InNamespace(namespace),
		client.MatchingLabels{podspec.LabelRole: podspec.RoleServer}); err != nil {
		return nil, fmt.Errorf("list server pods in %s: %w", namespace, err)
	}
	nodeOf := make(map[string]string, len(serverPods.Items))
	for i := range serverPods.Items {
		nodeOf[serverPods.Items[i].Name] = serverPods.Items[i].Spec.NodeName
	}
```

and in the `ServerState` literal: `Node: nodeOf[srv.Status.PodName],`. In the `ProxyState` literal: `Node: pod.Spec.NodeName,`.

`netstatus`: in `server()`, inside `if p, ok := v.pods[srv.Name]; ok {`, add `in.Node = p.Spec.NodeName`; in `proxy()`, add `Node: p.Spec.NodeName,` to the literal.

- [ ] **Step 4: Pass**

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/netstate/ ./internal/netstatus/ ./internal/agentserver/ -count=1 -p 1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -C /home/paul/git/spawnery add proto internal/agentpb agent/common/src/proto internal/netstate internal/netstatus
git -C /home/paul/git/spawnery commit -m "feat(netstate): the node a server or proxy runs on

The network picture and the status answer carry the name of the node
each server and proxy pod is scheduled on, empty while it is not. A
name only; nothing about the node itself is sent.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 2: node() in the Java API

**Files:**
- Modify: `agent/api/src/main/java/cloud/spawnery/agent/api/ServerInfo.java`
- Modify: `agent/api/src/main/java/cloud/spawnery/agent/api/ProxyInfo.java`
- Modify: `agent/api/src/main/java/cloud/spawnery/agent/api/InstanceStatus.java`
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/NetworkMirror.kt:72-93`
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/StatusConversion.kt`
- Test: `agent/common/src/test/kotlin/cloud/spawnery/agent/MirrorApiTest.kt`, `agent/api/src/test/java/cloud/spawnery/agent/api/` (new `RecordCompatibilityTest.java`)

**Interfaces:**
- Consumes: generated `getNode()` (Task 1).
- Produces: `ServerInfo.node()`, `ProxyInfo.node()`, `InstanceStatus.node()` — `String`, never null, `""` when not scheduled.

- [ ] **Step 1: Failing tests**

`RecordCompatibilityTest.java` (JUnit 5, header as the other Java files):

```java
package cloud.spawnery.agent.api;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.time.Duration;
import java.util.Map;
import java.util.OptionalDouble;
import org.junit.jupiter.api.Test;

class RecordCompatibilityTest {
    private static final ResourceUsage NONE = new ResourceUsage(0, 0, 0, false, 0, 0, 0, false, 0, 0);

    @Test
    void theZeroNinePreviousConstructorsStillBuildAndReadAsUnscheduled() {
        ServerInfo server = new ServerInfo("lobby-a", "lobby", ServerPhase.READY, 1, 20, true, "", Map.of(), "", 0, false);
        assertEquals("", server.node());
        ProxyInfo proxy = new ProxyInfo("gateway-a", "gateway", true, false, 3);
        assertEquals("", proxy.node());
        InstanceStatus instance = new InstanceStatus("lobby-a", "lobby", false, "Ready", true, 1, 20,
                OptionalDouble.empty(), OptionalDouble.empty(), Duration.ZERO, false, false, false, NONE);
        assertEquals("", instance.node());
    }

    @Test
    void aNullNodeReadsAsEmpty() {
        assertEquals("", new ProxyInfo("gateway-a", "gateway", true, false, 3, null).node());
    }
}
```

Check `ServerPhase.READY` is the constant's spelling in `ServerPhase.java` before running; use the file's name.

In `MirrorApiTest.kt`, add (following the file's existing way of building a `NetworkState` and a `MirrorApi`):

```kotlin
    @Test
    fun `servers and proxies carry the node they run on`() {
        val api = apiWith(
            NetworkState.newBuilder()
                .addServers(ServerState.newBuilder().setName("lobby-a").setGroup("lobby").setPhase("Ready").setNode("node-2"))
                .addProxies(ProxyState.newBuilder().setName("gateway-a").setGroup("gateway").setNode("node-3"))
                .build(),
        )
        assertEquals("node-2", api.server("lobby-a").get().node())
        assertEquals("node-3", api.proxy("gateway-a").get().node())
    }
```

`apiWith` stands for the file's existing helper that builds a `MirrorApi` over a `NetworkState`; use its real name.

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: FAIL — `Unresolved reference 'node'` / `cannot find symbol node()`.

- [ ] **Step 2: Implement**

`ServerInfo`: add the component `String node` after `boolean held`; in the compact constructor `node = node == null ? "" : node;`; add the 11-argument constructor (the one without `node`) delegating with `""`, and change the existing 10-argument one to pass `false, ""`. Javadoc `@param node the Kubernetes node the server's pod runs on, empty while it is not scheduled.`

`ProxyInfo`:

```java
public record ProxyInfo(String name, String group, boolean ready, boolean draining, int players, String node) {
    public ProxyInfo {
        Objects.requireNonNull(name, "name");
        Objects.requireNonNull(group, "group");
        node = node == null ? "" : node;
    }

    /** The record as it was before {@code node}, which it reads as not scheduled. */
    public ProxyInfo(String name, String group, boolean ready, boolean draining, int players) {
        this(name, group, ready, draining, players, "");
    }
}
```

`InstanceStatus`: add `String node` as the last component, `node = node == null ? "" : node;` in the compact constructor, and a constructor with the previous 14 components delegating with `""`. Mention `node` in the Javadoc.

`NetworkMirror.kt`: pass `it.node` as the new last argument to `ServerInfo(...)` and to `ProxyInfo(...)`.

`StatusConversion.kt`: pass `i.node` as the new last argument to `InstanceStatus(...)`.

- [ ] **Step 3: Pass**

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: build succeeds; `nix log .#agents` lists both new tests PASSED and `PackagingInvariantTest` passing.

- [ ] **Step 4: Commit**

```bash
git -C /home/paul/git/spawnery commit -m "feat(api): ServerInfo, ProxyInfo and InstanceStatus name their node

node() is the Kubernetes node the pod runs on, empty while it is not
scheduled. Each record keeps a constructor with its previous
components, so a plugin built against 0.9.0 still compiles.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 3: The layout toolkit

**Files:**
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/Style.kt` (bold helpers)
- Create: `agent/common/src/main/kotlin/cloud/spawnery/agent/Layout.kt`
- Test: `agent/common/src/test/kotlin/cloud/spawnery/agent/LayoutTest.kt`

**Interfaces:**
- Produces:
  ```kotlin
  object Layout {
      const val SEGMENTS = 20
      fun heading(title: String, summary: String = ""): String
      fun section(title: String): String
      fun entry(text: String): String       // three spaces
      fun member(text: String): String      // five spaces
      fun field(label: String, value: String): String
      fun joined(vararg parts: String): String   // non-empty parts joined by " · " in quiet grey
      fun bar(fraction: Double, colour: String): String
      fun fillColour(fraction: Double): String   // "green" | "yellow" | "red"
      fun tpsColour(tps: Double): String
      fun ok(text: String): String          // "✔ " + text
      fun fail(text: String): String        // "✘ " + text
  }
  ```
  Arguments named `text`, `value` and `summary` are already-styled strings; `title`/`label` are plain and get escaped.

- [ ] **Step 1: Failing tests**

`LayoutTest.kt`:

```kotlin
package cloud.spawnery.agent

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

class LayoutTest {
    private fun segments(bar: String, colour: String): Int =
        Regex("<$colour>(\\|*)</$colour>").find(bar)?.groupValues?.get(1)?.length ?: 0

    @Test
    fun `a bar has twenty segments, the filled part coloured`() {
        val bar = Layout.bar(0.5, "green")
        assertEquals(10, segments(bar, "green"))
        assertEquals(10, segments(bar, "dark_gray"))
    }

    @Test
    fun `a bar clamps what overflows`() {
        assertEquals(20, segments(Layout.bar(1.7, "red"), "red"))
        assertEquals(0, segments(Layout.bar(-0.2, "red"), "red"))
        assertEquals(20, segments(Layout.bar(-0.2, "red"), "dark_gray"))
        assertEquals(0, segments(Layout.bar(Double.NaN, "red"), "red"))
    }

    @Test
    fun `fill colours change at seventy and ninety percent`() {
        assertEquals("green", Layout.fillColour(0.0))
        assertEquals("green", Layout.fillColour(0.69))
        assertEquals("yellow", Layout.fillColour(0.70))
        assertEquals("yellow", Layout.fillColour(0.89))
        assertEquals("red", Layout.fillColour(0.90))
        assertEquals("red", Layout.fillColour(1.0))
    }

    @Test
    fun `tps colours change at nineteen and fifteen`() {
        assertEquals("green", Layout.tpsColour(19.0))
        assertEquals("yellow", Layout.tpsColour(18.99))
        assertEquals("yellow", Layout.tpsColour(15.0))
        assertEquals("red", Layout.tpsColour(14.99))
    }

    @Test
    fun `indentation marks sections, entries and members`() {
        assertTrue(Layout.section("Resources").startsWith(" <"))
        assertTrue(Layout.entry("x").startsWith("   x"))
        assertTrue(Layout.member("x").startsWith("     x"))
    }

    @Test
    fun `a heading is bold with a grey summary`() {
        val line = Layout.heading("Network", Style.quiet("3 groups"))
        assertTrue(line.contains("<bold>Network</bold>") && line.contains("<gray>3 groups</gray>"), line)
    }

    @Test
    fun `joined leaves out empty parts`() {
        assertEquals("a<gray> · </gray>b", Layout.joined("a", "", "b"))
    }

    @Test
    fun `outcomes are marked`() {
        assertTrue(Layout.ok("done").startsWith("<green>✔</green> "))
        assertTrue(Layout.fail("no").startsWith("<red>✘</red> "))
    }

    @Test
    fun `a label or value with markup prints as text`() {
        val line = Layout.field("Says <b>", Style.name("<red>x"))
        assertTrue(!line.contains("<b>") && !line.contains("<red>x"), line)
    }
}
```

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: FAIL — `Unresolved reference 'Layout'`.

- [ ] **Step 2: Implement**

`Style.kt`, beside the other helpers:

```kotlin
    fun title(value: String): String = "<white><bold>${escape(value)}</bold></white>"

    fun sectionTitle(value: String): String = "<gray><bold>${escape(value)}</bold></gray>"
```

`Layout.kt`:

```kotlin
package cloud.spawnery.agent

/**
 * The shapes every /cloud answer is built from: a heading, sections, entries
 * and members under them, labelled fields, and bars for what has a capacity.
 *
 * Chat is not monospaced, so nothing here pads to line up; order and ` · `
 * carry the structure instead.
 */
object Layout {
    const val SEGMENTS = 20

    fun heading(title: String, summary: String = ""): String =
        Style.title(title) + if (summary.isEmpty()) "" else "  $summary"

    fun section(title: String): String = " " + Style.sectionTitle(title)

    fun entry(text: String): String = "   $text"

    fun member(text: String): String = "     $text"

    fun field(label: String, value: String): String = " " + Style.quiet(label) + "  " + value

    fun joined(vararg parts: String): String =
        parts.filter { it.isNotEmpty() }.joinToString(Style.quiet(" · "))

    fun bar(fraction: Double, colour: String): String {
        val filled = if (fraction.isNaN()) 0 else (fraction.coerceIn(0.0, 1.0) * SEGMENTS).toInt()
        return "<$colour>" + "|".repeat(filled) + "</$colour>" +
            "<dark_gray>" + "|".repeat(SEGMENTS - filled) + "</dark_gray>"
    }

    fun fillColour(fraction: Double): String = when {
        fraction < 0.70 -> "green"
        fraction < 0.90 -> "yellow"
        else -> "red"
    }

    fun tpsColour(tps: Double): String = when {
        tps >= 19 -> "green"
        tps >= 15 -> "yellow"
        else -> "red"
    }

    fun ok(text: String): String = "<green>✔</green> $text"

    fun fail(text: String): String = "<red>✘</red> $text"
}
```

`toInt()` truncates, so 69 % shows 13 filled segments and 0.5 exactly 10; the test's 0.5 → 10 holds.

- [ ] **Step 3: Pass**

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: build succeeds; every `LayoutTest` case PASSED.

- [ ] **Step 4: Commit**

```bash
git -C /home/paul/git/spawnery commit -m "feat(agent): a layout toolkit for /cloud answers

Headings, sections, entries, labelled fields, ✔ and ✘, and bars of
twenty segments coloured green below 70 %, yellow below 90 % and red
from there, with TPS coloured by its own thresholds.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 4: /cloud list and /cloud info in sections

**Files:**
- Create: `agent/common/src/main/kotlin/cloud/spawnery/agent/ListLines.kt`
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/CloudCommand.kt` (list and info branches; remove `describe`, `describeProxy`, `describeGroup`)
- Test: `agent/common/src/test/kotlin/cloud/spawnery/agent/CloudCommandTest.kt`

**Interfaces:**
- Consumes: `Layout` (Task 3), `ServerInfo.node()`, `ProxyInfo.node()` (Task 2).
- Produces: `internal fun listLines(groups: List<Group>, servers: List<ServerInfo>, proxies: List<ProxyInfo>): List<String>`, `internal fun serverInfoLines(server: ServerInfo): List<String>`, `internal fun proxyInfoLines(proxy: ProxyInfo): List<String>`, `internal fun groupInfoLines(group: Group, servers: List<ServerInfo>, proxies: List<ProxyInfo>): List<String>`, `internal fun nodeText(node: String): String`.

- [ ] **Step 1: Failing tests**

In `CloudCommandTest`:

```kotlin
    @Test
    fun `list opens with a heading and sorts groups into sections`() {
        run("cloud list", api(aNetworkWithProxies()))
        assertTrue(sent[0].contains("<bold>Network</bold>") && sent[0].contains("2 groups"), sent[0])
        val serverGroups = sent.indexOfFirst { it.contains("Server groups") }
        val proxyGroups = sent.indexOfFirst { it.contains("Proxy groups") }
        assertTrue(serverGroups in 1 until proxyGroups, "$sent")
        assertTrue(sent[serverGroups + 1].startsWith("   ") && sent[serverGroups + 1].contains("lobby"), "$sent")
        assertTrue(sent.any { it.startsWith("     ") && it.contains("gateway-b") && it.contains("draining") }, "$sent")
    }

    @Test
    fun `list of an empty network still says so`() {
        run("cloud list", api(NetworkState.getDefaultInstance()))
        assertTrue(sent.any { it.contains("no groups on this network yet") }, "$sent")
    }

    @Test
    fun `info of a server shows its node and a players bar`() {
        run("cloud info lobby-a", api(aNetworkWithNode("node-2")))
        assertTrue(sent[0].contains("<bold>lobby-a</bold>") && sent[0].contains("lobby"), sent[0])
        assertTrue(sent.any { it.contains("Node") && it.contains("node-2") }, "$sent")
        assertTrue(sent.any { it.contains("Players") && it.contains("|") && it.contains("12") }, "$sent")
    }

    @Test
    fun `info of an unscheduled server says so`() {
        run("cloud info lobby-a", api(aNetworkWithNode("")))
        assertTrue(sent.any { it.contains("Node") && it.contains("not scheduled") }, "$sent")
    }
```

with, beside `aNetwork()`:

```kotlin
private fun aNetworkWithNode(node: String): NetworkState =
    NetworkState.newBuilder()
        .addGroups(
            GroupState.newBuilder().setName("lobby").setKind(GroupState.Kind.EPHEMERAL)
                .setReplicas(1).setReadyReplicas(1).setOnlinePlayers(12).setFreeSlots(88),
        )
        .addServers(
            ServerState.newBuilder().setName("lobby-a").setGroup("lobby")
                .setPhase("Ready").setPlayers(12).setSlots(100).setRegistered(true).setNode(node),
        )
        .build()
```

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: FAIL — the new tests fail on the heading and `Node` assertions.

- [ ] **Step 2: Implement `ListLines.kt`**

```kotlin
package cloud.spawnery.agent

import cloud.spawnery.agent.api.Group
import cloud.spawnery.agent.api.ProxyInfo
import cloud.spawnery.agent.api.ServerInfo

internal fun nodeText(node: String): String =
    if (node.isEmpty()) Style.quiet("not scheduled") else Style.name(node)

private fun count(n: Int, one: String, many: String) = "$n ${if (n == 1) one else many}"

internal fun listLines(groups: List<Group>, servers: List<ServerInfo>, proxies: List<ProxyInfo>): List<String> {
    val lines = mutableListOf(
        Layout.heading(
            "Network",
            Style.quiet("${count(groups.size, "group", "groups")} · ${count(servers.size, "server", "servers")} · ${count(proxies.size, "proxy", "proxies")}"),
        ),
    )
    if (groups.isEmpty()) {
        lines += Layout.entry(Style.quiet("no groups on this network yet"))
        return lines
    }
    val (proxyGroups, serverGroups) = groups.partition { it.kind() == Group.Kind.PROXY }
    if (serverGroups.isNotEmpty()) {
        lines += Layout.section("Server groups")
        for (g in serverGroups) {
            lines += Layout.entry(
                Layout.joined(
                    Style.name(g.name()) + Style.quiet(" (${g.kind().name.lowercase()})"),
                    Style.number("${g.readyReplicas()}/${g.replicas()}") + Style.quiet(" ready"),
                    Style.number("${g.onlinePlayers()}/${g.onlinePlayers() + g.freeSlots()}") + Style.quiet(" players"),
                    Style.number(g.freeSlots()) + Style.quiet(" free"),
                ),
            )
        }
    }
    if (proxyGroups.isNotEmpty()) {
        lines += Layout.section("Proxy groups")
        for (g in proxyGroups) {
            lines += Layout.entry(
                Layout.joined(
                    Style.name(g.name()),
                    Style.number("${g.readyReplicas()}/${g.replicas()}") + Style.quiet(" ready"),
                    Style.number(g.onlinePlayers()) + Style.quiet(" players"),
                ),
            )
            for (p in proxies.filter { it.group() == g.name() }.sortedBy { it.name() }) {
                lines += Layout.member(proxyLine(p))
            }
        }
    }
    return lines
}

private fun proxyLine(p: ProxyInfo): String =
    Layout.joined(
        Style.name(p.name()),
        if (p.ready()) Style.good("ready") else Style.bad("not ready"),
        if (p.draining()) Style.bad("draining") else "",
        Style.number(p.players()) + Style.quiet(" players"),
    )

internal fun serverInfoLines(s: ServerInfo): List<String> {
    val lines = mutableListOf(
        Layout.heading(
            s.name(),
            Layout.joined(
                Style.quiet("server in ") + Style.name(s.group()),
                Style.number(s.phase().toString()),
                if (s.registered()) Style.good("taking joins") else Style.bad("not taking joins"),
            ),
        ),
        Layout.field("Node", nodeText(s.node())),
    )
    val fill = if (s.slots() > 0) s.players().toDouble() / s.slots() else 0.0
    lines += Layout.field(
        "Players",
        (if (s.slots() > 0) Layout.bar(fill, Layout.fillColour(fill)) + "  " else "") +
            Style.number("${s.players()} / ${s.slots()}"),
    )
    if (s.state().isNotEmpty()) lines += Layout.field("Says", Style.name(s.state()))
    if (s.held()) lines += Layout.field("Marked", Style.bad("held"))
    return lines
}

internal fun proxyInfoLines(p: ProxyInfo): List<String> = listOf(
    Layout.heading(
        p.name(),
        Layout.joined(
            Style.quiet("proxy in ") + Style.name(p.group()),
            if (p.ready()) Style.good("ready") else Style.bad("not ready"),
            if (p.draining()) Style.bad("draining") else "",
        ),
    ),
    Layout.field("Node", nodeText(p.node())),
    Layout.field("Players", Style.number(p.players())),
)

internal fun groupInfoLines(g: Group, servers: List<ServerInfo>, proxies: List<ProxyInfo>): List<String> {
    val lines = mutableListOf(
        Layout.heading(
            g.name(),
            Layout.joined(
                Style.quiet("${g.kind().name.lowercase()} group"),
                Style.number("${g.readyReplicas()}/${g.replicas()}") + Style.quiet(" ready"),
            ),
        ),
    )
    if (g.kind() == Group.Kind.PROXY) {
        lines += Layout.field("Players", Style.number(g.onlinePlayers()))
        lines += Layout.section("Proxies")
        proxies.filter { it.group() == g.name() }.sortedBy { it.name() }.forEach { lines += Layout.entry(proxyLine(it)) }
    } else {
        val capacity = g.onlinePlayers() + g.freeSlots()
        val fill = if (capacity > 0) g.onlinePlayers().toDouble() / capacity else 0.0
        lines += Layout.field(
            "Players",
            (if (capacity > 0) Layout.bar(fill, Layout.fillColour(fill)) + "  " else "") +
                Style.number("${g.onlinePlayers()} / $capacity") + Style.quiet(" · ") +
                Style.number(g.freeSlots()) + Style.quiet(" free"),
        )
        val members = servers.filter { it.group() == g.name() }.sortedBy { it.name() }
        if (members.isNotEmpty()) {
            lines += Layout.section("Servers")
            members.forEach {
                lines += Layout.entry(
                    Layout.joined(
                        Style.name(it.name()),
                        Style.number(it.phase().toString()),
                        Style.number("${it.players()}/${it.slots()}"),
                        nodeText(it.node()),
                        if (it.held()) Style.bad("held") else "",
                    ),
                )
            }
        }
    }
    return lines
}
```

Check `ServerPhase` has a sensible `toString()` (it is an enum; if its constants are upper case, print the wire spelling the old `describe` printed — read `describe` before deleting it and keep whatever it passed to `Style.number`).

- [ ] **Step 3: Wire it**

In `CloudCommand.kt`, the `list` branch's `executes` becomes:

```kotlin
                .executes { ctx ->
                    val lines = listLines(api.groups(), api.servers(), api.proxies())
                    lines.forEach { reply(adapter, format, ctx.source, it) }
                    api.groups().size
                }
```

The `info` branch: replace the three `reply(... describe…(…))` calls with
`serverInfoLines(server.get()).forEach { reply(adapter, format, ctx.source, it) }`,
`proxyInfoLines(proxy.get()).forEach { … }` and
`groupInfoLines(group.get(), api.servers(), api.proxies()).forEach { … }`. The not-found line becomes
`Layout.fail(Style.bad("no server, proxy or group called") + " " + Style.name(name) + Style.quiet(" on this network"))`.
Delete `describe`, `describeProxy`, `describeGroup`.

- [ ] **Step 4: Adapt the existing tests**

These existing tests read a single line and now meet several: `list names every group and what it is doing`, `info about a server names its phase and whether it takes joins`, `a server that says what it is doing has it in the line, marked as its own word`, `a server that has said nothing gets no fragment rather than an empty one`, `taking joins and not taking joins are told apart by colour, not only by words`, `info about a group works through the same argument`, `info answers for a proxy`, `info says a held server is held`. Keep what each asserts; change only where it looks: `sent.single()` → the line that carries the fact (`sent.single { it.contains("<fact>") }`) or `sent.joinToString("\n")`. A test whose intent no longer holds under the approved layout (the "own word" marker is now the `Says` label) asserts the new form of the same fact. Ledger each changed test as a ruling.

- [ ] **Step 5: Pass**

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: build succeeds; the four new tests and every adapted test PASSED.

- [ ] **Step 6: Commit**

```bash
git -C /home/paul/git/spawnery commit -m "feat(agent): /cloud list and /cloud info in sections, with the node

list opens with a heading and sorts server groups and proxy groups into
sections, proxies under their group. info shows one labelled line per
fact, a players bar, and the node, or that the pod is not scheduled.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 5: /cloud status in sections and bars

**Files:**
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/StatusLines.kt` (rewrite)
- Test: `agent/common/src/test/kotlin/cloud/spawnery/agent/CloudCommandTest.kt` (replace the 0.9.0 status rendering tests)

**Interfaces:**
- Consumes: `Layout` (Task 3), `nodeText` (Task 4), `InstanceStatus.node()` (Task 2).
- Produces: `internal fun statusLines(status: NetworkStatus, target: String): List<String>` (same signature as today).

- [ ] **Step 1: Replace the tests**

Delete `status shows the network, its groups and the rest`, `status says when usage is unavailable`, `status says when nothing has a limit`, `status says when a limit covers only some containers`, `status says how many pods the usage covers`, `status of a server shows its ticks, its markers and its limits`, `status shows a server without a reported TPS as missing`. Keep `statusAnswer`, `usage`, and the refusal, permission and root tests. Add:

```kotlin
    @Test
    fun `status shows resources with bars and groups in sections`() {
        run("cloud status", api(aNetworkWithProxies()))
        statusAnswer {
            metricsAvailable = true
            players = 12; servers = 9; proxies = 2
            total = usage(3100, 8000, 12L shl 30, 24L shl 30, 11, 11).build()
            addGroups(
                cloud.spawnery.agent.pb.GroupStatus.newBuilder().setName("arena").setKind(GroupState.Kind.EPHEMERAL)
                    .setPhase("Ready").setReplicas(2).setReadyReplicas(2).setPlayers(6).setLowestTps(17.8)
                    .setUsage(usage(1100, 2000, 3L shl 30, 4L shl 30, 2, 2)),
            )
            addGroups(
                cloud.spawnery.agent.pb.GroupStatus.newBuilder().setName("gateway").setKind(GroupState.Kind.PROXY)
                    .setPhase("Ready").setReplicas(2).setReadyReplicas(2).setPlayers(12)
                    .setUsage(usage(200, 400, 1L shl 29, 1L shl 30, 2, 2)),
            )
            other = usage(500, 400, 1L shl 30, 1L shl 30, 2, 2).build()
        }
        assertTrue(sent[0].contains("<bold>Network status</bold>") && sent[0].contains("12 players"), sent[0])
        assertTrue(sent[1].contains("Resources"), sent[1])
        val cpu = sent.single { it.contains("CPU") && it.contains("cores") }
        assertTrue(cpu.contains("|") && cpu.contains("3.1 of 16.0") && cpu.contains("8.0 requested"), cpu)
        val ram = sent.single { it.contains("RAM") && it.contains("GiB") && it.contains("requested") }
        assertTrue(ram.contains("12.0 of 48.0"), ram)
        val serverSection = sent.indexOfFirst { it.contains("Server groups") }
        val proxySection = sent.indexOfFirst { it.contains("Proxy groups") }
        assertTrue(sent[serverSection + 1].contains("arena") && sent[serverSection + 1].contains("<yellow>17.8"), "$sent")
        assertTrue(sent[proxySection + 1].contains("gateway") && !sent[proxySection + 1].contains("TPS"), "$sent")
        assertTrue(sent.any { it.contains("Other pods") }, "$sent")
    }

    @Test
    fun `status says when usage is unavailable`() {
        run("cloud status", api(aNetworkWithProxies()))
        statusAnswer {
            metricsAvailable = false
            total = usage(0, 8000, 0, 24L shl 30, 11, 0).build()
        }
        assertTrue(sent.any { it.contains("metrics API not answering") }, "$sent")
        val cpu = sent.single { it.contains("CPU") && it.contains("cores") }
        assertTrue(cpu.contains("–") && !cpu.contains("|"), "an unmeasured value got a bar: $cpu")
    }

    @Test
    fun `status says when nothing has a limit and measures against the request`() {
        run("cloud status", api(aNetworkWithProxies()))
        statusAnswer {
            metricsAvailable = true
            total = usage(3100, 8000, 12L shl 30, 24L shl 30, 4, 4)
                .setCpuLimitMillicores(0).setCpuUnlimited(true).build()
        }
        val cpu = sent.single { it.contains("CPU") && it.contains("cores") }
        assertTrue(cpu.contains("no limit") && cpu.contains("|"), cpu)
    }

    @Test
    fun `status says how many pods the usage covers`() {
        run("cloud status", api(aNetworkWithProxies()))
        statusAnswer {
            metricsAvailable = true
            total = usage(3100, 8000, 12L shl 30, 24L shl 30, 9, 8).build()
        }
        assertTrue(sent.any { it.contains("8 of 9 pods") }, "$sent")
    }

    @Test
    fun `status of a server shows its node, bars and markers`() {
        run("cloud status lobby-r", api(aNetworkWithProxies()))
        statusAnswer {
            metricsAvailable = true
            total = usage(400, 500, 1L shl 30, 2L shl 30, 1, 1).build()
            addInstances(
                cloud.spawnery.agent.pb.InstanceStatus.newBuilder().setName("lobby-r").setGroup("lobby")
                    .setPhase("Retiring").setPlayers(3).setSlots(20).setTps(12.0).setMspt(80.0)
                    .setAgeSeconds(3 * 3600 + 20 * 60).setRetiring(true).setHeld(true).setNode("node-2")
                    .setUsage(usage(400, 500, 1L shl 30, 2L shl 30, 1, 1)),
            )
        }
        assertTrue(sent[0].contains("<bold>lobby-r</bold>") && sent[0].contains("3h20m"), sent[0])
        assertTrue(sent.any { it.contains("Node") && it.contains("node-2") }, "$sent")
        val tps = sent.single { it.contains("TPS") }
        assertTrue(tps.contains("<red>") && tps.contains("12.0") && tps.contains("80.0"), tps)
        assertTrue(sent.any { it.contains("Players") && it.contains("3 / 20") }, "$sent")
        assertTrue(sent.any { it.contains("Marked") && it.contains("retiring") && it.contains("held") }, "$sent")
    }

    @Test
    fun `status shows a server without a reported TPS as missing`() {
        run("cloud status lobby-r", api(aNetworkWithProxies()))
        statusAnswer {
            metricsAvailable = true
            total = usage(400, 500, 1L shl 30, 2L shl 30, 1, 1).build()
            addInstances(
                cloud.spawnery.agent.pb.InstanceStatus.newBuilder().setName("lobby-r").setGroup("lobby")
                    .setPhase("Ready").setNode("node-2").setUsage(usage(400, 500, 1L shl 30, 2L shl 30, 1, 1)),
            )
        }
        val tps = sent.single { it.contains("TPS") }
        assertTrue(tps.contains("–") && !tps.contains("|"), tps)
    }

    @Test
    fun `status of an unscheduled server says so`() {
        run("cloud status lobby-r", api(aNetworkWithProxies()))
        statusAnswer {
            metricsAvailable = true
            total = usage(0, 0, 0, 0, 0, 0).build()
            addInstances(
                cloud.spawnery.agent.pb.InstanceStatus.newBuilder().setName("lobby-r").setGroup("lobby")
                    .setPhase("Pending").setUsage(usage(0, 0, 0, 0, 0, 0)),
            )
        }
        assertTrue(sent.any { it.contains("Node") && it.contains("not scheduled") }, "$sent")
        assertTrue(sent.none { it.contains("CPU") && it.contains("|") }, "an unscheduled server got a usage bar: $sent")
    }

    @Test
    fun `status of a group lists its members with their node`() {
        run("cloud status lobby", api(aNetworkWithProxies()))
        statusAnswer {
            metricsAvailable = true
            total = usage(400, 500, 1L shl 30, 2L shl 30, 1, 1).build()
            addGroups(
                cloud.spawnery.agent.pb.GroupStatus.newBuilder().setName("lobby").setKind(GroupState.Kind.EPHEMERAL)
                    .setPhase("Ready").setReplicas(1).setReadyReplicas(1).setPlayers(3).setLowestTps(19.5)
                    .setUsage(usage(400, 500, 1L shl 30, 2L shl 30, 1, 1)),
            )
            addInstances(
                cloud.spawnery.agent.pb.InstanceStatus.newBuilder().setName("lobby-r").setGroup("lobby")
                    .setPhase("Ready").setPlayers(3).setSlots(20).setTps(19.5).setNode("node-2")
                    .setUsage(usage(400, 500, 1L shl 30, 2L shl 30, 1, 1)),
            )
        }
        assertTrue(sent[0].contains("<bold>lobby</bold>"), sent[0])
        val member = sent.single { it.contains("lobby-r") }
        assertTrue(member.startsWith("   ") && member.contains("node-2") && member.contains("<green>19.5"), member)
    }
```

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: FAIL — headings and sections absent.

- [ ] **Step 2: Rewrite `StatusLines.kt`**

```kotlin
package cloud.spawnery.agent

import cloud.spawnery.agent.api.Group
import cloud.spawnery.agent.api.GroupStatus
import cloud.spawnery.agent.api.InstanceStatus
import cloud.spawnery.agent.api.NetworkStatus
import cloud.spawnery.agent.api.ResourceUsage
import java.time.Duration
import java.util.Locale
import java.util.OptionalDouble

internal fun statusLines(status: NetworkStatus, target: String): List<String> {
    val metrics = status.metricsAvailable()
    val lines = mutableListOf<String>()
    when {
        target.isEmpty() -> {
            lines += Layout.heading(
                "Network status",
                Style.quiet("${status.players()} players · ${status.servers()} servers · ${status.proxies()} proxies"),
            )
            lines += Layout.section("Resources")
            lines += Layout.entry(cpuLine(status.total(), metrics))
            lines += Layout.entry(ramLine(status.total(), metrics))
            note(status.total(), metrics)?.let { lines += Layout.entry(it) }
            val (proxyGroups, serverGroups) = status.groups().partition { it.kind() == Group.Kind.PROXY }
            if (serverGroups.isNotEmpty()) {
                lines += Layout.section("Server groups")
                serverGroups.forEach { lines += Layout.entry(groupLine(it)) }
            }
            if (proxyGroups.isNotEmpty()) {
                lines += Layout.section("Proxy groups")
                proxyGroups.forEach { lines += Layout.entry(groupLine(it)) }
            }
            if (status.other().pods() > 0) {
                lines += Layout.section("Other pods")
                lines += Layout.entry(cpuRam(status.other()))
            }
        }
        status.groups().isNotEmpty() -> {
            val g = status.groups().single()
            lines += Layout.heading(g.name(), groupLine(g, withName = false))
            lines += Layout.section(if (g.kind() == Group.Kind.PROXY) "Proxies" else "Servers")
            status.instances().forEach { lines += Layout.entry(memberLine(it)) }
        }
        else -> lines += instanceLines(status.instances().single(), metrics)
    }
    return lines
}

private fun cores(milli: Long) = String.format(Locale.ROOT, "%.1f", milli / 1000.0)
private fun gib(bytes: Long) = String.format(Locale.ROOT, "%.1f", bytes / (1L shl 30).toDouble())
private fun one(v: Double) = String.format(Locale.ROOT, "%.1f", v)

/**
 * A resource line: the bar against the limit, or against the request where a
 * container has no limit, then used, limit and request in words.
 */
private fun resource(
    label: String?, used: Long, requested: Long, limit: Long, unlimited: Boolean,
    measured: Boolean, unit: String, show: (Long) -> String,
): String {
    val against = if (!unlimited && limit > 0) limit else requested
    val barPart = if (measured && against > 0) {
        val f = used.toDouble() / against
        Layout.bar(f, Layout.fillColour(f)) + "  "
    } else ""
    val usedText = if (measured) show(used) else "–"
    val limitText = when {
        !unlimited -> Style.number(usedText) + Style.quiet(" of ") + Style.number(show(limit)) + Style.quiet(" $unit")
        limit == 0L -> Style.number(usedText) + Style.quiet(" $unit · no limit")
        else -> Style.number(usedText) + Style.quiet(" $unit · limit ≥ ") + Style.number(show(limit))
    }
    return (if (label == null) "" else Style.quiet("$label  ")) + barPart + limitText + Style.quiet(" · ") +
        Style.number(show(requested)) + Style.quiet(" requested")
}

private fun cpuLine(u: ResourceUsage, metrics: Boolean, labelled: Boolean = true) = resource(
    if (labelled) "CPU" else null, u.cpuUsedMillicores(), u.cpuRequestedMillicores(), u.cpuLimitMillicores(), u.cpuUnlimited(),
    metrics && u.measured(), "cores", ::cores,
)

private fun ramLine(u: ResourceUsage, metrics: Boolean, labelled: Boolean = true) = resource(
    if (labelled) "RAM" else null, u.memoryUsedBytes(), u.memoryRequestedBytes(), u.memoryLimitBytes(), u.memoryUnlimited(),
    metrics && u.measured(), "GiB", ::gib,
)

private fun note(u: ResourceUsage, metrics: Boolean): String? = when {
    !metrics -> Style.bad("usage unavailable (metrics API not answering)")
    u.measured() && !u.complete() -> Style.quiet("usage of ${u.podsMeasured()} of ${u.pods()} pods")
    else -> null
}

private fun cpuRam(u: ResourceUsage): String =
    if (!u.measured()) {
        Layout.joined(Style.quiet("CPU ") + Style.number("–"), Style.quiet("RAM ") + Style.number("–"))
    } else {
        Layout.joined(
            Style.quiet("CPU ") + Style.number(cores(u.cpuUsedMillicores())),
            Style.quiet("RAM ") + Style.number(gib(u.memoryUsedBytes()) + " GiB"),
        )
    }

private fun tpsText(value: OptionalDouble): String =
    if (value.isEmpty) Style.quiet("TPS ") + Style.number("–")
    else Style.quiet("TPS ") + "<${Layout.tpsColour(value.asDouble)}>" + Style.escape(one(value.asDouble)) +
        "</${Layout.tpsColour(value.asDouble)}>"

private fun groupLine(g: GroupStatus, withName: Boolean = true): String {
    val proxy = g.kind() == Group.Kind.PROXY
    return Layout.joined(
        if (withName) Style.name(g.name()) else "",
        Style.number(g.phase()),
        Style.number("${g.readyReplicas()}/${g.replicas()}"),
        Style.number(g.players()) + Style.quiet(" players"),
        if (proxy) "" else tpsText(g.lowestTps()),
        cpuRam(g.usage()),
    )
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

private fun markers(i: InstanceStatus): String = listOfNotNull(
    if (i.retiring()) "retiring" else null,
    if (i.held()) "held" else null,
    if (i.draining()) "draining" else null,
).joinToString(Style.quiet(" · ")) { Style.bad(it) }

private fun memberLine(i: InstanceStatus): String = Layout.joined(
    Style.name(i.name()),
    if (i.proxy()) (if (i.ready()) Style.good("ready") else Style.bad("not ready")) else Style.number(i.phase()),
    Style.number("${i.players()}/${i.slots()}"),
    if (i.proxy()) "" else tpsText(i.tps()),
    cpuRam(i.usage()),
    nodeText(i.node()),
    Style.quiet("up ") + Style.number(age(i.age())),
    markers(i),
)

private fun instanceLines(i: InstanceStatus, metrics: Boolean): List<String> {
    val lines = mutableListOf(
        Layout.heading(
            i.name(),
            Layout.joined(
                Style.quiet(if (i.proxy()) "proxy in " else "server in ") + Style.name(i.group()),
                if (i.proxy()) (if (i.ready()) Style.good("ready") else Style.bad("not ready")) else Style.number(i.phase()),
                Style.quiet("up ") + Style.number(age(i.age())),
            ),
        ),
        Layout.field("Node", nodeText(i.node())),
    )
    val fill = if (i.slots() > 0) i.players().toDouble() / i.slots() else 0.0
    lines += Layout.field(
        "Players",
        (if (i.slots() > 0) Layout.bar(fill, Layout.fillColour(fill)) + "  " else "") +
            Style.number("${i.players()} / ${i.slots()}"),
    )
    if (!i.proxy()) {
        val tps = i.tps()
        val mspt = if (i.mspt().isEmpty) "–" else one(i.mspt().asDouble)
        lines += Layout.field(
            "TPS",
            if (tps.isEmpty) Style.number("–") + Style.quiet(" · MSPT ") + Style.number(mspt)
            else Layout.bar(tps.asDouble / 20, Layout.tpsColour(tps.asDouble)) + "  " +
                "<${Layout.tpsColour(tps.asDouble)}>" + Style.escape(one(tps.asDouble)) + "</${Layout.tpsColour(tps.asDouble)}>" +
                Style.quiet(" · MSPT ") + Style.number(mspt),
        )
    }
    if (i.usage().pods() > 0) {
        lines += Layout.field("CPU", cpuLine(i.usage(), metrics, labelled = false))
        lines += Layout.field("RAM", ramLine(i.usage(), metrics, labelled = false))
    }
    markers(i).takeIf { it.isNotEmpty() }?.let { lines += Layout.field("Marked", it) }
    return lines
}
```

- [ ] **Step 3: Pass**

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: build succeeds; every status test PASSED.

- [ ] **Step 4: Commit**

```bash
git -C /home/paul/git/spawnery commit -m "feat(agent): /cloud status in sections, with bars and the node

The network form shows CPU and RAM as bars against their limit, or
their request where a container has none, then server groups, proxy
groups and the other pods in sections. A group lists its members with
node and age; a server or proxy shows players, TPS, CPU and RAM as bars
and names its node.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 6: ✔ and ✘ on the one-line answers

**Files:**
- Modify: `agent/common/src/main/kotlin/cloud/spawnery/agent/CloudCommand.kt`
- Test: `agent/common/src/test/kotlin/cloud/spawnery/agent/CloudCommandTest.kt`

**Interfaces:**
- Consumes: `Layout.ok`, `Layout.fail` (Task 3).

- [ ] **Step 1: Failing test**

```kotlin
    @Test
    fun `one-line answers say at a glance whether it worked`() {
        run("cloud retire lobby-a")
        answer { setRetire(RetireResult.newBuilder().setServer("lobby-a")) }
        assertTrue(sent.single().startsWith("<green>✔</green> "), sent.single())
        sent.clear(); requested.clear()

        run("cloud retire lobby-a")
        answer { setError(RequestError.newBuilder().setReason(RequestError.Reason.REFUSED).setMessage("already retiring")) }
        assertTrue(sent.single().startsWith("<red>✘</red> ") && sent.single().contains("already retiring"), sent.single())
        sent.clear(); requested.clear()

        run("cloud events off")
        assertTrue(sent.single().startsWith("<green>✔</green> "), sent.single())
    }
```

Use the file's actual `RetireResult` builder fields (read the existing retire test first).

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: FAIL on the first `startsWith`.

- [ ] **Step 2: Implement**

Wrap the first line of every outcome:

- `Layout.ok(...)`: retire success, unretire success, the first line of a boost success, stop with removed > 0, stop with nothing to remove (`had no boosts running` — the command did what was asked), events on, events off.
- `Layout.fail(...)`: could not retire, could not unretire, could not boost, could not stop boosts, could not read (duration), the console refusal, `no status`, info's not-found line (already done in Task 4).

The boost's second and third lines stay as they are (explanations, not outcomes).

- [ ] **Step 3: Pass**

Run: `git -C /home/paul/git/spawnery add -A agent && $NIX make -C /home/paul/git/spawnery agent`
Expected: build succeeds; the new test and all existing tests PASSED. An existing test that compared a reply with `startsWith` or `==` on its text is adapted to the marked form and ledgered.

- [ ] **Step 4: Commit**

```bash
git -C /home/paul/git/spawnery commit -m "feat(agent): /cloud one-line answers begin with ✔ or ✘

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 7: Docs

**Files:**
- Modify: `docs/guides/cloud-command.md` (the node in `info` and `status`; one sentence on the layout)
- Modify: `docs/plugin-api/what-a-plugin-can-do.md` (`node()`)

- [ ] **Step 1: Write**

In `cloud-command.md`, after the `/cloud status` paragraph:

```markdown
`/cloud info` and `/cloud status` name the node a server or proxy runs on, or
say it is not scheduled yet. Nothing else about the node is shown.

Every answer opens with a heading and sorts what follows into sections; bars
show players against slots, TPS against 20, and CPU and memory against their
limit (or their request where a container has no limit). One-line answers
begin with ✔ or ✘.
```

In `what-a-plugin-can-do.md`, after the `status()` paragraph:

```markdown
`ServerInfo.node()`, `ProxyInfo.node()` and `InstanceStatus.node()` name the
Kubernetes node the pod runs on, empty while it is not scheduled.
```

Run: `$NIX make -C /home/paul/git/spawnery docs-length-lint`
Expected: exit 0.

- [ ] **Step 2: Commit**

```bash
git -C /home/paul/git/spawnery add docs
git -C /home/paul/git/spawnery commit -m "docs(guides): the node, and /cloud answers in sections

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

## After the last task

Whole suite, then the final review. The release 0.10.0 (operator, chart and images; `flake.nix`, `Chart.yaml` with a paragraph each, `values.yaml`, image tags in docs and samples) is its own `chore: 0.10.0, …` commit after review, as for 0.9.0.
