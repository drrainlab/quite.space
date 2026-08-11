package space.quiet.arprobe

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import android.util.Log
import org.json.JSONObject
import space.quiet.quietcore.Quietcore
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * A four-digit code that opens this device, wrapped in hardware.
 *
 * TWO LAYERS, AND THE OUTER ONE IS THE WHOLE POINT.
 *
 *     inner   Go's kernel/passcode: scrypt(N=2^17, 128 MiB) over the code,
 *             XChaCha20-Poly1305 over the passphrase, and a durable
 *             attempt counter carried inside the blob
 *     outer   AES-GCM under an AndroidKeyStore key that cannot be exported
 *
 * On a laptop the inner layer alone buys about an hour: four digits are
 * 10 000 possibilities, and a copied file can be ground through offline at
 * a third of a second each. Here that attack does not exist, because the
 * outer key is not in the file — it is in hardware, on this handset, and
 * nothing that leaves the device carries it. What remains is guessing at
 * the screen, which is exactly what the attempt counter is for.
 *
 * So this is [PassphraseVault]'s sibling, not its replacement. That one
 * remembers a passphrase so the app opens with no question at all; this
 * one asks for four digits first. Both keep their secret behind the same
 * hardware; they differ in what they ask of the person.
 *
 * THE COUNTER IS SPENT BEFORE THE GUESS IS JUDGED. Go hands back an
 * updated blob on every path, including refusal, and [tryCode] stores it
 * before it looks at the answer. Storing after would mean a kill during
 * the KDF's third of a second recorded nothing, which turns ten tries into
 * as many as somebody cares to script. At lockout Go returns a spent
 * tombstone rather than nothing, so even a mistake here fails closed.
 *
 * NOTHING UNVERIFIED IS EVER SEALED — [bind] is called only with a
 * passphrase the core has actually opened with, the same rule
 * [PassphraseVault.save] follows and for the same reason.
 */
internal class PasscodeVault(app: Context) {

    private val prefs = app.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** Whether a code is bound on this device. */
    fun has(): Boolean = prefs.contains(KEY_BLOB)

    /** How many digits the bound code has, for drawing the right dots. */
    fun digits(): Int = prefs.getInt(KEY_DIGITS, 4)

    /**
     * Seal [passphrase] under [code]. The caller must already have opened
     * the core with that passphrase.
     */
    fun bind(code: String, passphrase: String): Boolean = try {
        val inner = Quietcore.sealForVault(code, passphrase)
        val cipher = Cipher.getInstance(TRANSFORM)
        cipher.init(Cipher.ENCRYPT_MODE, key()!!)
        val blob = cipher.iv + cipher.doFinal(inner)
        prefs.edit()
            .putString(KEY_BLOB, Base64.encodeToString(blob, Base64.NO_WRAP))
            .putInt(KEY_DIGITS, code.length)
            .commit()
    } catch (t: Throwable) {
        Log.w(TAG, "could not bind the code", t)
        false
    }

    /** What a [tryCode] attempt came to. */
    sealed interface Attempt {
        /** Right code; [passphrase] opens the core. */
        data class Open(val passphrase: String) : Attempt
        /** Wrong code, with what is left before the shortcut erases itself. */
        data class Wrong(val attemptsLeft: Int) : Attempt
        /** Spent. The code is gone; the passphrase still opens everything. */
        data object LockedOut : Attempt
        /** Not four digits — refused without spending anything. */
        data object Malformed : Attempt
        /** Nothing bound, or the hardware key is unreadable right now. */
        data object Unavailable : Attempt
    }

