package cloud.spawnery.agent.velocity

import cloud.spawnery.agent.api.ProxyInfo
import org.junit.jupiter.api.Test
import java.net.InetSocketAddress
import java.util.UUID
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

class TransfersTest {
    private val secret = "s3cret".toByteArray()
    private var seconds = 1_000L
    private val cookie = TransferCookie(secret) { seconds }
    private val host = InetSocketAddress.createUnresolved("play.example.net", 25565)

    private val leaving = listOf(
        ProxyInfo("edge-1", "edge", true, true, 0, ""),
        ProxyInfo("edge-2", "edge", true, false, 0, ""),
    )
    private val staying = listOf(
        ProxyInfo("edge-1", "edge", true, false, 0, ""),
        ProxyInfo("edge-2", "edge", true, false, 0, ""),
    )
    private var proxies = leaving
    private var registered = setOf("lobby-1", "arena-1")

    val infos = mutableListOf<String>()
    val warnings = mutableListOf<Pair<String, Throwable?>>()

    private val transfers = Transfers(
        cookie = cookie,
        policy = TransferPolicy(0L) { seconds * 1000 },
        picture = { TransferPolicy.Picture("edge-1", "edge", proxies, emptySet()) },
        registered = { it in registered },
        info = { infos += it },
        warn = { message, error -> warnings += message to error },
    )

    private class FakeTraveller(
        override val username: String,
        override val currentServer: String?,
        override val virtualHost: InetSocketAddress?,
        var failWith: Exception? = null,
    ) : Traveller {
        override val uuid: UUID = UUID.nameUUIDFromBytes(username.toByteArray())
        val sent = mutableListOf<Pair<ByteArray, InetSocketAddress>>()

        override fun transfer(cookie: ByteArray, to: InetSocketAddress) {
            failWith?.let { throw it }
            sent += cookie to to
        }
    }

    @Test
    fun `a switch on a leaving proxy is denied, and the player is sent away carrying the target`() {
        val alice = FakeTraveller("alice", "lobby-1", host)

        assertTrue(transfers.onSwitch(alice, "arena-1"))

        assertEquals(1, alice.sent.size)
        val (bytes, to) = alice.sent.single()
        assertEquals(host, to)
        assertEquals(TransferCookie.Result.Valid("arena-1"), cookie.read(alice.uuid, bytes))
        assertEquals(listOf("spawnery: transferred 'alice' (switch) toward 'arena-1'"), infos)
    }

    @Test
    fun `a switch on a proxy that is not leaving is left alone`() {
        proxies = staying
        val alice = FakeTraveller("alice", "lobby-1", host)

        assertFalse(transfers.onSwitch(alice, "arena-1"))
        assertTrue(alice.sent.isEmpty())
    }

    @Test
    fun `a player without a virtual host is never transferred`() {
        val alice = FakeTraveller("alice", "lobby-1", null)

        assertFalse(transfers.onSwitch(alice, "arena-1"))
        transfers.pass(listOf(alice))
        assertTrue(alice.sent.isEmpty())
    }

    @Test
    fun `a pass transfers every forced occupant toward their current server`() {
        val alice = FakeTraveller("alice", "lobby-1", host)
        val bob = FakeTraveller("bob", "arena-1", host)

        transfers.pass(listOf(alice, bob))

        assertEquals(TransferCookie.Result.Valid("lobby-1"), cookie.read(alice.uuid, alice.sent.single().first))
        assertEquals(TransferCookie.Result.Valid("arena-1"), cookie.read(bob.uuid, bob.sent.single().first))
        assertEquals(
            setOf(
                "spawnery: transferred 'alice' (forced) toward 'lobby-1'",
                "spawnery: transferred 'bob' (forced) toward 'arena-1'",
            ),
            infos.toSet(),
        )
    }

    @Test
    fun `a transfer that throws is logged and not tried again`() {
        val alice = FakeTraveller("alice", "lobby-1", host, failWith = IllegalArgumentException("too old"))

        transfers.pass(listOf(alice))
        transfers.pass(listOf(alice))

        assertEquals(1, warnings.size)
        assertTrue(warnings.single().first.contains("'alice'"), warnings.single().first)
        assertTrue(infos.isEmpty())
    }

    @Test
    fun `a switch whose transfer throws goes ahead as a normal switch`() {
        val alice = FakeTraveller("alice", "lobby-1", host, failWith = IllegalStateException("closing"))

        assertFalse(transfers.onSwitch(alice, "arena-1"))
        assertEquals(1, warnings.size)
    }

    @Test
    fun `an arrival with a valid cookie lands on the named server`() {
        val alice = UUID.nameUUIDFromBytes("alice".toByteArray())
        var resumed = 0
        transfers.expecting(alice) { resumed++ }

        assertTrue(transfers.received(alice, "alice", cookie.write(alice, "arena-1")))

        assertEquals(1, resumed)
        assertEquals("arena-1", transfers.landing(alice, "alice"))
        assertNull(transfers.landing(alice, "alice"))
    }

    @Test
    fun `an arrival carrying another player's cookie is routed as a fresh join`() {
        val alice = UUID.nameUUIDFromBytes("alice".toByteArray())
        val bob = UUID.nameUUIDFromBytes("bob".toByteArray())
        transfers.expecting(alice) {}

        transfers.received(alice, "alice", cookie.write(bob, "arena-1"))

        assertNull(transfers.landing(alice, "alice"))
        assertEquals(
            listOf("spawnery: transfer cookie from 'alice' refused: other player"),
            warnings.map { it.first },
        )
    }

    @Test
    fun `an arrival naming a server that is not registered is routed as a fresh join`() {
        val alice = UUID.nameUUIDFromBytes("alice".toByteArray())
        transfers.expecting(alice) {}
        transfers.received(alice, "alice", cookie.write(alice, "arena-9"))

        assertNull(transfers.landing(alice, "alice"))
        assertTrue(warnings.single().first.startsWith("spawnery: transfer cookie from 'alice' refused: "))
        assertTrue(warnings.single().first.contains("arena-9"))
    }

    @Test
    fun `an arrival without a cookie is routed as a fresh join`() {
        val alice = UUID.nameUUIDFromBytes("alice".toByteArray())
        transfers.expecting(alice) {}

        assertTrue(transfers.received(alice, "alice", null))

        assertNull(transfers.landing(alice, "alice"))
        assertEquals(1, warnings.size)
    }

    @Test
    fun `a timed-out wait resumes once, and the late answer is still claimed but not used`() {
        val alice = UUID.nameUUIDFromBytes("alice".toByteArray())
        var resumed = 0
        transfers.expecting(alice) { resumed++ }

        transfers.gaveUp(alice, "alice", "no answer")
        assertTrue(transfers.received(alice, "alice", cookie.write(alice, "arena-1")))
        transfers.gaveUp(alice, "alice", "no answer")

        assertEquals(1, resumed)
        assertNull(transfers.landing(alice, "alice"))
    }

    @Test
    fun `a cookie nobody asked for is not claimed`() {
        val alice = UUID.nameUUIDFromBytes("alice".toByteArray())

        assertFalse(transfers.received(alice, "alice", cookie.write(alice, "arena-1")))
        assertNull(transfers.landing(alice, "alice"))
    }
}
