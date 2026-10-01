package cloud.spawnery.agent.velocity

import java.security.MessageDigest
import java.util.Base64
import java.util.UUID
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec

class TransferCookie(secret: ByteArray, private val clock: () -> Long) {
    private val key: SecretKeySpec = run {
        val digest = MessageDigest.getInstance("SHA-256")
        digest.update("spawnery-transfer-v1\n".toByteArray(Charsets.UTF_8))
        digest.update(secret)
        SecretKeySpec(digest.digest(), "HmacSHA256")
    }

    fun write(player: UUID, target: String): ByteArray {
        val payload = "$player|$target|${clock() + LIFETIME_SECONDS}"
        val signature = Base64.getUrlEncoder().withoutPadding().encodeToString(sign(payload))
        return "$payload|$signature".toByteArray(Charsets.UTF_8)
    }

    fun read(player: UUID, bytes: ByteArray): Result {
        val text = String(bytes, Charsets.UTF_8)
        val signatureBar = text.lastIndexOf('|')
        if (signatureBar < 0) return Result.Refused("malformed")
        val payload = text.substring(0, signatureBar)
        val signature = text.substring(signatureBar + 1)

        val expiryBar = payload.lastIndexOf('|')
        val uuidBar = payload.indexOf('|')
        if (expiryBar < 0 || uuidBar < 0 || uuidBar >= expiryBar) return Result.Refused("malformed")
        val uuid = runCatching { UUID.fromString(payload.substring(0, uuidBar)) }.getOrNull()
            ?: return Result.Refused("malformed")
        val target = payload.substring(uuidBar + 1, expiryBar)
        val expiry = payload.substring(expiryBar + 1).toLongOrNull() ?: return Result.Refused("malformed")
        val mac = runCatching { Base64.getUrlDecoder().decode(signature) }.getOrNull()
            ?: return Result.Refused("malformed")

        if (!MessageDigest.isEqual(mac, sign(payload))) return Result.Refused("bad signature")
        if (uuid != player) return Result.Refused("other player")
        if (clock() >= expiry) return Result.Refused("expired")
        return Result.Valid(target)
    }

    private fun sign(payload: String): ByteArray {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(key)
        return mac.doFinal(payload.toByteArray(Charsets.UTF_8))
    }

    sealed interface Result {
        data class Valid(val target: String) : Result
        data class Refused(val reason: String) : Result
    }

    companion object {
        const val KEY = "spawnery:transfer"
        const val LIFETIME_SECONDS = 60L
    }
}
