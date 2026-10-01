package cloud.spawnery.agent.velocity

import cloud.spawnery.agent.api.ProxyInfo
import org.junit.jupiter.api.Test
import java.util.UUID
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class TransferPolicyTest {
    private val alice = UUID.fromString("00000000-0000-0000-0000-00000000000a")
    private val bob = UUID.fromString("00000000-0000-0000-0000-00000000000b")
    private var now = 1_000L

    private fun policy(forceAfterMillis: Long = 1_000L) = TransferPolicy(forceAfterMillis) { now }

    private fun picture(
        proxies: List<ProxyInfo>,
        self: String = "edge-1",
        group: String = "edge",
        closedDoors: Set<String> = emptySet(),
        accepting: Set<String> = proxies.map { it.name() }.toSet(),
    ) = TransferPolicy.Picture(self, group, proxies, closedDoors, accepting)

    private val leavingAlone = listOf(
        ProxyInfo("edge-1", "edge", false, true, 0, ""),
        ProxyInfo("edge-2", "edge", true, false, 0, ""),
    )

    @Test
    fun `a proxy that is not draining transfers nothing`() {
        val p = policy()
        val pic = picture(listOf(ProxyInfo("edge-1", "edge", true, false, 0, ""), ProxyInfo("edge-2", "edge", true, false, 0, "")))

        assertFalse(p.onSwitch(pic, alice))
        assertEquals(emptyList(), p.forced(pic, listOf(TransferPolicy.Occupant(alice, "lobby-1"))))
    }

    @Test
    fun `leaving with no other ready, non-draining proxy of the group transfers nothing`() {
        val p = policy()
        val pic = picture(
            listOf(
                ProxyInfo("edge-1", "edge", false, true, 0, ""),
                ProxyInfo("hub-1", "hub", true, false, 0, ""),
            ),
        )

        assertFalse(p.onSwitch(pic, alice))
        assertEquals(emptyList(), p.forced(pic, listOf(TransferPolicy.Occupant(alice, "lobby-1"))))
    }

    @Test
    fun `before the deadline a switch becomes a transfer but nothing is forced`() {
        val p = policy(forceAfterMillis = 1_000L)
        val pic = picture(leavingAlone)

        assertTrue(p.onSwitch(pic, alice))
        assertEquals(emptyList(), p.forced(pic, listOf(TransferPolicy.Occupant(bob, "lobby-1"))))
    }

    @Test
    fun `after the deadline an open occupant is forced, a closed one is not, and is forced once the door opens`() {
        val p = policy(forceAfterMillis = 1_000L)
        val closed = picture(leavingAlone, closedDoors = setOf("lobby-2"))
        assertTrue(p.leaving(closed))
        now += 1_000

        val forced = p.forced(
            closed,
            listOf(TransferPolicy.Occupant(alice, "lobby-1"), TransferPolicy.Occupant(bob, "lobby-2")),
        )
        assertEquals(listOf(alice to "lobby-1"), forced)

        val open = picture(leavingAlone)
        assertEquals(listOf(bob to "lobby-2"), p.forced(open, listOf(TransferPolicy.Occupant(bob, "lobby-2"))))
    }

    @Test
    fun `a player forced once is not forced again, and a player who switched is not forced`() {
        val p = policy(forceAfterMillis = 0L)
        val pic = picture(leavingAlone)

        assertEquals(listOf(alice to "lobby-1"), p.forced(pic, listOf(TransferPolicy.Occupant(alice, "lobby-1"))))
        assertEquals(emptyList(), p.forced(pic, listOf(TransferPolicy.Occupant(alice, "lobby-1"))))

        assertTrue(p.onSwitch(pic, bob))
        assertEquals(emptyList(), p.forced(pic, listOf(TransferPolicy.Occupant(bob, "lobby-1"))))
    }

    @Test
    fun `an occupant without a server is not forced`() {
        val p = policy(forceAfterMillis = 0L)
        val pic = picture(leavingAlone)

        assertEquals(emptyList(), p.forced(pic, listOf(TransferPolicy.Occupant(alice, null))))
    }

    @Test
    fun `the clock starts at the first leaving call that returns true, not at construction`() {
        val p = policy(forceAfterMillis = 500L)
        val notLeaving = picture(listOf(ProxyInfo("edge-1", "edge", true, false, 0, ""), ProxyInfo("edge-2", "edge", true, false, 0, "")))
        assertFalse(p.leaving(notLeaving))

        now = 5_000L
        // If the clock had started at construction (now=1_000), the 500ms
        // deadline would already be long past by now=5_000.
        assertEquals(emptyList(), p.forced(picture(leavingAlone), listOf(TransferPolicy.Occupant(alice, "lobby-1"))))
    }

    @Test
    fun `a peer whose Velocity refuses transfers is nowhere to land`() {
        val p = policy(forceAfterMillis = 0L)
        val pic = picture(leavingAlone, accepting = setOf("edge-1"))

        assertFalse(p.onSwitch(pic, alice))
        assertEquals(emptyList(), p.forced(pic, listOf(TransferPolicy.Occupant(alice, "lobby-1"))))
    }

    @Test
    fun `a draining proxy that is still ready is not leaving yet`() {
        val p = policy(forceAfterMillis = 0L)
        val pic = picture(listOf(ProxyInfo("edge-1", "edge", true, true, 0, ""), ProxyInfo("edge-2", "edge", true, false, 0, "")))

        assertFalse(p.leaving(pic))
        assertFalse(p.onSwitch(pic, alice))
        assertEquals(emptyList(), p.forced(pic, listOf(TransferPolicy.Occupant(alice, "lobby-1"))))
    }

    @Test
    fun `a player who comes back to the leaving proxy is not transferred again`() {
        val p = policy(forceAfterMillis = 0L)
        val pic = picture(leavingAlone)

        assertEquals(listOf(alice to "lobby-1"), p.forced(pic, listOf(TransferPolicy.Occupant(alice, "lobby-1"))))
        p.forget(pic, alice)

        assertEquals(emptyList(), p.forced(pic, listOf(TransferPolicy.Occupant(alice, "lobby-1"))))
        assertFalse(p.onSwitch(pic, alice))
    }

    @Test
    fun `a cancelled scale-down starts the clock and the tried players afresh`() {
        val p = policy(forceAfterMillis = 1_000L)
        val leaving = picture(leavingAlone)
        val stayed = picture(listOf(ProxyInfo("edge-1", "edge", true, false, 0, ""), ProxyInfo("edge-2", "edge", true, false, 0, "")))
        assertTrue(p.leaving(leaving))
        now += 1_000
        assertEquals(listOf(alice to "lobby-1"), p.forced(leaving, listOf(TransferPolicy.Occupant(alice, "lobby-1"))))

        assertFalse(p.leaving(stayed))
        assertEquals(emptyList(), p.forced(leaving, listOf(TransferPolicy.Occupant(alice, "lobby-1"))))
        now += 1_000
        assertEquals(listOf(alice to "lobby-1"), p.forced(leaving, listOf(TransferPolicy.Occupant(alice, "lobby-1"))))
    }
}
