package cloud.spawnery.agent.velocity

import org.junit.jupiter.api.Test
import java.util.UUID
import kotlin.test.assertEquals
import kotlin.test.assertIs

class TransferCookieTest {
    private val secret = "s3cret".toByteArray()
    private val alice = UUID.fromString("00000000-0000-0000-0000-00000000000a")
    private val bob = UUID.fromString("00000000-0000-0000-0000-00000000000b")
    private var now = 1_000L
    private val cookie = TransferCookie(secret) { now }

    @Test fun `a cookie written for a player reads back its target`() {
        assertEquals(TransferCookie.Result.Valid("lobby-1"), cookie.read(alice, cookie.write(alice, "lobby-1")))
    }
    @Test fun `another player's cookie is refused`() {
        assertIs<TransferCookie.Result.Refused>(cookie.read(bob, cookie.write(alice, "lobby-1")))
    }
    @Test fun `an expired cookie is refused`() {
        val bytes = cookie.write(alice, "lobby-1"); now += 61
        assertIs<TransferCookie.Result.Refused>(cookie.read(alice, bytes))
    }
    @Test fun `a tampered target is refused`() {
        val bytes = cookie.write(alice, "lobby-1")
        val forged = String(bytes).replace("lobby-1", "arena-1").toByteArray()
        assertIs<TransferCookie.Result.Refused>(cookie.read(alice, forged))
    }
    @Test fun `a cookie signed with another secret is refused`() {
        val other = TransferCookie("other".toByteArray()) { now }
        assertIs<TransferCookie.Result.Refused>(cookie.read(alice, other.write(alice, "lobby-1")))
    }
    @Test fun `garbage is refused, not thrown`() {
        assertIs<TransferCookie.Result.Refused>(cookie.read(alice, byteArrayOf(1, 2, 3)))
    }
}
