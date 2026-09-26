package cloud.spawnery.agent

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

class LayoutTest {
    private fun segments(bar: String, colour: String): Int =
        Regex("<$colour>(\\|*)</$colour>").find(bar)?.groupValues?.get(1)?.length ?: 0

    @Test
    fun `a bar has twenty segments, the filled part coloured`() {
        val bar = Layout.bar(0.5, "green")
        assertEquals(10, segments(bar, "green"))
        assertEquals(10, segments(bar, "dark_gray"))
    }

    @Test
    fun `a bar clamps what overflows`() {
        assertEquals(20, segments(Layout.bar(1.7, "red"), "red"))
        assertEquals(0, segments(Layout.bar(-0.2, "red"), "red"))
        assertEquals(20, segments(Layout.bar(-0.2, "red"), "dark_gray"))
        assertEquals(0, segments(Layout.bar(Double.NaN, "red"), "red"))
    }

    @Test
    fun `fill colours change at seventy and ninety percent`() {
        assertEquals("green", Layout.fillColour(0.0))
        assertEquals("green", Layout.fillColour(0.69))
        assertEquals("yellow", Layout.fillColour(0.70))
        assertEquals("yellow", Layout.fillColour(0.89))
        assertEquals("red", Layout.fillColour(0.90))
        assertEquals("red", Layout.fillColour(1.0))
    }

    @Test
    fun `tps colours change at nineteen and fifteen`() {
        assertEquals("green", Layout.tpsColour(19.0))
        assertEquals("yellow", Layout.tpsColour(18.99))
        assertEquals("yellow", Layout.tpsColour(15.0))
        assertEquals("red", Layout.tpsColour(14.99))
    }

    @Test
    fun `indentation marks sections, entries and members`() {
        assertTrue(Layout.section("Resources").startsWith(" <"))
        assertTrue(Layout.entry("x").startsWith("   x"))
        assertTrue(Layout.member("x").startsWith("     x"))
    }

    @Test
    fun `a heading is bold with a grey summary`() {
        val line = Layout.heading("Network", Style.quiet("3 groups"))
        assertTrue(line.contains("<bold>Network</bold>") && line.contains("<gray>3 groups</gray>"), line)
    }

    @Test
    fun `joined leaves out empty parts`() {
        assertEquals("a<gray> · </gray>b", Layout.joined("a", "", "b"))
    }

    @Test
    fun `outcomes are marked`() {
        assertTrue(Layout.ok("done").startsWith("<green>✔</green> "))
        assertTrue(Layout.fail("no").startsWith("<red>✘</red> "))
    }

    @Test
    fun `a label or value with markup prints as text`() {
        val line = Layout.field("Says <b>", Style.name("<red>x"))
        assertTrue(line.contains("\\<b>") && line.contains("\\<red>x"), line)
    }
}
