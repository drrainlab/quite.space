package space.quiet.arprobe

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.hardware.usb.UsbConstants
import android.hardware.usb.UsbDevice
import android.hardware.usb.UsbDeviceConnection
import android.hardware.usb.UsbEndpoint
import android.hardware.usb.UsbInterface
import android.hardware.usb.UsbManager
import android.os.Build
import android.util.Log
import space.quiet.quietcore.UsbHost
import space.quiet.quietcore.UsbSerial

/**
 * The RNode's cable, on a phone.
 *
 * WHY ANY OF THIS EXISTS. Every other host opens a serial path and is done.
 * Android has none: it creates no device node for a USB peripheral, so /dev
 * holds one `tty` and nothing a driver could open — measured on the device.
 * The board is reachable only through [UsbManager], from here, behind a
 * permission the person grants for that specific device.
 *
 * So this file acquires the hardware and NOTHING else. It speaks no KISS, it
 * knows no frames, it has never heard of a segment: it hands the core three
 * verbs and the same modem driver a laptop uses does the rest. That division
 * is deliberate — a second protocol implementation living on the Kotlin side
 * would drift from the first one the week after it was written.
 *
 * THE CHIP. RNode boards put a USB-to-UART bridge in front of the MCU, and
 * which one decides how much work this is. The board this was written against
 * reports Silicon Labs CP2102 (10c4:ea60), which is the kind case: four
 * vendor control transfers to set the line up, then plain bulk in and out
 * with no framing of its own. FTDI, by contrast, prefixes two status bytes to
 * every read; if one of those turns up, it needs its own driver here rather
 * than a flag.
 */
object UsbRadio {

    private const val TAG = "quiet-usb"
    private const val ACTION_PERMISSION = "space.quiet.arprobe.USB_PERMISSION"

    // Silicon Labs CP210x. The product ids differ across the family; the
    // vendor is what identifies the protocol.
    private const val VENDOR_SILABS = 0x10C4

    // CP210x vendor requests (AN571). Sent to the interface, host-to-device.
    private const val REQ_IFC_ENABLE = 0x00
    private const val REQ_SET_LINE_CTL = 0x03
    private const val REQ_SET_MHS = 0x07
    private const val REQ_SET_BAUDRATE = 0x1E

    private const val UART_ENABLE = 0x0001
    private const val LINE_CTL_8N1 = 0x0800     // 8 data bits, no parity, 1 stop

    // DTR and RTS asserted, both with their write masks set. THIS IS THE
    // RESET: opening a serial port on a desktop toggles DTR and the ESP32
    // reboots, which is why the driver waits two seconds before configuring.
    // Nothing does that for us here, so it is done explicitly or the driver
    // ends up talking to a board in whatever state it was left in.
    private const val MHS_DTR_RTS_ON = 0x0303

    private const val BAUD = 115200
    private const val REQ_TYPE_HOST_TO_DEVICE = 0x41   // vendor, interface
    private const val CONTROL_TIMEOUT_MS = 2000

    /** What the interface can show in a list. */
    data class Candidate(
        val deviceName: String,
        val label: String,
        val vendorId: Int,
        val productId: Int,
        val supported: Boolean,
        val why: String,
    )

    /**
     * Everything plugged in, said plainly — including the boards we cannot
     * drive. A list that silently omits the modem somebody is holding is the
     * worst possible answer: they cannot tell "not plugged in" from "plugged
     * in and unsupported", and those have completely different next steps.
     */
    fun candidates(context: Context): List<Candidate> {
        val usb = context.getSystemService(Context.USB_SERVICE) as UsbManager
        return usb.deviceList.values.map { d ->
            val silabs = d.vendorId == VENDOR_SILABS
            Candidate(
                deviceName = d.deviceName,
                label = describe(d),
                vendorId = d.vendorId,
                productId = d.productId,
                supported = silabs,
                why = if (silabs) {
                    "a Silicon Labs CP210x bridge — this build speaks it"
                } else {
                    "this build drives Silicon Labs CP210x bridges; " +
                        "${hex(d.vendorId)}:${hex(d.productId)} is something else"
                },
            )
        }.sortedByDescending { it.supported }
    }

    private fun describe(d: UsbDevice): String {
        val name = d.productName ?: "USB device"
        return "$name (${hex(d.vendorId)}:${hex(d.productId)})"
    }

    private fun hex(v: Int) = String.format("%04x", v)

