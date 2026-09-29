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
            GroupStatus(
                g.name, kindOf(g.kind), g.phase, g.replicas, g.readyReplicas, g.players,
                reported(g.lowestTps), usage(g.usage),
            )
        },
        pb.instancesList.map { i ->
            InstanceStatus(
                i.name, i.group, i.proxy, i.phase, i.ready, i.players, i.slots,
                reported(i.tps), reported(i.mspt), Duration.ofSeconds(i.ageSeconds),
                i.retiring, i.held, i.draining, usage(i.usage), i.node, i.playableSlots,
            )
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
