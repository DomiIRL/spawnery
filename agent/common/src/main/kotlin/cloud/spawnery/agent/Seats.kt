package cloud.spawnery.agent

internal fun seatsText(players: Int, playable: Int, slots: Int, spaced: Boolean): String {
    val of = if (spaced) " / " else "/"
    return if (playable in 1 until slots) "$players$of$playable · max $slots" else "$players$of$slots"
}

internal fun seatsFill(players: Int, playable: Int, slots: Int): Double {
    val capacity = if (playable in 1..slots) playable else slots
    return if (capacity > 0) (players.toDouble() / capacity).coerceAtMost(1.0) else 0.0
}