    /**
     * What the CORE talks to. One object, registered once, implementing the
     * Go interface: list what is plugged in, and claim one by name.
     *
     * [openBlocking] is called from a Go goroutine and may sit for a long
     * time — the first use of a device raises the system's permission dialog
     * and the person answers in their own time. That is why the core calls it
     * off its own thread and why this may block without apology.
     */
    class Host(private val context: Context) : UsbHost {

        override fun listJSON(): String {
            val items = candidates(context).joinToString(",") { c ->
                """{"name":${quote(c.deviceName)},"label":${quote(c.label)},""" +
                    """"supported":${c.supported},"why":${quote(c.why)}}"""
            }
            return "[$items]"
        }

        override fun open(name: String): UsbSerial = openBlocking(context, name)

        private fun quote(s: String): String {
            val b = StringBuilder("\"")
            for (ch in s) when (ch) {
                '\\' -> b.append("\\\\")
                '"' -> b.append("\\\"")
                '\n' -> b.append("\\n")
                '\r' -> b.append("\\r")
                '\t' -> b.append("\\t")
                else -> if (ch < ' ') b.append(String.format("\\u%04x", ch.code)) else b.append(ch)
            }
            return b.append('"').toString()
        }
    }

    /**
     * [open] with the callback flattened, for a caller that has its own thread
     * and wants the answer or the reason. Throws rather than returning null:
     * the core turns the message into the sentence a person reads.
     */
    fun openBlocking(context: Context, deviceName: String): Link {
        val latch = java.util.concurrent.CountDownLatch(1)
        var link: Link? = null
        var why: String? = null
        open(context, deviceName) { l, e ->
            link = l
            why = e
            latch.countDown()
        }
        // Generous: the dialog is a person, not a timeout. Bounded anyway,
        // because a Go goroutine parked forever on a dialog nobody ever saw
        // is a leak that only shows up as a radio that never attaches.
        if (!latch.await(3, java.util.concurrent.TimeUnit.MINUTES)) {
            throw IllegalStateException("nobody answered the permission request")
        }
        return link ?: throw IllegalStateException(why ?: "the modem would not open")
    }

