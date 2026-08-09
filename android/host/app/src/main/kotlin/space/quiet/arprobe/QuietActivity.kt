package space.quiet.arprobe

import android.Manifest
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.text.InputType
import android.util.Log
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.webkit.PermissionRequest
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import androidx.activity.ComponentActivity
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import org.json.JSONObject

/**
 * The product screen: a WebView over the interface the node already serves,
 * and the platform seams a web page cannot reach.
 *
 * WHAT IT IS NOT is the shorter list and the more important one. It does not
 * open the core, does not resolve the data directory, does not hold identity,
 * does not read the journal, renders no conversation in Kotlin, contains no
 * NotificationManager logic, and inherits nothing from AR-0's rig. Every one
 * of those belongs to something that outlives it: [RuntimeController] owns the
 * runtime, [NotificationCoordinator] owns what a person is woken for, and the
 * web UI owns the conversation. An Activity is destroyed and recreated by a
 * rotation; nothing that must survive one may live here.
 *
 * So it does five things:
 *
 * ```
 *   attach to the RuntimeController that QuietApp created
 *   show the existing JS interface in a WebView
 *   report lifecycle to the notification coordinator
 *   ask for system permissions, at a moment a person can understand
 *   accept a notification / deep-link intent and hand the target to the UI
 * ```
 *
 * THE UNLOCK IS DELIBERATELY THE MINIMUM. A passphrase field and a button,
 * because the node cannot open without one and an app that cannot make the
 * node run is not a shell — not because this is the unlock design. That is
 * DS-3's, with its own screen, its own copy and its own error states. What is
 * load-bearing here is only that the request goes to the CONTROLLER: the
 * Activity never calls the binding's Start, and never invents a data
 * directory.
 */
class QuietActivity : ComponentActivity() {

    private lateinit var root: FrameLayout
    private lateinit var web: WebView

    /**
     * The token the node minted for this session, and the bridge's whole
     * admission test. Empty until the interface is shown, which is correct:
     * before there is a session there is nobody to admit.
     */
    @Volatile
    private var sessionToken: String = ""
    private lateinit var gate: NotificationPermissionGate

    private var statusPanel: View? = null
    private var permissionPanel: View? = null

    /** Per Activity instance: the way back is offered once, never nagged. */
    private var recoveryOffered = false

    /** The URL currently shown, so a state change does not reload needlessly. */
    private var loaded: String? = null

    /**
     * A notification's target, held until the interface is up. It arrives
     * before the WebView can act on it — an intent is delivered to a cold
     * Activity long before a page exists.
     */
    private var pendingTarget: JSONObject? = null

    private val controller by lazy { RuntimeController.get(this) }

    /**
     * THE TWO THINGS A WEBVIEW CANNOT DO BY ITSELF, and both were silently
     * broken on the phone while working on a desktop browser.
     *
     * A file input's click reaches [WebChromeClient.onShowFileChooser] and
     * NOWHERE ELSE. The default implementation returns false, which means
     * "no chooser" — so `attachPick()` opened nothing at all, no error, no
     * log line: the paperclip menu simply did not respond. There is no way
     * to fix that in the page; the Activity has to open the picker.
     *
     * A getUserMedia call reaches [WebChromeClient.onPermissionRequest], and
     * the default DENIES it. The recorder's own catch then said "microphone
     * unavailable", which is true and unhelpful — the microphone was fine
     * and nobody had ever been asked for it.
     */
    private var pendingFiles: ValueCallback<Array<Uri>>? = null

