package cloud.spawnery.agent

/** The JVM heap in use and its maximum, in bytes, as both agents report it. */
fun heapNow(runtime: Runtime = Runtime.getRuntime()): Pair<Long, Long> =
    (runtime.totalMemory() - runtime.freeMemory()) to runtime.maxMemory()
