package space.quiet.arprobe

import android.Manifest
import android.app.AlertDialog
import android.app.DownloadManager
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Environment
import android.text.Editable
import android.text.InputType
import android.text.TextWatcher
import android.util.Log
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.webkit.PermissionRequest
import android.webkit.URLUtil
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
import android.widget.Toast
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

    /** Where a remembered passphrase lives, and the only thing that reads it. */
    private val vault by lazy { PassphraseVault(applicationContext) }

    /** The four digits, wrapped in hardware. See PasscodeVault. */
    private val passcode by lazy { PasscodeVault(applicationContext) }

    /** A face or a fingerprint, when somebody asked for one. See BiometricVault. */
    private val bio by lazy { BiometricVault(applicationContext) }

    /** Offered once per instance, like the code, and for the same reason. */
    private var bioOffered = false

    /**
     * Per Activity instance, like [recoveryOffered]: a code is offered once
     * after an unlock and never nagged. Somebody who does not want one has
     * said so by walking past it.
     */
    private var passcodeOffered = false

    /** Digits typed into the keypad so far, for the dots. */
    private var codeEntry = ""

    /**
     * The passphrase currently being tried, held until the core says whether
     * it worked. Nothing is written down before that: a stored typo would
     * become an auto-open that fails on every launch and cannot be corrected
     * from a screen that no longer appears.
     */
    @Volatile
    private var pendingPassphrase: String? = null

    /**
     * Set when the attempt in flight came from the vault rather than from
     * somebody typing. Its failure means the remembered value is no longer
     * good — the keystore was replaced, or the data directory was — and the
     * only way out of that loop is to forget it and ask.
     */
    @Volatile
    private var autoAttempt: String? = null

    /** What the last failed attempt was refused for, shown on the panel. */
    private var unlockMessage: String? = null

    /** What is in the field, so rebuilding the panel does not empty it. */
    private var lastTyped: String = ""

    /** The first-run name field, preserved across panel rebuilds like [lastTyped]. */
    private var lastName: String = ""

    /**
     * The code chosen during onboarding, held until the core proves the
     * generated passphrase by opening with it. Bound only at STATE_ALIVE —
     * the same law as [pendingPassphrase]: nothing unverified is ever sealed.
     */
    @Volatile
    private var pendingBindCode: String? = null

    /**
     * The passphrase this instance opened with, kept in memory only. It backs
     * the two native dialogs Settings can raise (reveal / change code); it is
     * never written anywhere new and never crosses the WebView bridge — the
     * bridge's own doctrine: the passphrase has no getter anywhere.
     */
    @Volatile
    private var openedPassphrase: String? = null

    /**
     * The person walked through the "use a long passphrase instead" door.
     * Sticky for this instance so a refused attempt re-renders the form they
     * were actually filling in, not the short one.
     */
    private var longFormPreferred = false

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
            // parseResult reads only intent.data — ONE uri — so a
            // multi-select answer (which arrives as clipData) would collapse
            // to nothing. Read both shapes ourselves.
            val data = result.data
            val clip = data?.clipData
            val uris = when {
                result.resultCode != RESULT_OK -> null
                clip != null && clip.itemCount > 0 ->
                    Array(clip.itemCount) { clip.getItemAt(it).uri }
                data?.data != null -> arrayOf(data.data!!)
                else -> null
            }
            cb?.onReceiveValue(uris)
        }

    // SR-0: the PTT mic request — a PLAIN permission ask, unlike the
    // WebView-coupled one below. The result is not read: the person presses
    // the button again and the check re-runs against the system's answer.
    private val requestPttMic =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { _ -> }

    private var pendingMic: PermissionRequest? = null

    // Geolocation prompt handoff (SP-3): the WebView's callback is retained
    // while the native ask is up, superseding any earlier one — the same
    // discipline pendingMic follows. Without this override the callback
    // never fires and navigator.geolocation hangs silently forever.
    private var pendingGeoOrigin: String? = null
    private var pendingGeoCallback: android.webkit.GeolocationPermissions.Callback? = null

    private val requestLocation =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            val origin = pendingGeoOrigin
            val cb = pendingGeoCallback
            pendingGeoOrigin = null
            pendingGeoCallback = null
            if (origin != null && cb != null) {
                // retain=false: the grant is per-prompt; the page asks again
                // next session and the system answer is re-consulted.
                cb.invoke(origin, granted, false)
            }
        }

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
            // A VIDEO STARTS BECAUSE SOMEBODY PRESSED PLAY, and Android
            // cannot see that it did. The tap lands on our button, the
            // player frame is mounted, and the embed inside it begins a
            // moment later — by then the gesture has expired, and the
            // default here would leave a black rectangle with no error.
            //
            // Loosened for the one page this WebView is allowed to load:
            // its own interface, over loopback, whose player mounts only
            // after a press and asks before the first one.
            settings.mediaPlaybackRequiresUserGesture = false
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
                    // NOT createIntent(): that helper types the intent */*
                    // and drops the page's accept list on most WebViews —
                    // measured as "choosing a photo offers every file on the
                    // phone" — and it never asks for multi-select even when
                    // the input said multiple. The page's own accept types
                    // and mode are right here; honor them.
                    val accepts = params?.acceptTypes
                        ?.filter { it.isNotBlank() && it.contains('/') }
                        ?: emptyList()
                    val intent = Intent(Intent.ACTION_GET_CONTENT).apply {
                        addCategory(Intent.CATEGORY_OPENABLE)
                        type = if (accepts.size == 1) accepts[0] else "*/*"
                        if (accepts.isNotEmpty()) {
                            putExtra(Intent.EXTRA_MIME_TYPES, accepts.toTypedArray())
                        }
                        if (params?.mode == WebChromeClient.FileChooserParams.MODE_OPEN_MULTIPLE) {
                            putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true)
                        }
                    }
                    return try {
                        pickFiles.launch(intent)
                        true
                    } catch (e: ActivityNotFoundException) {
                        // A phone with no picker for this MIME type. Say no
                        // properly so the input can be used again.
                        Log.w(TAG, "no activity can pick ${accepts.joinToString()}", e)
                        pendingFiles = null
                        callback?.onReceiveValue(null)
                        false
                    }
                }

                override fun onGeolocationPermissionsShowPrompt(
                    origin: String,
                    callback: android.webkit.GeolocationPermissions.Callback,
                ) {
                    runOnUiThread {
                        // Only our own page: the WebView is locked to the
                        // loopback origin, and anything else is denied
                        // without being forwarded to the person.
                        if (!origin.startsWith("http://127.0.0.1")) {
                            callback.invoke(origin, false, false)
                            return@runOnUiThread
                        }
                        val have = ContextCompat.checkSelfPermission(
                            this@QuietActivity, Manifest.permission.ACCESS_FINE_LOCATION,
                        ) == PackageManager.PERMISSION_GRANTED
                        if (have) {
                            callback.invoke(origin, true, false)
                        } else {
                            pendingGeoCallback?.invoke(pendingGeoOrigin ?: origin, false, false)
                            pendingGeoOrigin = origin
                            pendingGeoCallback = callback
                            requestLocation.launch(Manifest.permission.ACCESS_FINE_LOCATION)
                        }
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
            // SAVING A FILE IS A THING A WEBVIEW CANNOT DO BY ITSELF, and its
            // way of saying so is to do nothing at all: an <a download> tap
            // reaches here, and with no listener registered the default is to
            // ignore it. No error, no log line, a button that is simply inert
            // — the same shape as the file chooser and the microphone before
            // them.
            //
            // ONLY OUR OWN NODE'S URLS ARE ACCEPTED. The download goes out
            // through Android's DownloadManager, which fetches the URL in a
            // different process, so this must never become a way for a page
            // to have the system fetch something else — the WebView is locked
            // to loopback, and so is this.
            setDownloadListener { url, _, contentDisposition, mimeType, _ ->
                saveDownload(url, contentDisposition, mimeType)
            }
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
                    // BOTH COPIES, OR THE SETTING LIES. The row that offers to
                    // undo a remembered passphrase must appear when the
                    // remembered one is sealed behind a face as well — it is
                    // still a passphrase this device opens with — and
                    // forgetting has to take both, or "Quiet will ask for it
                    // the next time it opens" is not what happens.
                    unlockRemembered = { vault.has() || bio.has() },
                    forgetPassphrase = { vault.forget(); bio.forget() },
                    // BOTH ANSWER WITH A DIALOG ON THE PHONE'S OWN SCREEN,
                    // never with a value into the page — the bridge's doctrine
                    // stands: the passphrase has no getter anywhere.
                    revealPassphrase = {
                        val pass = openedPassphrase
                        if (pass.isNullOrEmpty()) {
                            false
                        } else {
                            runOnUiThread { showRecoveryDialog(pass) }
                            true
                        }
                    },
                    changeCode = {
                        val pass = openedPassphrase
                        if (pass.isNullOrEmpty()) {
                            false
                        } else {
                            runOnUiThread {
                                unlockMessage = null
                                chooseCode(pass)
                            }
                            true
                        }
                    },
                    stayRefused = { controller.availabilityRefused() },
                    // EN-3 — the doorbell. Status distinguishes "nothing on
                    // this phone can carry it" from "off", so the settings
                    // row can say the true sentence instead of a dead switch.
                    doorbellStatus = {
                        if (!UnifiedPushConnector.hasDistributor(this@QuietActivity)) {
                            "no_distributor"
                        } else {
                            UnifiedPushConnector.status(this@QuietActivity)
                        }
                    },
                    doorbellOn = { UnifiedPushConnector.register(this@QuietActivity) },
                    doorbellOff = { UnifiedPushConnector.unregister(this@QuietActivity) },
                    // SR-0 — Radio Mode and push-to-talk. The store write is
                    // the host's; the page gets the authoritative state
                    // pushed back so a stale localStorage echo self-corrects.
                    radioMode = { space, enabled, autoplay ->
                        controller.setRadioMode(space, enabled, autoplay)
                        runOnUiThread { pushRadioEcho(space) }
                        true
                    },
                    radioBackground = { on ->
                        controller.radio.setBackgroundListen(on)
                        true
                    },
                    pttPress = { space ->
                        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) !=
                            PackageManager.PERMISSION_GRANTED
                        ) {
                            // Asked at the moment of first use, never at app
                            // start (groom §45). The press is refused; the
                            // person answers the dialog and presses again.
                            runOnUiThread { requestPttMic.launch(Manifest.permission.RECORD_AUDIO) }
                            false
                        } else {
                            controller.ptt.press(space)
                        }
                    },
                    pttRelease = { controller.ptt.release() },
                    pttCancel = { controller.ptt.cancel() },
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

        // THE LAUNCH IS DECIDED BEFORE ANY SCREEN IS DRAWN. Asked for BEFORE
        // addListener so the first render is "Opening…" and not a blank field
        // that vanishes half a second later — a flash of the unlock panel on
        // every launch is the same complaint in a shorter form.
        //
        // WHICH door wins is UnlockRoute's business, along with the reasons,
        // one of which cost a real handset to learn. Three things now want to
        // own this moment; the one place they are ranked is a file with tests
        // in front of it.
        when (UnlockRoute.of(bio.has(), passcode.has(), vault.has())) {
            UnlockRoute.BIOMETRIC ->
                // NOTHING IS NEEDED ON REFUSAL. The prompt is raised over the
                // panel the listener draws a moment later, and that panel is
                // already the next door down — the keypad when a code is
                // bound, the passphrase field when nothing is. The order the
                // Activity falls through in and the order UnlockRoute.refused
                // returns are the same order, and that is not a coincidence
                // worth relying on quietly: see the test that pins it.
                bio.unlock(this, onOpen = { stored ->
                    autoAttempt = stored
                    controller.ensureStarted(stored, null, true)
                }, onRefused = { })

            UnlockRoute.REMEMBERED ->
                vault.load()?.let { stored ->
                    autoAttempt = stored
                    controller.ensureStarted(stored, null, true)
                }

            // Both draw a panel rather than opening anything, and render()
            // already knows which — asking here would be a second opinion.
            UnlockRoute.CODE, UnlockRoute.ASK -> Unit
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
        controller.setPttSink(pttSink)
        controller.setSpeakingSink { space, on ->
            runOnUiThread {
                if (web.visibility == View.VISIBLE) {
                    web.evaluateJavascript(
                        "window.quietSpeaking && window.quietSpeaking(" +
                            JSONObject.quote(space) + ", $on);",
                        null,
                    )
                }
            }
        }
        // AR-1b.7's first half. WHICH space is on screen is a separate fact
        // the web UI has to report, and that seam lands with b.7 — until it
        // does, a message for the conversation being read still notifies,
        // which is the safe direction to be wrong in.
        controller.notifications.onForeground(true)
        controller.setForeground(true)
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
        // A boot receiver is its own decision; coming back when somebody opens
        // the app is enough, and it keeps the mode tied to a person's presence.
        applyAvailabilityMode()
    }

    /**
     * Start the mode if it is wanted and not already running.
     *
     * GATED ON THE CORE BEING OPEN, which matters now that the mode is on by
     * default: onResume runs while the unlock panel is still up on a fresh
     * install, and a permanent notification announcing that the app is staying
     * connected — over a node that has not been opened and cannot carry
     * anything — would be a claim about nothing. So it is reconciled here AND
     * from [render] the moment the runtime comes alive.
     */
    private fun applyAvailabilityMode() {
        if (!controller.isAlive()) return
        if (controller.availabilityRequested() && controller.availabilityLeaseCount() == 0) {
            AvailabilityService.start(this)
        }
    }

    override fun onPause() {
        // A recording must not outlive the surface that started it (SR-0):
        // the page is about to stop receiving pushes, so the utterance dies
        // here rather than finishing into a void.
        controller.ptt.cancel()
        controller.setPttSink(null)
        controller.setSpeakingSink(null)
        controller.notifications.onForeground(false)
        controller.setForeground(false)
        super.onPause()
    }

    @Deprecated("Back is the universal exit on a phone; the WebView owns history first.")
    override fun onBackPressed() {
        if (web.canGoBack()) web.goBack() else @Suppress("DEPRECATION") super.onBackPressed()
    }

    // ------------------------------------------------------------- rendering

    private fun render(state: String) {
        when (state) {
            RuntimeController.STATE_ALIVE -> {
                // IT WORKED, SO NOW IT MAY BE WRITTEN DOWN. This is the only
                // place that stores one, and it is reached only by a value the
                // core has already opened with.
                // THE ONE MOMENT A CODE CAN BE BOUND: we are holding a
                // passphrase the core has just opened with, so nothing
                // unverified can ever be sealed. Offered once per instance
                // and never nagged — the politeness recoveryOffered keeps.
                var offering = false
                pendingPassphrase?.let { pass ->
                    pendingPassphrase = null
                    autoAttempt = null
                    openedPassphrase = pass
                    // NOT WHILE A GUARDED COPY EXISTS. Writing the plain one
                    // back on every successful open would quietly reopen the
                    // hole offerFace closed — the app would auto-open before
                    // the sensor was ever asked.
                    if (!vault.has() && !bio.has()) vault.save(pass)
                    val chosen = pendingBindCode
                    if (chosen != null) {
                        // The onboarding already asked for the code; binding it
                        // here, against a passphrase the core has JUST opened
                        // with, is the whole point of the delay. No offer needed
                        // — the person chose these digits minutes ago.
                        pendingBindCode = null
                        passcodeOffered = true
                        passcode.bind(chosen, pass)
                        offering = true
                        unlockMessage = null
                        offerFace(pass)
                    } else if (!passcode.has() && !passcodeOffered) {
                        passcodeOffered = true
                        offering = true
                        unlockMessage = null
                        chooseCode(pass)
                    } else if (!bioOffered && !bio.has() &&
                        bio.available() == BiometricVault.Availability.READY
                    ) {
                        // Somebody who already has a code still gets asked
                        // once — otherwise the feature would only ever reach
                        // people installing for the first time.
                        offering = true
                        unlockMessage = null
                        offerFace(pass)
                    }
                }
                if (offering) return
                unlockMessage = null
                showInterface()
                // The core is open, so "staying connected" is now a claim
                // about something. Starting a foreground service is only
                // allowed from a visible Activity, and this is one.
                applyAvailabilityMode()
            }
            RuntimeController.STATE_OPENING -> showStatus("Opening…", unlockable = false)
            else -> {
                // An attempt that ended here was refused. WHY is in the core's
                // status, and throwing it away is what made this screen so
                // hard to get past: a person typed a word, pressed Open, and
                // got back the same blank field with no sentence anywhere.
                pendingBindCode = null
                pendingPassphrase?.let {
                    pendingPassphrase = null
                    unlockMessage = unlockReason(controller.lastError())
                    if (autoAttempt != null) {
                        // The remembered value no longer opens this data
                        // directory. Keeping it would retry the same failure
                        // on every launch, in front of a person who has no
                        // screen from which to change it.
                        autoAttempt = null
                        vault.forget()
                        unlockMessage = "The saved passphrase no longer opens this device's data. " +
                            "Enter it again."
                    }
                }
                showStatus("Quiet is not running on this device yet.", unlockable = true)
            }
        }
    }

    /**
     * The core's error, as a sentence for somebody holding a phone.
     *
     * The rule that sent the beta's testers in circles is the first case: the
     * keystore has required eight bytes since it was written, and nothing on
     * this screen has ever said so.
     */
    private fun unlockReason(err: String?): String = when {
        err == null || err.isBlank() -> "That did not open. Check the passphrase and try again."
        err.contains("at least 8") -> "Too short — a passphrase needs at least 8 characters."
        err.contains("wrong passphrase") -> "That passphrase does not open this device's data."
        err.contains("already running") -> "Quiet is already open. Give it a moment."
        err.contains("locked by another") || err.contains("data-dir") ->
            "Another copy of Quiet is using this data. Close it and try again."
        else -> err
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

    /**
     * The keypad. One builder, two jobs: ENTERING a bound code, and CHOOSING
     * one right after an unlock. They differ only in what four digits mean at
     * the end, so they share a screen rather than drifting into two.
     *
     * Programmatic, like everything else on this Activity — the host has no
     * layout XML, and a keypad is a grid of twelve identical things.
     */
    private fun showKeypad(
        title: String,
        note: String,
        digits: Int,
        onComplete: (String) -> Unit,
        onEscape: Pair<String, () -> Unit>?,
    ) {
        web.visibility = View.GONE
        statusPanel?.let { root.removeView(it) }
        codeEntry = ""

        val panel = SpaceLook.card(this)
        panel.addView(SpaceLook.title(this, title))
        val dots = SpaceLook.dots(this)
        panel.addView(dots)
        val noteView = SpaceLook.note(this, note)
        panel.addView(noteView)

        fun paint() {
            // Filled and empty circles rather than a password field: the
            // count is the only thing worth showing, and a keyboard would
            // put the OS's own suggestions over a secret.
            dots.setText("●".repeat(codeEntry.length) + "○".repeat(digits - codeEntry.length))
        }
        paint()

        fun push(k: String, from: View) {
            when (k) {
                "<" -> {
                    if (codeEntry.isNotEmpty()) codeEntry = codeEntry.dropLast(1)
                    Haptics.erase(from)
                }
                "C" -> {
                    codeEntry = ""
                    Haptics.erase(from)
                }
                else -> if (codeEntry.length < digits) {
                    codeEntry += k
                    Haptics.tap(from)
                }
            }
            paint()
            if (codeEntry.length == digits) {
                val code = codeEntry
                codeEntry = ""
                paint()
                onComplete(code)
            }
        }

        for (row in listOf(listOf("1", "2", "3"), listOf("4", "5", "6"),
                listOf("7", "8", "9"), listOf("C", "0", "<"))) {
            panel.addView(LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL
                layoutParams = LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
                row.forEach { k ->
                    addView(SpaceLook.key(this@QuietActivity, k).apply {
                        setOnClickListener { push(k, it) }
                    }, SpaceLook.keyRowParams(this@QuietActivity))
                }
            })
        }

        onEscape?.let { (label, action) ->
            panel.addView(SpaceLook.ghost(this, label).apply {
                setOnClickListener { action() }
            })
        }

        val screen = SpaceLook.screen(this, panel)
        root.addView(screen)
        statusPanel = screen
    }

    private fun showStatus(text: String, unlockable: Boolean) {
        // A BOUND CODE REPLACES THE PASSPHRASE FIELD. Not adds to it: two
        // ways in on one screen is a person deciding which secret is being
        // asked for, every time. The passphrase is one button away, which is
        // where somebody who has forgotten their code will look for it.
        if (unlockable && passcode.has()) {
            val left = passcode.attemptsLeft()
            showKeypad(
                title = "quite.space",
                note = unlockMessage ?: if (left <= 3) {
                    "$left of 10 tries left"
                } else {
                    "Four digits keep out somebody who picked up this phone."
                },
                digits = passcode.digits(),
                onComplete = { code -> tryPasscode(code) },
                onEscape = "Use the passphrase instead" to {
                    // One press, and the code is out of the way for this
                    // attempt only — nothing is forgotten by asking.
                    unlockMessage = null
                    showPassphraseStatus("Quiet is not running on this device yet.", unlockable = true)
                },
            )
            return
        }
        showPassphraseStatus(text, unlockable)
    }

    /**
     * Choosing a code, which is TYPED TWICE.
     *
     * A mistyped code loses no data — the passphrase is still remembered and
     * still opens everything — but it loses the easy way in, and the moment
     * that gets discovered is the moment somebody wanted to be quick. The
     * confirmation costs four taps once; being wrong costs a passphrase at
     * the worst time.
     *
     * The two screens are the same keypad with different words, and a
     * mismatch goes back to the first one rather than trying to be clever
     * about which of the two was the typo.
     */
    private fun chooseCode(pass: String) {
        val escape = "Not now" to { offerFace(pass) }
        showKeypad(
            title = "Choose a code",
            note = unlockMessage ?: "Four digits instead of your passphrase next time. " +
                "The passphrase still works, and still opens your backup.",
            digits = 4,
            onComplete = { first ->
                unlockMessage = null
                showKeypad(
                    title = "Type it again",
                    note = "So a slip now does not become a locked-out phone later.",
                    digits = 4,
                    onComplete = { second ->
                        if (second == first) {
                            statusPanel?.let { Haptics.accept(it) }
                            passcode.bind(first, pass)
                            offerFace(pass)
                        } else {
                            statusPanel?.let { Haptics.refuse(it) }
                            unlockMessage = "Those were not the same — start again."
                            chooseCode(pass)
                        }
                    },
                    onEscape = escape,
                )
            },
            onEscape = escape,
        )
    }

    /**
     * The one offer of a face, and then never again this instance.
     *
     * IT IS OFFERED AFTER THE CODE, not instead of it. They answer different
     * questions — a code is what somebody types when the sensor will not
     * look at them, and a phone with only a face and no other door is a
     * phone that can lock its owner out over a scratched finger.
     *
     * TURNING IT ON FORGETS THE REMEMBERED PASSPHRASE, and that line is the
     * whole feature. [PassphraseVault] opens with no question at all; leaving
     * it in place behind a biometric would mean the app was already open
     * before the prompt could matter — the same way an auto-open once made a
     * bound code pure decoration, measured on a real handset. A gate with a
     * hole beside it is not a gate.
     */
    private fun offerFace(pass: String) {
        val done = {
            showInterface()
            applyAvailabilityMode()
        }
        if (bioOffered || bio.has() || bio.available() != BiometricVault.Availability.READY) {
            done(); return
        }
        bioOffered = true
        AlertDialog.Builder(this)
            .setTitle("Open with your face?")
            .setMessage(
                "Your passphrase moves behind the sensor: nothing opens this app " +
                    "until the phone has just seen you. Your code still works, and " +
                    "so does your passphrase."
            )
            .setPositiveButton("Turn it on") { _, _ ->
                bio.enroll(this, pass) { ok ->
                    if (ok) {
                        // The plain copy goes now that a guarded one exists,
                        // and only now: forgetting first would leave a phone
                        // with no stored passphrase at all if the prompt were
                        // cancelled halfway.
                        vault.forget()
                    } else {
                        Toast.makeText(this, "That did not turn on.", Toast.LENGTH_SHORT).show()
                    }
                    done()
                }
            }
            .setNegativeButton("Not now") { _, _ -> done() }
            .setOnCancelListener { done() }
            .show()
    }

    /**
     * The recovery passphrase, on the phone's own screen. Six words the app
     * minted at first run (or whatever the person typed themselves) — the one
     * secret that outlives this phone's hardware vault. Shown, never sent:
     * the WebView asked for this dialog but never sees its content.
     */
    private fun showRecoveryDialog(pass: String) {
        AlertDialog.Builder(this)
            .setTitle("Your recovery passphrase")
            .setMessage(
                pass + "\n\n" +
                    "Write it down somewhere that is not this phone. It opens this " +
                    "device's data even if the code and the phone's own vault are " +
                    "gone — a repair or a biometric reset must not cost you your spaces."
            )
            .setPositiveButton("I wrote it down", null)
            .show()
    }

    /** The passcode attempt, and the four answers it can have. */
    private fun tryPasscode(code: String) {
        // The verdict is FELT on the panel that asked, so this happens before
        // anything redraws — a moment later the keypad is gone and the buzz
        // would land on whatever replaced it.
        val asked = statusPanel
        when (val r = passcode.tryCode(code)) {
            is PasscodeVault.Attempt.Open -> {
                asked?.let { Haptics.accept(it) }
                // Straight into the ordinary open path — the controller owns
                // the directory and the lock, exactly as with a typed
                // passphrase. Nothing about the node knows a code was used.
                pendingPassphrase = r.passphrase
                controller.ensureStarted(r.passphrase, null, true)
            }
            is PasscodeVault.Attempt.Wrong -> {
                asked?.let { Haptics.refuse(it) }
                unlockMessage = "Not that one — ${r.attemptsLeft} of 10 tries left"
                showStatus("", unlockable = true)
            }
            PasscodeVault.Attempt.LockedOut -> {
                asked?.let { Haptics.refuse(it) }
                unlockMessage = "Too many tries. The code is gone — " +
                    "your passphrase still opens everything."
                showPassphraseStatus("Quiet is not running on this device yet.", unlockable = true)
            }
            PasscodeVault.Attempt.Malformed -> {
                asked?.let { Haptics.refuse(it) }
                unlockMessage = "That is not a code."
                showStatus("", unlockable = true)
            }
            PasscodeVault.Attempt.Unavailable -> {
                asked?.let { Haptics.refuse(it) }
                // The hardware key would not answer — on a locked phone that
                // is the ordinary reply, not a wrong code. Nothing was spent.
                unlockMessage = "Unlock the phone first, then try again."
                showPassphraseStatus("Quiet is not running on this device yet.", unlockable = true)
            }
        }
    }

    /**
     * Picking a code is TYPED TWICE, the same reasoning as [chooseCode]: a
     * mistyped code costs the easy way in at the worst moment. Generic over
     * what happens next, because onboarding binds it only after the core has
     * proven the key the code will wrap.
     */
    private fun pickCode(back: () -> Unit, onChosen: (String) -> Unit) {
        val escape = "Back" to back
        showKeypad(
            title = "Pick four digits",
            note = unlockMessage
                ?: "They open Quiet on this phone. The real key is longer, written by the app itself.",
            digits = 4,
            onComplete = { first ->
                unlockMessage = null
                showKeypad(
                    title = "Type them again",
                    note = "So a slip now does not become a locked-out phone later.",
                    digits = 4,
                    onComplete = { second ->
                        if (second == first) {
                            statusPanel?.let { Haptics.accept(it) }
                            onChosen(first)
                        } else {
                            statusPanel?.let { Haptics.refuse(it) }
                            unlockMessage = "Those were not the same — start again."
                            pickCode(back, onChosen)
                        }
                    },
                    onEscape = escape,
                )
            },
            onEscape = escape,
        )
    }

    /**
     * FIRST RUN: what should people call you, and four digits. The passphrase
     * is minted by the core (six words, the same generator as the desktop),
     * proven by OPENING before the code is bound, and readable afterwards in
     * Settings → This device — a keystore reset must never equal data loss,
     * so the real key stays reachable by its owner.
     */
    private fun showFreshOnboarding(text: String) {
        web.visibility = View.GONE
        statusPanel?.let { root.removeView(it) }

        val panel = SpaceLook.card(this)
        panel.addView(SpaceLook.title(this, "quite.space"))
        panel.addView(SpaceLook.sub(this, text))

        panel.addView(SpaceLook.section(this, "What should people call you?"))
        val nameField = SpaceLook.input(this, "Your name").apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_CAP_WORDS
            setText(lastName)
            setSelection(lastName.length)
            addTextChangedListener(object : TextWatcher {
                override fun afterTextChanged(s: Editable?) { lastName = s?.toString() ?: "" }
                override fun beforeTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) {}
                override fun onTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) {}
            })
        }
        panel.addView(nameField)
        val note = SpaceLook.note(this, unlockMessage
            ?: "Then pick four digits to open it with. The key itself is written " +
                "by the app — you can read it later in Settings → This device.")
        panel.addView(note)
        panel.addView(SpaceLook.primary(this, "Next").apply {
            setOnClickListener {
                val name = nameField.text.toString().trim()
                if (name.isEmpty()) {
                    note.text = "A name first — it is how the people you write to will see you."
                    return@setOnClickListener
                }
                unlockMessage = null
                pickCode(back = { showFreshOnboarding(text) }) { code ->
                    val generated = space.quiet.quietcore.Quietcore.suggestPassphrase()
                    if (generated.isEmpty()) {
                        unlockMessage = "Could not mint a key — try again."
                        showFreshOnboarding(text)
                        return@pickCode
                    }
                    pendingBindCode = code
                    pendingPassphrase = generated
                    // The CONTROLLER opens the node — one owner, one directory,
                    // one lock. The name rides the same call; the binding has
                    // threaded it end to end all along, the UI just never spoke.
                    controller.ensureStarted(generated, name, true)
                }
            }
        })

        panel.addView(SpaceLook.section(this, "Or: this is my second device"))
        val offerField = SpaceLook.input(this, "Pairing code from the other device").apply {
            inputType = InputType.TYPE_CLASS_TEXT
        }
        panel.addView(offerField)
        val pairNote = SpaceLook.note(this, "On the other device: Settings → Devices → Pair a new device. " +
            "Your name and spaces come across with the pairing.")
        panel.addView(pairNote)
        panel.addView(SpaceLook.plain(this, "Pair with my other device").apply {
            setOnClickListener {
                val offer = offerField.text.toString().trim()
                if (offer.isEmpty()) { pairNote.text = "Paste the code first."; return@setOnClickListener }
                val proceed = {
                    unlockMessage = null
                    pickCode(back = { showFreshOnboarding(text) }) { code ->
                        startPairCeremony(offer, code, text)
                    }
                }
                if (controller.hasIdentity() || controller.dataDir().listFiles()?.isNotEmpty() == true) {
                    // The dir is somebody's. Pairing writes an identity, and
                    // identities are never written over each other — so the
                    // fork is the PERSON'S to take, with its cost spelled out.
                    AlertDialog.Builder(this@QuietActivity)
                        .setTitle("This phone already holds Quiet data")
                        .setMessage("To pair as your other device, everything Quiet " +
                            "keeps on this phone must be erased first: its identity, " +
                            "its spaces' local history, its remembered code. " +
                            "This cannot be undone. Erase and pair?")
                        .setPositiveButton("Erase and pair") { _, _ ->
                            val err = controller.startOver()
                            if (err != null) { pairNote.text = err } else { proceed() }
                        }
                        .setNegativeButton("Keep it", null)
                        .show()
                    return@setOnClickListener
                }
                proceed()
            }
        })

        panel.addView(SpaceLook.ghost(this, "Use a long passphrase instead").apply {
            setOnClickListener {
                // The door for whoever wants to own the words themselves —
                // sticky, so a refused attempt re-renders THIS form.
                longFormPreferred = true
                unlockMessage = null
                showPassphraseStatus(text, unlockable = true)
            }
        })

        val screen = SpaceLook.screen(this, panel)
        root.addView(screen)
        statusPanel = screen
    }

    /**
     * The child's side of the ceremony, with a key nobody typed: minted here,
     * handed to the pairing (it encrypts THIS phone only — the freight is
     * sealed by the ceremony's own key), bound to the chosen code only after
     * the paired open proves it. Order is law, same as create.
     */
    private fun startPairCeremony(offer: String, code: String, backText: String) {
        val generated = space.quiet.quietcore.Quietcore.suggestPassphrase()
        if (generated.isEmpty()) {
            unlockMessage = "Could not mint a key — try again."
            showFreshOnboarding(backText)
            return
        }
        val refusal = controller.pairStart(generated, offer)
        if (refusal != null) {
            unlockMessage = refusal
            showFreshOnboarding(backText)
            return
        }

        web.visibility = View.GONE
        statusPanel?.let { root.removeView(it) }
        val panel = SpaceLook.card(this)
        panel.addView(SpaceLook.title(this, "Pairing"))
        val pairNote = SpaceLook.note(this, "Reaching your other device…")
        panel.addView(pairNote)
        val pairDigits = TextView(this).apply {
            gravity = Gravity.CENTER
            textSize = 34f
            letterSpacing = 0.3f
            setTextColor(SpaceLook.STAR)
            setShadowLayer(14f, 0f, 0f, 0x8CDFE8FF.toInt())
            setPadding(0, 24, 0, 12)
        }
        panel.addView(pairDigits)
        val approveBtn = SpaceLook.primary(this, "The digits match").apply {
            visibility = View.GONE
        }
        panel.addView(approveBtn)
        val screen = SpaceLook.screen(this, panel)
        root.addView(screen)
        statusPanel = screen

        var cancelled = false
        val poll = object : Runnable {
            override fun run() {
                if (cancelled) return
                val st = controller.pairState()
                when (st.optString("stage")) {
                    "digits" -> {
                        pairDigits.text = st.optString("digits")
                        approveBtn.visibility = View.VISIBLE
                        pairNote.text = "Compare with the other screen. Continue only if the digits match."
                        web.postDelayed(this, 400)
                    }
                    "done" -> {
                        pairNote.text = "Paired — opening…"
                        pendingBindCode = code
                        pendingPassphrase = generated
                        controller.ensureStarted(generated, null, true)
                    }
                    "failed" -> {
                        unlockMessage = "Failed: " + st.optString("error")
                        showFreshOnboarding(backText)
                    }
                    else -> web.postDelayed(this, 400)
                }
            }
        }
        approveBtn.setOnClickListener {
            approveBtn.visibility = View.GONE
            pairNote.text = "Waiting for the other device to approve too…"
            controller.pairApprove()
        }
        panel.addView(SpaceLook.ghost(this, "Start over").apply {
            setOnClickListener {
                cancelled = true
                unlockMessage = null
                showFreshOnboarding(backText)
            }
        })
        web.postDelayed(poll, 400)
    }

    private fun showPassphraseStatus(text: String, unlockable: Boolean) {
        // FIRST RUN, THE SHORT WAY: a name and four digits, and the key draws
        // itself — the desktop's create screen (lockgate, 61a0d6e), finally
        // ported. The long-passphrase form below stays one button away for
        // whoever wants to own the words themselves.
        if (unlockable && !longFormPreferred && !controller.hasIdentity()) {
            showFreshOnboarding(text)
            return
        }
        web.visibility = View.GONE
        statusPanel?.let { root.removeView(it) }

        val panel = SpaceLook.card(this)
        panel.addView(SpaceLook.title(this, "quite.space"))
        panel.addView(SpaceLook.sub(this, text))
        if (unlockable) {
            val fresh = !controller.hasIdentity()
            fun sectionLabel(text: String) = SpaceLook.section(this, text)
            fun hintView(text: String) = SpaceLook.note(this, text)

            if (fresh) panel.addView(sectionLabel("A passphrase for this phone"))
            val field = SpaceLook.input(this, "Passphrase").apply {
                inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
                // THE TEXT SURVIVES A REFUSAL. The panel is rebuilt on every
                // state change, so a rejected attempt used to take the typed
                // characters with it and the person started over — on a phone
                // keyboard, with a passphrase, blind behind dots.
                setText(lastTyped)
                setSelection(lastTyped.length)
                addTextChangedListener(object : TextWatcher {
                    override fun afterTextChanged(s: Editable?) { lastTyped = s?.toString() ?: "" }
                    override fun beforeTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) {}
                    override fun onTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) {}
                })
            }
            panel.addView(field)

            // TYPED TWICE ON A FIRST RUN, because there is no second chance:
            // nobody can reset it and nobody can read past it, so a typo
            // silently encrypts everything to a key its owner never knew.
            // An unlock of an existing keystore stays one field — the second
            // chance there is simply typing again.
            var field2: EditText? = null
            if (fresh) {
                field2 = SpaceLook.input(this, "Type it again").apply {
                    inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
                }
                panel.addView(field2)
            }

            val note = SpaceLook.note(this, unlockMessage ?: (if (fresh)
                "At least 8 characters. It encrypts everything on THIS phone — " +
                "you may reuse the passphrase from your other device, or make a new one."
            else
                "At least 8 characters. This is what encrypts everything on this device."))
            panel.addView(note)

            if (fresh) {
                panel.addView(SpaceLook.plain(this, "Suggest a passphrase").apply {
                    setOnClickListener {
                        val p = space.quiet.quietcore.Quietcore.suggestPassphrase()
                        if (p.isNotEmpty()) {
                            field.setText(p); field2?.setText(p)
                            field.inputType = InputType.TYPE_CLASS_TEXT
                            field2?.inputType = InputType.TYPE_CLASS_TEXT
                            note.text = "Write these words down somewhere that is not this phone."
                        }
                    }
                })
            }

            fun passOrComplain(): String? {
                val pass = field.text.toString()
                if (pass.length < MIN_PASSPHRASE) {
                    note.text = "Too short — a passphrase needs at least $MIN_PASSPHRASE characters."
                    return null
                }
                if (fresh && field2?.text.toString() != pass) {
                    note.text = "The two passphrase fields differ — a typo here would lock you out forever."
                    return null
                }
                return pass
            }

            // ---- pairing: its own section, not part of the pile ----
            panel.addView(sectionLabel(if (fresh)
                "Or: this is my second device" else "Pair as a second device instead"))
            val offerField = SpaceLook.input(this, "Pairing code from the other device").apply {
                inputType = InputType.TYPE_CLASS_TEXT
            }
            panel.addView(offerField)
            val pairNote = hintView("On the other device: Settings → Devices → Pair a new device.")
            panel.addView(pairNote)
            val pairDigits = TextView(this).apply {
                gravity = Gravity.CENTER
                textSize = 34f
                letterSpacing = 0.3f
                setTextColor(SpaceLook.STAR)
                setShadowLayer(14f, 0f, 0f, 0x8CDFE8FF.toInt())
            }
            panel.addView(pairDigits)
            val approveBtn = SpaceLook.primary(this, "The digits match").apply {
                visibility = View.GONE
            }
            panel.addView(approveBtn)
            val pairBtn = SpaceLook.plain(this, "Pair with my other device")
            panel.addView(pairBtn)
            val poll = object : Runnable {
                override fun run() {
                    val st = controller.pairState()
                    when (st.optString("stage")) {
                        "digits" -> {
                            pairDigits.text = st.optString("digits")
                            approveBtn.visibility = View.VISIBLE
                            pairNote.text = "Compare with the other screen. Continue only if the digits match."
                            web.postDelayed(this, 400)
                        }
                        "done" -> {
                            pairNote.text = "Paired — opening…"
                            pendingPassphrase = lastTyped
                            controller.ensureStarted(lastTyped, null, true)
                        }
                        "failed" -> {
                            pairNote.text = "Failed: " + st.optString("error")
                            pairBtn.isEnabled = true
                            approveBtn.visibility = View.GONE
                            pairDigits.text = ""
                        }
                        else -> web.postDelayed(this, 400)
                    }
                }
            }
            approveBtn.setOnClickListener {
                approveBtn.visibility = View.GONE
                pairNote.text = "Waiting for the other device to approve too…"
                controller.pairApprove()
            }
            fun beginPair(offer: String, pass: String) {
                val refusal = controller.pairStart(pass, offer)
                if (refusal != null) { pairNote.text = refusal; return }
                pairBtn.isEnabled = false
                pairNote.text = "Reaching your other device…"
                web.postDelayed(poll, 400)
            }
            pairBtn.setOnClickListener {
                val offer = offerField.text.toString().trim()
                if (offer.isEmpty()) { pairNote.text = "Paste the code first."; return@setOnClickListener }
                val pass = passOrComplain()
                if (pass == null) {
                    // The complaint lives under the passphrase fields — echo it
                    // HERE too, where the finger just was.
                    pairNote.text = note.text
                    return@setOnClickListener
                }
                if (controller.hasIdentity() || controller.dataDir().listFiles()?.isNotEmpty() == true) {
                    // The dir is somebody's. Pairing writes an identity, and
                    // identities are never written over each other — so the
                    // fork is the PERSON'S to take, with its cost spelled out.
                    AlertDialog.Builder(this@QuietActivity)
                        .setTitle("This phone already holds Quiet data")
                        .setMessage("To pair as your other device, everything Quiet " +
                            "keeps on this phone must be erased first: its identity, " +
                            "its spaces' local history, its remembered code. " +
                            "This cannot be undone. Erase and pair?")
                        .setPositiveButton("Erase and pair") { _, _ ->
                            val err = controller.startOver()
                            if (err != null) { pairNote.text = err } else { beginPair(offer, pass) }
                        }
                        .setNegativeButton("Keep it", null)
                        .show()
                    return@setOnClickListener
                }
                beginPair(offer, pass)
            }

            if (fresh) panel.addView(sectionLabel("Or: start fresh on this phone"))
            panel.addView(SpaceLook.primary(this, if (fresh) "Create and open" else "Open").apply {
                setOnClickListener {
                    val pass = passOrComplain() ?: return@setOnClickListener
                    pendingPassphrase = pass
                    // The CONTROLLER opens the node. This Activity never calls
                    // the binding's Start and never resolves a data directory
                    // — one owner, one directory, one lock.
                    controller.ensureStarted(pass, null, true)
                }
            })
        }
        val screen = SpaceLook.screen(this, panel)
        root.addView(screen)
        statusPanel = screen
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
    /**
     * SR-0 — the host's pushes into the page, both guarded the same way as
     * every evaluateJavascript here: visible WebView, loopback page only.
     * The bridge stays getterless; these are the answers.
     */
    private fun pushRadioEcho(space: String) {
        if (web.visibility != View.VISIBLE) return
        val enabled = controller.radio.enabled(space)
        val autoplay = controller.radio.autoplay(space)
        val js = "window.quietRadio && window.quietRadio(" +
            JSONObject.quote(space) + ", {enabled:$enabled, autoplay:$autoplay});"
        web.evaluateJavascript(js, null)
    }

    private val pttSink: (String) -> Unit = { json ->
        runOnUiThread {
            if (web.visibility == View.VISIBLE) {
                web.evaluateJavascript("window.quietPtt && window.quietPtt($json);", null)
            }
        }
    }

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
    /**
     * Hand one of our own files to Android's downloads.
     *
     * THE HOST CHECK IS THE WHOLE SECURITY OF THIS. DownloadManager fetches
     * in another process with its own network access, so a page that could
     * name any URL here would have found a way around the WebView's loopback
     * lock. Only the node's own address is accepted, and the check is the
     * same shape as [LocalOnlyClient]'s.
     *
     * The session token rides in the URL because that is how the node's API
     * is addressed at all, and it therefore reaches DownloadManager's own
     * records. That is a loopback token for a node the person is already
     * holding — but it is the reason this refuses anything it does not have
     * to accept.
     */
    private fun saveDownload(url: String, contentDisposition: String?, mimeType: String?) {
        val uri = runCatching { Uri.parse(url) }.getOrNull()
        val host = uri?.host
        if (uri == null || (host != "127.0.0.1" && host != "localhost")) {
            Log.w(TAG, "refused a download that was not ours: $url")
            return
        }
        val name = URLUtil.guessFileName(url, contentDisposition, mimeType)
        val req = DownloadManager.Request(uri)
            .setTitle(name)
            .setMimeType(mimeType)
            .setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
            .setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, name)
        try {
            (getSystemService(DOWNLOAD_SERVICE) as DownloadManager).enqueue(req)
            Toast.makeText(this, "Saving $name…", Toast.LENGTH_SHORT).show()
        } catch (t: Throwable) {
            // Saying nothing would leave a button that looks broken, which is
            // exactly what this whole listener exists to stop.
            Log.w(TAG, "the download could not be queued", t)
            Toast.makeText(this, "This device would not save the file.", Toast.LENGTH_LONG).show()
        }
    }

    private inner class LocalOnlyClient : WebViewClient() {
        override fun shouldOverrideUrlLoading(
            view: WebView,
            request: WebResourceRequest,
        ): Boolean {
            // SUBFRAMES ARE NOT THIS METHOD'S BUSINESS, and saying so is the
            // difference between a video that plays and one that throws the
            // person out into a browser.
            //
            // This callback fires for every frame, not only the top one. The
            // conversation frames the node's own /player, which frames the
            // video embed — so without this line the embed's navigation
            // looked exactly like "the page is trying to leave", and was
            // answered by launching an Intent and leaving the frame blank.
            //
            // What may be framed is decided by the Content-Security-Policy
            // the node serves (frame-src 'self' on the interface, one named
            // host on the player), which is where that rule belongs: a
            // policy the browser enforces on every load, rather than a
            // string comparison in a callback that only sees navigations.
            //
            // The lock this method exists for is untouched — the TOP frame
            // still cannot go anywhere but loopback.
            if (!request.isForMainFrame) return false
            val host = request.url.host
            if (host == "127.0.0.1" || host == "localhost") return false
            startActivity(Intent(Intent.ACTION_VIEW, request.url as Uri))
            return true
        }
    }

    companion object {
        private const val TAG = "quiet-activity"

        /**
         * Mirrors kernel/storage's own floor (ErrPassphraseTooShort). Stated
         * here so the screen can say it BEFORE the core refuses, and kept in
         * step with that constant rather than chosen independently — a screen
         * that demanded more than the keystore does would be inventing a rule.
         */
        private const val MIN_PASSPHRASE = 8

        const val EXTRA_SPACE = "space_id"
        const val EXTRA_MESSAGE = "target_message_id"
        const val EXTRA_EVENT = "event_id"
    }
}