    private val pickFiles =
        registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
            // ALWAYS ANSWERED, including a cancel: a callback left hanging
            // wedges that input element for the life of the page — the next
            // press of the same button does nothing, forever.
            val cb = pendingFiles
            pendingFiles = null
            cb?.onReceiveValue(
                WebChromeClient.FileChooserParams.parseResult(result.resultCode, result.data),
            )
        }

    private var pendingMic: PermissionRequest? = null

    private val requestMic =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            val req = pendingMic
            pendingMic = null
            if (req == null) return@registerForActivityResult
            if (granted) {
                req.grant(arrayOf(PermissionRequest.RESOURCE_AUDIO_CAPTURE))
            } else {
                req.deny()
            }
        }

    private val requestNotifications =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { _ ->
            // The RESULT is not read: the gate re-reads the system's own
            // answer, which is authoritative and also covers the case this
            // callback cannot see — notifications switched off for the app
            // while the permission itself was granted.
            applyPermissionState()
        }

    private val runtimeListener = RuntimeController.Listener { state ->
        runOnUiThread { render(state) }
    }

    override fun onCreate(saved: Bundle?) {
        super.onCreate(saved)
        gate = NotificationPermissionGate(this)

        root = FrameLayout(this)
        web = WebView(this).apply {
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true      // the UI keeps preferences there
            // Hardened rather than defaulted: the page is served by our own
            // node over loopback and has no business reading the filesystem or
            // another app's content provider.
            settings.allowFileAccess = false
            settings.allowContentAccess = false
            webChromeClient = object : WebChromeClient() {
                override fun onShowFileChooser(
                    view: WebView?,
                    callback: ValueCallback<Array<Uri>>?,
                    params: FileChooserParams?,
                ): Boolean {
                    // A second request supersedes the first, and the first is
                    // told so rather than dropped.
                    pendingFiles?.onReceiveValue(null)
                    pendingFiles = callback
                    val intent = params?.createIntent()
                    if (intent == null) {
                        pendingFiles = null
                        return false
                    }
                    return try {
                        pickFiles.launch(intent)
                        true
                    } catch (e: ActivityNotFoundException) {
                        // A phone with no picker for this MIME type. Say no
                        // properly so the input can be used again.
                        Log.w(TAG, "no activity can pick ${params.acceptTypes?.joinToString()}", e)
                        pendingFiles = null
                        callback?.onReceiveValue(null)
                        false
                    }
                }

                override fun onPermissionRequest(request: PermissionRequest) {
                    runOnUiThread {
                        // ONLY THE MICROPHONE. The page has no reason to ask
                        // for a camera or the screen, and a blanket grant is
                        // how a WebView becomes a hole: anything else is
                        // denied without being forwarded to the person.
                        if (!request.resources.contains(PermissionRequest.RESOURCE_AUDIO_CAPTURE)) {
                            request.deny()
                            return@runOnUiThread
                        }
                        val have = ContextCompat.checkSelfPermission(
                            this@QuietActivity, Manifest.permission.RECORD_AUDIO,
                        ) == PackageManager.PERMISSION_GRANTED
                        if (have) {
                            request.grant(arrayOf(PermissionRequest.RESOURCE_AUDIO_CAPTURE))
                        } else {
                            pendingMic?.deny()
                            pendingMic = request
                            requestMic.launch(Manifest.permission.RECORD_AUDIO)
                        }
                    }
                }
            }
            webViewClient = LocalOnlyClient()
            // AR-1b.8 — the interface's way back. Registered once, on the one
            // WebView this app has, and every method on it demands the node's
            // own session token: see HostBridge for why the origin lock is not
            // considered enough on its own.
            addJavascriptInterface(
                HostBridge(
                    token = { sessionToken },
                    coordinator = controller.notifications,
                    policy = { controller.setNotificationPolicy(it) },
                    // STARTED FROM HERE, not from the service or the core:
                    // this Activity is the visible thing a person pressed,
                    // and that is exactly what Android 12+ requires.
                    stayConnected = { on ->
                        controller.setAvailabilityRequested(on)
                        if (on) {
                            AvailabilityService.start(this@QuietActivity)
                        } else {
                            AvailabilityService.stop(this@QuietActivity)
                        }
                    },
                ),
                "QuietHost",
            )
        }
        root.addView(
            web,
            FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT,
            ),
        )
        setContentView(root)

        // EDGE-TO-EDGE IS NOT OPTIONAL ON ANDROID 15 for an app targeting 35:
        // the system draws the window under the status bar and the navigation
        // bar whether it was asked to or not. Uncorrected, the interface's own
        // header renders underneath the clock and the battery — seen on the
        // Nothing Phone (1) the first time this screen ran there.
        //
        // The inset is applied HERE, as padding, rather than left to the web
        // UI. A WebView reports `env(safe-area-inset-*)` only with
        // viewport-fit=cover and a display-cutout mode that permits it, and
        // there is no env() anywhere in the stylesheet today — RS-0c is that
        // work. Until then the platform seam is closed where platform seams
        // belong: in the host, not in the page.
        ViewCompat.setOnApplyWindowInsetsListener(root) { view, insets ->
            val bars = insets.getInsets(
                WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout()
            )
            view.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            insets
        }

        controller.addListener(runtimeListener)   // also renders the current state
        takeTarget(intent)
    }

    override fun onDestroy() {
        controller.removeListener(runtimeListener)
        super.onDestroy()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        takeTarget(intent)
        deliverTarget()
    }

    override fun onResume() {
        super.onResume()
        // AR-1b.7's first half. WHICH space is on screen is a separate fact
        // the web UI has to report, and that seam lands with b.7 — until it
        // does, a message for the conversation being read still notifies,
        // which is the safe direction to be wrong in.
        controller.notifications.onForeground(true)
        applyPermissionState()
        // AR-1c — THE MODE COMES BACK HERE, AND ONLY HERE.
        //
        // A foreground service does not survive a process death, and nothing
        // may start one from the background: Android 12+ forbids it, and the
        // rule is the right shape. So the mode's INTENTION is durable and its
        // SERVICE is not, and this screen — visible, in front of the person
        // who asked for it — is the one place allowed to reconcile the two.
        //
        // In onResume rather than beside the WebView load: coming back to an
        // Activity that was never destroyed is the ordinary way somebody
        // returns, and it does not reload anything.
        //
        // What this deliberately does NOT do is survive a reboot on its own.
        // That needs its own decision, and a mode that quietly reappeared
        // after a restart would be a battery cost nobody re-consented to.
        if (controller.availabilityRequested() && controller.availabilityLeaseCount() == 0) {
            AvailabilityService.start(this)
        }
    }

    override fun onPause() {
        controller.notifications.onForeground(false)
        super.onPause()
    }

    @Deprecated("Back is the universal exit on a phone; the WebView owns history first.")
    override fun onBackPressed() {
        if (web.canGoBack()) web.goBack() else @Suppress("DEPRECATION") super.onBackPressed()
    }

    // ------------------------------------------------------------- rendering

    private fun render(state: String) {
        when (state) {
            RuntimeController.STATE_ALIVE -> showInterface()
            RuntimeController.STATE_OPENING -> showStatus("Opening…", unlockable = false)
            else -> showStatus("Quiet is not running on this device yet.", unlockable = true)
        }
    }

    /**
     * The interface is the node's own, reached over the ordinary local HTTP
     * API — ADR-011's boundary and the same seam every other client uses. The
     * port and the session token come from the controller's snapshot; nothing
     * here is hardcoded, and no listener is exposed beyond loopback.
     */
    private fun showInterface() {
        val core = controller.snapshot().optJSONObject("core") ?: return
        val port = core.optInt("api_port", 0)
        val token = core.optString("session_token", "")
        if (port == 0 || token.isEmpty()) return

        sessionToken = token
        val url = "http://127.0.0.1:$port/?token=$token"
        statusPanel?.let { root.removeView(it); statusPanel = null }
        web.visibility = View.VISIBLE
        if (loaded != url) {
            loaded = url
            web.loadUrl(url)
        }
        deliverTarget()
        // The interface has only now appeared, and the permission panel sits
        // on top of it — so it is offered HERE as well as on resume. Asking
        // only in onResume meant a person who unlocked the node was never
        // shown the offer until they left the app and came back, which is
        // exactly the moment it stops being about notifications.
        applyPermissionState()
    }

    private fun showStatus(text: String, unlockable: Boolean) {
        web.visibility = View.GONE
        statusPanel?.let { root.removeView(it) }

        val panel = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER
            setPadding(64, 64, 64, 64)
            addView(TextView(this@QuietActivity).apply {
                setText(text)
                gravity = Gravity.CENTER
            })
        }
        if (unlockable) {
            val field = EditText(this).apply {
                setHint("Passphrase")
                inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
            }
            panel.addView(field)
            panel.addView(Button(this).apply {
                setText("Open")
                setOnClickListener {
                    // The CONTROLLER opens the node. This Activity never calls
                    // the binding's Start and never resolves a data directory
                    // — one owner, one directory, one lock.
                    controller.ensureStarted(field.text.toString(), null, false)
                }
            })
        }
        root.addView(panel)
        statusPanel = panel
    }

    // ----------------------------------------------------------- permissions

    /**
     * The permission is NOT requested from onCreate. A system dialog with no
     * context is a dialog a person refuses, and on Android 13+ a refusal is
     * final as far as our own dialog is concerned — the second request is
     * silently ignored. So the app asks for itself first, in a sentence that
     * says what it is for, and only a deliberate tap reaches the system.
     */
    private fun applyPermissionState() {
        val state = gate.state()

        // A refusal reaches all the way down: the coordinator stops
        // presenting AND the core stops producing candidates. Producing them
        // for a host that may not render them would burn a bounded queue and
        // count drops nobody can act on.
        controller.setNotificationsEnabled(state.canPresent)

        permissionPanel?.let { root.removeView(it); permissionPanel = null }
        if (web.visibility != View.VISIBLE) return

        val panel = when {
            state.canAsk -> askPanel()

            // THE ROUTE BACK. Our dialog cannot return — on 33+ a second
            // request after a refusal is silently ignored — so a person who
            // said no, or who switched notifications off in system settings,
            // would otherwise have no way to change their mind from inside
            // the app. Offered ONCE per launch and dismissible: an offer that
            // reappears on every resume is nagging, and nagging is how a
            // person turns something off for good.
            state.needsSystemSettings && !recoveryOffered -> {
                recoveryOffered = true
                recoveryPanel()
            }

            else -> return
        }
        root.addView(
            panel,
            FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
                Gravity.BOTTOM,
            ),
        )
        permissionPanel = panel
    }

    /** The product's own question, asked before the system's. */
    private fun askPanel(): View = strip(
        "Quiet can tell you about new signals while the app is closed.",
        primary = "Allow notifications" to {
            // Recorded BEFORE the dialog returns: a person may dismiss it
            // without answering, and that still spends the one request the
            // platform will show.
            gate.markAsked()
            if (Build.VERSION.SDK_INT >= 33) {
                requestNotifications.launch(Manifest.permission.POST_NOTIFICATIONS)
            } else {
                applyPermissionState()
            }
        },
        // NOT marked as asked: this is our question, not the system's, and
        // declining it must not spend the one system request or move the
        // state to DENIED.
        secondary = "Not now" to { dismissPermissionPanel() },
    )

    /** The only honest recovery once the system will not ask again. */
    private fun recoveryPanel(): View = strip(
        "Notifications are off for Quiet. Android only lets you turn them back on in settings.",
        primary = "Open settings" to { startActivity(gate.systemSettingsIntent()) },
        secondary = "Not now" to { dismissPermissionPanel() },
    )

    private fun dismissPermissionPanel() {
        permissionPanel?.let { root.removeView(it) }
        permissionPanel = null
    }

    private fun strip(
        message: String,
        primary: Pair<String, () -> Unit>,
        secondary: Pair<String, () -> Unit>,
    ): View = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        setBackgroundColor(Color.argb(230, 20, 20, 22))
        setPadding(48, 48, 48, 48)
        addView(TextView(this@QuietActivity).apply {
            setText(message)
            setTextColor(Color.WHITE)
        })
        addView(LinearLayout(this@QuietActivity).apply {
            orientation = LinearLayout.HORIZONTAL
            addView(Button(this@QuietActivity).apply {
                setText(primary.first)
                setOnClickListener { primary.second() }
            })
            addView(Button(this@QuietActivity).apply {
                setText(secondary.first)
                setOnClickListener { secondary.second() }
            })
        })
    }

    // ------------------------------------------------------------ deep links

    private fun takeTarget(intent: Intent?) {
        val space = intent?.getStringExtra(EXTRA_SPACE) ?: return
        pendingTarget = JSONObject()
            .put("space_id", space)
            .put("message_id", intent.getStringExtra(EXTRA_MESSAGE) ?: "")
            .put("event_id", intent.getStringExtra(EXTRA_EVENT) ?: "")
    }

    /**
     * Hands the target to the interface. The JS side of this — opening the
     * exact message rather than the space — is AR-1b.5, together with the
     * PendingIntent that will carry it; today nothing posts a notification, so
     * nothing produces such an intent. The seam exists here so the tap path
     * never has to be routed through the debug rig and moved later.
     */
    private fun deliverTarget() {
        val target = pendingTarget ?: return
        if (web.visibility != View.VISIBLE) return
        pendingTarget = null
        web.evaluateJavascript(
            "window.quietOpen && window.quietOpen($target);",
        ) { Log.i(TAG, "target handed to the interface") }
    }

    /**
     * The WebView shows one origin: our own node on loopback. Anything else a
     * page links to goes to the OS, where a person can see where they are
     * going — a WebView that follows arbitrary links is a browser without an
     * address bar.
     */
    private inner class LocalOnlyClient : WebViewClient() {
        override fun shouldOverrideUrlLoading(
            view: WebView,
            request: WebResourceRequest,
        ): Boolean {
            val host = request.url.host
            if (host == "127.0.0.1" || host == "localhost") return false
            startActivity(Intent(Intent.ACTION_VIEW, request.url as Uri))
            return true
        }
    }

    companion object {
        private const val TAG = "quiet-activity"

        const val EXTRA_SPACE = "space_id"
        const val EXTRA_MESSAGE = "target_message_id"
        const val EXTRA_EVENT = "event_id"
    }
}