    fun tryCode(code: String): Attempt {
        val stored = prefs.getString(KEY_BLOB, null) ?: return Attempt.Unavailable
        val inner = try {
            val raw = Base64.decode(stored, Base64.NO_WRAP)
            val cipher = Cipher.getInstance(TRANSFORM)
            cipher.init(
                Cipher.DECRYPT_MODE, key(create = false) ?: return Attempt.Unavailable,
                GCMParameterSpec(TAG_BITS, raw, 0, IV_LEN),
            )
            cipher.doFinal(raw, IV_LEN, raw.size - IV_LEN)
        } catch (t: Throwable) {
            // The hardware key is gone or refuses right now. NOT a wrong
            // code, and not a reason to erase anything: on a locked device
            // setUnlockedDeviceRequired makes this the ordinary answer.
            Log.w(TAG, "the wrapped code could not be read", t)
            return Attempt.Unavailable
        }

        val res = JSONObject(Quietcore.openFromVault(code, inner))

        // STORE THE UPDATED BLOB FIRST, before looking at the verdict. This
        // is where the spent attempt lives, and Go hands one back on every
        // path — including refusal, and including lockout, where it is a
        // spent tombstone.
        res.optString("blob").takeIf { it.isNotEmpty() }?.let { rewrap(it) }

        if (res.optBoolean("ok")) {
            return Attempt.Open(res.getString("passphrase"))
        }
        return when (res.optString("kind")) {
            "locked_out" -> { forget(); Attempt.LockedOut }
            "malformed" -> Attempt.Malformed
            "none" -> Attempt.Unavailable
            else -> Attempt.Wrong(attemptsLeft())
        }
    }

    /** Attempts remaining, read without spending one. */
    fun attemptsLeft(): Int = prefs.getInt(KEY_LEFT, MAX_ATTEMPTS)

    /**
     * Forget the code. Access is untouched: the passphrase still opens the
     * node, and [PassphraseVault] is a separate decision with its own
     * button. The key goes before the ciphertext, so a crash between the
     * two lines leaves a blob nobody can read rather than the reverse.
     */
    fun forget() {
        try {
            keyStore().deleteEntry(ALIAS)
        } catch (t: Throwable) {
            Log.w(TAG, "could not delete the code key", t)
        }
        prefs.edit().remove(KEY_BLOB).remove(KEY_DIGITS).remove(KEY_LEFT).commit()
    }

    /** Re-seal the updated inner blob under the same hardware key. */
    private fun rewrap(innerB64: String) {
        try {
            val inner = Base64.decode(innerB64, Base64.NO_WRAP)
            val cipher = Cipher.getInstance(TRANSFORM)
            cipher.init(Cipher.ENCRYPT_MODE, key()!!)
            val blob = cipher.iv + cipher.doFinal(inner)
            // The attempt count is mirrored into prefs purely so the screen
            // can show it without another decryption. The COUNT THAT BINDS
            // is the one inside the sealed blob; this copy is a display
            // convenience and is never trusted for a decision.
            val left = try {
                JSONObject(String(inner, Charsets.UTF_8)).optInt("attempts_left", MAX_ATTEMPTS)
            } catch (_: Throwable) {
                MAX_ATTEMPTS
            }
            prefs.edit()
                .putString(KEY_BLOB, Base64.encodeToString(blob, Base64.NO_WRAP))
                .putInt(KEY_LEFT, left)
                .commit()
        } catch (t: Throwable) {
            Log.w(TAG, "could not store the spent attempt", t)
        }
    }

    private fun keyStore(): KeyStore = KeyStore.getInstance(PROVIDER).apply { load(null) }

    /** As in [PassphraseVault]: reading must never mint a key. */
    private fun key(create: Boolean = true): SecretKey? {
        val ks = keyStore()
        (ks.getEntry(ALIAS, null) as? KeyStore.SecretKeyEntry)?.let { return it.secretKey }
        if (!create) return null

        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, PROVIDER)
        gen.init(
            KeyGenParameterSpec.Builder(
                ALIAS,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .apply {
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                        setUnlockedDeviceRequired(true)
                    }
                }
                .build(),
        )
        return gen.generateKey()
    }

    private companion object {
        const val TAG = "quiet-passcode"
        const val PREFS = "quiet-unlock"
        const val KEY_BLOB = "passcode_v1"
        const val KEY_DIGITS = "passcode_digits"
        const val KEY_LEFT = "passcode_left"
        const val PROVIDER = "AndroidKeyStore"
        const val ALIAS = "quiet.passcode.v1"
        const val TRANSFORM = "AES/GCM/NoPadding"
        const val IV_LEN = 12
        const val TAG_BITS = 128
        const val MAX_ATTEMPTS = 10
    }
}
