package cloud.spawnery.agent.paper

import java.util.UUID
import java.util.concurrent.ConcurrentHashMap

/**
 * Players admitted past the login check who have not joined yet. Released on
 * join, quit or a closed connection -- the close event may arrive off the main
 * thread, hence the concurrent map -- and expired after [ttlMillis] in case
 * none of those ever comes.
 */
class PendingSeats(private val clock: () -> Long, private val ttlMillis: Long = 300_000) {
    private val admitted = ConcurrentHashMap<UUID, Long>()

    fun admit(player: UUID) {
        admitted[player] = clock()
    }

    fun release(player: UUID) {
        admitted.remove(player)
    }

    fun count(): Int {
        val now = clock()
        admitted.entries.removeIf { now - it.value > ttlMillis }
        return admitted.size
    }
}
