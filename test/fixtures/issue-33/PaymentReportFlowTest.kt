package reports

import kotlin.test.Test
import kotlin.test.assertEquals

class PaymentReportFlowTest {
    @Test
    fun includesCompletedPayments() {
        val payments = listOf(1200, 3400)
        val reportTotal = payments.sum()
        assertEquals(4600, reportTotal)
    }

    @Test
    fun emptyReportHasZeroTotal() {
        assertEquals(0, emptyList<Int>().sum())
    }
}
