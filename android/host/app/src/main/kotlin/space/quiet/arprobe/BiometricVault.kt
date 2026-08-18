package space.quiet.arprobe

import android.app.Activity
import android.content.Context
import android.hardware.biometrics.BiometricManager
import android.hardware.biometrics.BiometricPrompt
import android.os.Build
import android.os.CancellationSignal
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyPermanentlyInvalidatedException
import android.security.keystore.KeyProperties
import android.security.keystore.StrongBoxUnavailableException
import android.util.Base64
import android.util.Log
import java.security.KeyStore
import java.security.UnrecoverableKeyException
import javax.crypto.AEADBadTagException
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * The passphrase, sealed behind a face or a fingerprint.
 *
 * WHAT THIS FIXES, AND IT IS SOMETHING [PassphraseVault] SAYS ABOUT ITSELF:
 * that vault "protects NOTHING against somebody holding this phone
 * unlocked". Its key is usable by this process whenever the screen is on, so
 * the app opens for whoever is holding the handset. That is the ordinary
 * trade, and this class is how a person reverses it — the key here CANNOT be
 * used at all until the hardware has just seen a face or a finger it
 * enrolled. Not a check this code performs and could forget to: the keystore
 * refuses the cipher, in hardware, and there is no branch to get wrong.
 *
 * SO IT IS OPT-IN, ALWAYS. The sibling vault's comment is right that a
 * biometric gate "is a different feature with a different decision behind
 * it, not a default to slip in here" — this is that feature, reached by
 * somebody deciding, and [forget] hands the phone back to the old trade.
 *
 * STRONGBOX WHERE THERE IS ONE. A separate security chip means the wrap key
 * lives somewhere the main processor cannot reach even if it is entirely
 * owned. Not every handset has one, and asking for it where there is none
 * throws rather than degrades — so the failure is caught and the ordinary
 * TEE key is made instead. Both are hardware; one is more hardware.
 *
 * API 30 IS THE FLOOR, deliberately and not out of laziness. 30 is where
 * setAllowedAuthenticators and setUserAuthenticationParameters both exist,
 * and where the prompt stops needing a negative button that lies about what
 * it does. Below it [available] answers UNSUPPORTED and the app is exactly
 * what it was before this file — the older devices lose a feature they never
 * had, rather than getting a worse copy of it.
 *
 * NOTHING UNVERIFIED IS EVER SEALED. [enroll] takes a passphrase the core
 * has already opened with, the same rule both sibling vaults follow.
 */
internal class BiometricVault(private val app: Context) {

    /** What this handset can do, said plainly enough to put on a screen. */
    enum class Availability {
        /** A face or finger is enrolled and this can be turned on. */
        READY,

        /** The hardware is here, but nobody has enrolled anything yet. */
        NOT_ENROLLED,

        /** No biometric hardware, or an Android too old to do this properly. */
        UNSUPPORTED,
    }

    private val prefs = app.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** Whether a passphrase is sealed behind a face on this device. */
    fun has(): Boolean = prefs.contains(KEY_BLOB)

    fun available(): Availability {
        if (Build.VERSION.SDK_INT < 30) return Availability.UNSUPPORTED
        val m = app.getSystemService(BiometricManager::class.java) ?: return Availability.UNSUPPORTED
        return when (m.canAuthenticate(BiometricManager.Authenticators.BIOMETRIC_STRONG)) {
            BiometricManager.BIOMETRIC_SUCCESS -> Availability.READY
            BiometricManager.BIOMETRIC_ERROR_NONE_ENROLLED -> Availability.NOT_ENROLLED
            else -> Availability.UNSUPPORTED
        }
    }

