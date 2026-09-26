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