    /**
     * Ask for the device, then open it. The permission dialog is the system's
     * and belongs to the person: this never pre-empts it and never retries a
     * refusal, because a refused device is an answer.
     */
    fun open(
        context: Context,
        deviceName: String,
        onResult: (Link?, String?) -> Unit,
    ) {
        val usb = context.getSystemService(Context.USB_SERVICE) as UsbManager
        val device = usb.deviceList[deviceName]
        if (device == null) {
            onResult(null, "that device is not plugged in any more")
            return
        }
        if (device.vendorId != VENDOR_SILABS) {
            onResult(null, "this build drives Silicon Labs CP210x bridges only")
            return
        }
        if (usb.hasPermission(device)) {
            finishOpen(usb, device, onResult)
            return
        }

        val filter = IntentFilter(ACTION_PERMISSION)
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(c: Context, intent: Intent) {
                if (intent.action != ACTION_PERMISSION) return
                context.unregisterReceiver(this)
                if (!intent.getBooleanExtra(UsbManager.EXTRA_PERMISSION_GRANTED, false)) {
                    onResult(null, "the modem was not allowed")
                    return
                }
                finishOpen(usb, device, onResult)
            }
        }
        if (Build.VERSION.SDK_INT >= 33) {
            context.registerReceiver(receiver, filter, Context.RECEIVER_NOT_EXPORTED)
        } else {
            @Suppress("UnspecifiedRegisterReceiverFlag")
            context.registerReceiver(receiver, filter)
        }
        // MUTABLE, and it has to be: the system fills the grant result into
        // this intent. FLAG_IMMUTABLE here means the callback arrives with no
        // answer in it, which reads as a silent refusal.
        val pi = PendingIntent.getBroadcast(
            context, 0, Intent(ACTION_PERMISSION).setPackage(context.packageName),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE,
        )
        usb.requestPermission(device, pi)
    }

    private fun finishOpen(
        usb: UsbManager,
        device: UsbDevice,
        onResult: (Link?, String?) -> Unit,
    ) {
        try {
            onResult(Link(usb, device), null)
        } catch (e: Exception) {
            Log.w(TAG, "opening ${device.deviceName}", e)
            onResult(null, e.message ?: "the modem would not open")
        }
    }

    /**
     * One claimed CP210x, presented as the three verbs the core asked for.
     *
     * [UsbSerial] is a Go interface implemented here — the same reverse
     * binding the notification sink uses. Every method may be called from a
     * Go goroutine, so nothing here touches the main thread and nothing here
     * calls back into the core.
     */
    class Link(usb: UsbManager, device: UsbDevice) : UsbSerial {

        private val connection: UsbDeviceConnection
        private val iface: UsbInterface
        private val bulkIn: UsbEndpoint
        private val bulkOut: UsbEndpoint

        @Volatile
        private var closed = false

        init {
            iface = device.getInterface(0)
            connection = usb.openDevice(device)
                ?: throw IllegalStateException("the system would not open the modem")
            if (!connection.claimInterface(iface, true)) {
                connection.close()
                throw IllegalStateException("another app is holding the modem")
            }
            var inEp: UsbEndpoint? = null
            var outEp: UsbEndpoint? = null
            for (i in 0 until iface.endpointCount) {
                val ep = iface.getEndpoint(i)
                if (ep.type != UsbConstants.USB_ENDPOINT_XFER_BULK) continue
                if (ep.direction == UsbConstants.USB_DIR_IN) inEp = ep else outEp = ep
            }
            if (inEp == null || outEp == null) {
                connection.releaseInterface(iface)
                connection.close()
                throw IllegalStateException("the modem exposes no bulk endpoints")
            }
            bulkIn = inEp
            bulkOut = outEp

            try {
                control(REQ_IFC_ENABLE, UART_ENABLE)
                // Baud rate is a four-byte payload, little endian — the older
                // divisor request (0x01) is not used by CP2102 firmware.
                val baud = byteArrayOf(
                    (BAUD and 0xFF).toByte(),
                    ((BAUD shr 8) and 0xFF).toByte(),
                    ((BAUD shr 16) and 0xFF).toByte(),
                    ((BAUD shr 24) and 0xFF).toByte(),
                )
                controlWithData(REQ_SET_BAUDRATE, 0, baud)
                control(REQ_SET_LINE_CTL, LINE_CTL_8N1)
                control(REQ_SET_MHS, MHS_DTR_RTS_ON)
            } catch (e: Exception) {
                connection.releaseInterface(iface)
                connection.close()
                throw e
            }
        }

        private fun control(request: Int, value: Int) = controlWithData(request, value, null)

        private fun controlWithData(request: Int, value: Int, data: ByteArray?) {
            val n = connection.controlTransfer(
                REQ_TYPE_HOST_TO_DEVICE, request, value, iface.id,
                data, data?.size ?: 0, CONTROL_TIMEOUT_MS,
            )
            if (n < 0) {
                throw IllegalStateException(
                    "the bridge refused setup request ${hex(request)}")
            }
        }

        // A read buffer sized to the endpoint, reused: the driver above calls
        // this in a tight loop and a fresh allocation per read is pure churn.
        private val rx = ByteArray(maxOf(bulkIn.maxPacketSize, 64))

        /**
         * Blocks briefly, and returns an EMPTY slice when the line is quiet —
         * silence on a radio link is the normal case, not a failure. Only a
         * transfer error, which is what an unplugged cable looks like, is an
         * error, and the driver treats that as the modem being gone.
         */
        override fun readChunk(): ByteArray {
            if (closed) return ByteArray(0)
            val n = connection.bulkTransfer(bulkIn, rx, rx.size, READ_TIMEOUT_MS)
            if (n < 0) {
                // A timeout and a dead device are the same return value here,
                // so this cannot throw on n < 0 alone: closed is the signal
                // that the device is really gone.
                if (closed) throw IllegalStateException("the modem was detached")
                return ByteArray(0)
            }
            return rx.copyOf(n)
        }

        override fun writeChunk(b: ByteArray) {
            if (closed) throw IllegalStateException("the modem is closed")
            var off = 0
            while (off < b.size) {
                val slice = if (off == 0 && b.size <= bulkOut.maxPacketSize) b
                else b.copyOfRange(off, minOf(b.size, off + bulkOut.maxPacketSize))
                val n = connection.bulkTransfer(bulkOut, slice, slice.size, WRITE_TIMEOUT_MS)
                if (n < 0) throw IllegalStateException("the modem stopped accepting bytes")
                off += if (n == 0) slice.size else n
            }
        }

        override fun closeLink() {
            if (closed) return
            closed = true
            try {
                // Drop DTR and RTS on the way out, so the board is not left
                // held in whatever state we put it in.
                control(REQ_SET_MHS, 0x0300)
                control(REQ_IFC_ENABLE, 0x0000)
            } catch (_: Exception) {
                // Already gone. Releasing is what matters.
            }
            runCatching { connection.releaseInterface(iface) }
            runCatching { connection.close() }
        }

        private companion object {
            // Short enough that a close is noticed promptly, long enough that
            // the loop is not a spin.
            const val READ_TIMEOUT_MS = 200
            const val WRITE_TIMEOUT_MS = 2000
        }
    }
}