    /**
     * Turn it on for a passphrase THE CORE HAS ALREADY OPENED WITH.
     *
     * The prompt appears here too, and that is not ceremony: a per-use key
     * cannot encrypt either, so the person proving who they are is what
     * makes the seal possible. It also means nobody can quietly bind a
     * stranger's face to somebody else's passphrase while the app is open in
     * their hand.
     */
    fun enroll(activity: Activity, passphrase: String, done: (Boolean) -> Unit) {
        if (available() != Availability.READY) { done(false); return }
        val k = newKey()
        if (k == null) { done(false); return }
        val cipher = try {
            Cipher.getInstance(TRANSFORM).apply { init(Cipher.ENCRYPT_MODE, k) }
        } catch (t: Throwable) {
            Log.w(TAG, "could not start the seal", t); done(false); return
        }
        prompt(
            activity,
            title = "Confirm it is you",
            subtitle = "Then your face or fingerprint opens quite.space",
            cipher = cipher,
            onAuthed = { c ->
                done(
                    try {
                        val body = c.doFinal(passphrase.toByteArray(Charsets.UTF_8))
                        prefs.edit()
                            .putString(KEY_BLOB, Base64.encodeToString(c.iv + body, Base64.NO_WRAP))
                            .commit()
                    } catch (t: Throwable) {
                        Log.w(TAG, "could not seal the passphrase", t); false
                    }
                )
            },
            onRefused = { done(false) },
        )
    }

    /**
     * Ask for the face, and hand back the passphrase if it is given.
     *
     * A REFUSAL IS NOT A FAILURE. Cancelling the prompt is somebody choosing
     * a different door, and [onRefused] is where the caller goes looking for
     * one — see [UnlockRoute.refused], which exists so that door can never be
     * this prompt again.
     *
     * The two kinds of failure are told apart exactly as [PassphraseVault]
     * tells them apart, and for the reason it learned the hard way: a key
     * that is temporarily unusable must not be deleted. The one addition is
     * biometric re-enrolment — a new face added to the phone invalidates this
     * key BY DESIGN, and that one really is permanent, so the seal is dropped
     * and the person is asked for the other way in.
     */
    fun unlock(activity: Activity, onOpen: (String) -> Unit, onRefused: () -> Unit) {
        val raw = prefs.getString(KEY_BLOB, null)
        if (raw == null) { onRefused(); return }
        val cipher = try {
            val blob = Base64.decode(raw, Base64.NO_WRAP)
            if (blob.size <= IV_LEN) throw AEADBadTagException("blob too short")
            val k = key() ?: throw KeyPermanentlyInvalidatedException()
            Cipher.getInstance(TRANSFORM).apply {
                init(Cipher.DECRYPT_MODE, k, GCMParameterSpec(TAG_BITS, blob, 0, IV_LEN))
            }
        } catch (t: Throwable) {
            drop(t); onRefused(); return
        }
        prompt(
            activity,
            title = "Open quite.space",
            subtitle = null,
            cipher = cipher,
            onAuthed = { c ->
                try {
                    val blob = Base64.decode(raw, Base64.NO_WRAP)
                    onOpen(String(c.doFinal(blob, IV_LEN, blob.size - IV_LEN), Charsets.UTF_8))
                } catch (t: Throwable) {
                    drop(t); onRefused()
                }
            },
            onRefused = onRefused,
        )
    }

    /** Stop opening with a face. The key goes first, so a crash between the
     *  two lines leaves a blob nobody can read rather than the reverse. */
    fun forget() {
        try {
            keyStore().deleteEntry(ALIAS)
        } catch (t: Throwable) {
            Log.w(TAG, "could not delete the key", t)
        }
        prefs.edit().remove(KEY_BLOB).commit()
    }

    /** Forget only what can never be read again; keep what is merely unusable now. */
    private fun drop(t: Throwable) {
        var e: Throwable? = t
        while (e != null) {
            when (e) {
                is KeyPermanentlyInvalidatedException,
                is UnrecoverableKeyException,
                is AEADBadTagException,
                    -> {
                    Log.w(TAG, "the sealed passphrase can never be read again — forgetting it", t)
                    forget()
                    return
                }
            }
            e = e.cause
        }
        Log.w(TAG, "the sealed passphrase cannot be read right now; keeping it", t)
    }

