package cloud.spawnery.agent.velocity

import cloud.spawnery.agent.api.ProxyInfo
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicLong

class TransferPolicy(private val forceAfterMillis: Long, private val clock: () -> Long) {
    data class Picture(
        val self: String,
        val group: String,
        val proxies: List<ProxyInfo>,
        val closedDoors: Set<String>,
    )
    data class Occupant(val id: UUID, val server: String?)

    private val firstLeavingAt = AtomicLong(0)
    private val tried = ConcurrentHashMap.newKeySet<UUID>()

    fun leaving(picture: Picture): Boolean {
        val leaving = picture.proxies.any { it.name() == picture.self && it.draining() }
        if (leaving) firstLeavingAt.compareAndSet(0, clock())
        return leaving
    }

    fun forced(picture: Picture, occupants: List<Occupant>): List<Pair<UUID, String>> {
        if (!leaving(picture) || !somewhereElse(picture)) return emptyList()
        if (clock() - firstLeavingAt.get() < forceAfterMillis) return emptyList()

        val result = mutableListOf<Pair<UUID, String>>()
        for (occupant in occupants) {
            val server = occupant.server ?: continue
            if (server in picture.closedDoors) continue
            if (tried.add(occupant.id)) result += occupant.id to server
        }
        return result
    }

    fun onSwitch(picture: Picture, player: UUID): Boolean {
        if (!leaving(picture) || !somewhereElse(picture)) return false
        return tried.add(player)
    }

    fun forget(player: UUID) {
        tried.remove(player)
    }

    private fun somewhereElse(picture: Picture): Boolean =
        picture.proxies.any { it.group() == picture.group && it.name() != picture.self && it.ready() && !it.draining() }
}