    private fun prompt(
        activity: Activity,
        title: String,
        subtitle: String?,
        cipher: Cipher,
        onAuthed: (Cipher) -> Unit,
        onRefused: () -> Unit,
    ) {
        if (Build.VERSION.SDK_INT < 30) { onRefused(); return }
        val p = BiometricPrompt.Builder(activity)
            .setTitle(title)
            .apply { subtitle?.let { setSubtitle(it) } }
            // BIOMETRIC_STRONG ONLY, and no device credential. A PIN fallback
            // here would make the phone's own lock screen a second way into
            // this app, which is not what somebody turning this on is asking
            // for — they already have their own four digits for that, and
            // those are what the negative button leads back to.
            .setAllowedAuthenticators(BiometricManager.Authenticators.BIOMETRIC_STRONG)
            .setNegativeButton("Another way in", activity.mainExecutor) { _, _ -> onRefused() }
            .build()
        p.authenticate(
            BiometricPrompt.CryptoObject(cipher),
            CancellationSignal(),
            activity.mainExecutor,
            object : BiometricPrompt.AuthenticationCallback() {
                override fun onAuthenticationSucceeded(r: BiometricPrompt.AuthenticationResult) {
                    // The cipher the HARDWARE authorised, not the one handed
                    // in: they are the same object today, and relying on that
                    // is relying on an implementation detail of the prompt.
                    val c = r.cryptoObject?.cipher
                    if (c == null) onRefused() else onAuthed(c)
                }

                override fun onAuthenticationError(code: Int, msg: CharSequence) {
                    // Every error ends the attempt — including lockout, which
                    // is the system telling the person to use another door
                    // rather than something for this class to retry.
                    onRefused()
                }

                // onAuthenticationFailed is a face the sensor did not
                // recognise. The prompt stays up and tries again on its own;
                // nothing here should end the attempt for it.
            },
        )
    }

    private fun keyStore(): KeyStore = KeyStore.getInstance(PROVIDER).apply { load(null) }

    /** The existing key, or null. Reading never mints one — see PassphraseVault. */
    private fun key(): SecretKey? =
        (keyStore().getEntry(ALIAS, null) as? KeyStore.SecretKeyEntry)?.secretKey

    /** A fresh key, replacing any older one: enrolling again is a new seal. */
    private fun newKey(): SecretKey? {
        if (Build.VERSION.SDK_INT < 30) return null
        for (strongBox in listOf(true, false)) {
            try {
                val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, PROVIDER)
                gen.init(
                    KeyGenParameterSpec.Builder(
                        ALIAS,
                        KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
                    )
                        .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                        .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                        .setKeySize(256)
                        // THE WHOLE POINT, IN TWO LINES. Authentication is
                        // required, and the window is zero seconds — so it is
                        // required for THIS use, not for a few minutes after
                        // some earlier one.
                        .setUserAuthenticationRequired(true)
                        .setUserAuthenticationParameters(
                            0,
                            KeyProperties.AUTH_BIOMETRIC_STRONG,
                        )
                        // A NEW FACE ON THE PHONE KILLS THIS KEY. Without
                        // this, somebody who could add their own fingerprint
                        // to an unlocked handset would inherit the seal.
                        .setInvalidatedByBiometricEnrollment(true)
                        .setIsStrongBoxBacked(strongBox)
                        .build()
                )
                return gen.generateKey()
            } catch (t: Throwable) {
                if (strongBox && hasCause(t) { it is StrongBoxUnavailableException }) {
                    Log.i(TAG, "no StrongBox on this device; using the ordinary hardware key")
                    continue
                }
                Log.w(TAG, "could not make a key behind the biometric", t)
                return null
            }
        }
        return null
    }

    private inline fun hasCause(t: Throwable, pred: (Throwable) -> Boolean): Boolean {
        var e: Throwable? = t
        while (e != null) {
            if (pred(e)) return true
            e = e.cause
        }
        return false
    }

    private companion object {
        const val TAG = "quiet.bio"
        const val PREFS = "quiet.biometric"
        const val KEY_BLOB = "sealed"
        const val ALIAS = "quiet.passphrase.bio.v1"
        const val PROVIDER = "AndroidKeyStore"
        const val TRANSFORM = "AES/GCM/NoPadding"
        const val IV_LEN = 12
        const val TAG_BITS = 128
    }
}
