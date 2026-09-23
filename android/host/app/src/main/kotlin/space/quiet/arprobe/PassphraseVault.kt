package space.quiet.arprobe

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyPermanentlyInvalidatedException
import android.security.keystore.KeyProperties
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
 * The passphrase, kept so the app opens without asking again.
 *
 * WHY THIS EXISTS. The node's keystore is opened with a passphrase and there
 * was nowhere to keep one, so every cold start met a blank field. On a laptop
 * that is a terminal command somebody chose to run; on a phone it is the first
 * thing a person sees after tapping an icon, several times a day, and the
 * beta's own testers hit it before they hit anything else.
 *
 * WHAT IT PROTECTS, SAID PLAINLY, because a vault that oversells itself is
 * worse than no vault. The key lives in the AndroidKeyStore, which on modern
 * hardware means it never enters this process's memory and cannot leave the
 * device — so the stored value survives neither a copied file, an adb backup,
 * nor another app reading our directory. It protects NOTHING against somebody
 * holding this phone unlocked: they can open the app, which is the same thing
 * they could do if they knew the passphrase. That trade is the ordinary one
 * every messenger on the device makes, and it is the person's to reverse —
 * [forget] is wired to a setting, not buried here.
 *
 * NOTHING UNVERIFIED IS EVER STORED. [save] is called only after the core has
 * actually opened with the value, so a typo cannot be written down and then
 * replayed forever as an auto-open that always fails.
 */
internal class PassphraseVault(app: Context) {

    private val prefs = app.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** Whether this device has a passphrase to open with. */
    fun has(): Boolean = prefs.contains(KEY_BLOB) || prefs.contains(KEY_BLOB_BG)

    /**
     * WHETHER THE NODE MAY OPEN WHILE THE PHONE IS LOCKED (AN-3). On unless
     * the person turned it off.
     *
     * The first key here is usable only on an unlocked device. That was free
     * caution until the day it was measured against what people need from a
     * messenger: Android restarts our process in a pocket — after an update,
     * after a battery saver's sweep — and a node that cannot open there
     * receives nothing a person can read until they pick the phone up. For
     * somebody waiting on a message that matters, that is the app failing.
     *
     * So there is a second key without that one requirement, and it is the
     * DEFAULT, by the owner's decision: a messenger that stays silent is the
     * worse failure for most people, and every other messenger on the phone
     * already makes this trade. What it costs, said plainly: with this on,
     * the lock screen is no longer a second wall in front of the node —
     * somebody who defeats Android's own protection of a locked phone finds
     * the node openable. The key still never leaves the secure hardware, a
     * copied file or a backup is still useless, and after a reboot nothing
     * opens before the first unlock, because Android itself keeps this app's
     * files sealed until then. It is a setting, in the same section that
     * offers to forget the passphrase altogether.
     */
    fun receiveWhileLocked(): Boolean = prefs.getBoolean(KEY_RECEIVE_LOCKED, true)

    /**
     * Whether the code or the face is asked for at the screen when a
     * passphrase is remembered. OFF by default (owner's decision after the
     * beta: the code every time, and the face offer it invites). See
     * [UnlockRoute.doorsApply] for the one rule that reads it.
     */
    fun requireCode(): Boolean = prefs.getBoolean(KEY_REQUIRE_CODE, false)

    fun setRequireCode(on: Boolean): Boolean = prefs.edit().putBoolean(KEY_REQUIRE_CODE, on).commit()

    /**
     * Change the choice and RE-SEAL what is already stored, so the switch
     * means something the moment it is thrown. Needs the value in hand: the
     * caller passes the passphrase the node is open with, or we read our own
     * copy (the person is looking at a settings screen, so the phone is
     * unlocked and either key works). False = nothing changed.
     */
    fun setReceiveWhileLocked(on: Boolean, openedWith: String?, keepCopy: Boolean = true): Boolean {
        if (!keepCopy) {
            prefs.edit().putBoolean(KEY_RECEIVE_LOCKED, on).commit()
            forget()
            return true
        }
        val pass = openedWith?.takeIf { it.isNotEmpty() } ?: load() ?: return false
        val was = receiveWhileLocked()
        prefs.edit().putBoolean(KEY_RECEIVE_LOCKED, on).commit()
        if (save(pass)) return true
        prefs.edit().putBoolean(KEY_RECEIVE_LOCKED, was).commit()
        return false
    }

    /**
     * The stored passphrase, or null when there is none or it cannot be read
     * RIGHT NOW.
     *
     * TWO KINDS OF FAILURE, AND THEY MUST NOT BE TREATED ALIKE — measured on
     * a phone, where the first launch after installing an update happened
     * with the screen locked and the log said:
     *
     *     KeyStoreException: Device locked (internal Keystore code: -72)
     *
     * The key is marked setUnlockedDeviceRequired, so a locked device cannot
     * use it. That is TEMPORARY: it will work in a second, when somebody
     * unlocks the phone to look at the app. Treating it as a dead key — which
     * this did — deleted a perfectly good passphrase and made the person type
     * it again, for the exact thing the vault exists to prevent.
     *
     * PERMANENT is a different list, and a short one: the key is gone (screen
     * lock removed, or the entry was never here), or the ciphertext does not
     * belong to the key (a restored backup on another device). Those are the
     * only cases where forgetting is right, and only they are forgotten.
     */
    fun load(): String? {
        val bg = prefs.contains(KEY_BLOB_BG)
        val got = loadFrom(if (bg) KEY_BLOB_BG else KEY_BLOB, if (bg) ALIAS_BG else ALIAS)
        // A copy sealed the other way than the setting says — an install
        // from before the setting existed, most often. It was just read, so
        // the phone is unlocked and this is the one moment it can be moved.
        if (got != null && bg != receiveWhileLocked()) save(got)
        return got
    }

    private fun loadFrom(blobKey: String, alias: String): String? {
        val raw = prefs.getString(blobKey, null) ?: return null
        return try {
            val blob = Base64.decode(raw, Base64.NO_WRAP)
            if (blob.size <= IV_LEN) throw AEADBadTagException("blob too short")
            val k = key(alias, create = false) ?: throw KeyPermanentlyInvalidatedException()
            val cipher = Cipher.getInstance(TRANSFORM)
            cipher.init(
                Cipher.DECRYPT_MODE, k,
                GCMParameterSpec(TAG_BITS, blob, 0, IV_LEN),
            )
            String(cipher.doFinal(blob, IV_LEN, blob.size - IV_LEN), Charsets.UTF_8)
        } catch (t: Throwable) {
            if (isPermanent(t)) {
                Log.w(TAG, "the stored passphrase can never be read again — forgetting it", t)
                forgetOne(blobKey, alias)
            } else {
                // Kept. The next launch, on an unlocked phone, will read it.
                Log.w(TAG, "the passphrase cannot be read right now; keeping it", t)
            }
            null
        }
    }

    /** Whether a failure means the value is unrecoverable rather than merely unavailable. */
    private fun isPermanent(t: Throwable): Boolean {
        var e: Throwable? = t
        while (e != null) {
            when (e) {
                // The key is gone: the screen lock was removed, or it was
                // never created on this device.
                is KeyPermanentlyInvalidatedException,
                is UnrecoverableKeyException,
                    -> return true
                // The ciphertext does not belong to this key — a blob carried
                // here from somewhere else. No amount of unlocking helps.
                is AEADBadTagException -> return true
            }
            e = e.cause
        }
        return false
    }

    /**
     * Remember a passphrase that HAS ALREADY OPENED THE CORE.
     *
     * Returns false when the platform would not keep it. A phone that cannot
     * hold the value must not be told it did: the caller keeps asking, which
     * is worse than one tap and much better than a promise that silently
     * expired.
     */
    fun save(passphrase: String): Boolean = try {
        val bg = receiveWhileLocked()
        val cipher = Cipher.getInstance(TRANSFORM)
        cipher.init(Cipher.ENCRYPT_MODE, key(if (bg) ALIAS_BG else ALIAS)!!)
        val body = cipher.doFinal(passphrase.toByteArray(Charsets.UTF_8))
        val blob = cipher.iv + body
        val ok = prefs.edit()
            .putString(if (bg) KEY_BLOB_BG else KEY_BLOB, Base64.encodeToString(blob, Base64.NO_WRAP))
            .commit()
        // ONE COPY, EVER. The other sealing is destroyed only once this one
        // is safely written — two copies under two rules would make the
        // setting a statement about one of them.
        if (ok) forgetOne(if (bg) KEY_BLOB else KEY_BLOB_BG, if (bg) ALIAS else ALIAS_BG)
        ok
    } catch (t: Throwable) {
        Log.w(TAG, "could not store the passphrase", t)
        false
    }

    /**
     * Stop remembering, and destroy the key rather than only the ciphertext.
     * Deleting one of the two would leave the other lying around for no
     * reason; deleting the key first means a crash between the two lines
     * leaves a blob nobody can read, which is the safe order.
     */
    fun forget() {
        forgetOne(KEY_BLOB, ALIAS)
        forgetOne(KEY_BLOB_BG, ALIAS_BG)
    }

    private fun forgetOne(blobKey: String, alias: String) {
        try {
            keyStore().deleteEntry(alias)
        } catch (t: Throwable) {
            Log.w(TAG, "could not delete the key", t)
        }
        prefs.edit().remove(blobKey).commit()
    }

    private fun keyStore(): KeyStore =
        KeyStore.getInstance(PROVIDER).apply { load(null) }

    /**
     * The existing key, or a new one when create is set.
     *
     * READING MUST NOT MINT ONE. A load that quietly created a fresh key
     * would then fail to decrypt the old blob and report that as permanent —
     * turning "the key is missing" into "forget the passphrase" on a path
     * that is supposed to be read-only.
     */
    private fun key(alias: String, create: Boolean = true): SecretKey? {
        val ks = keyStore()
        (ks.getEntry(alias, null) as? KeyStore.SecretKeyEntry)?.let { return it.secretKey }
        if (!create) return null

        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, PROVIDER)
        val spec = KeyGenParameterSpec.Builder(
            alias,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
            // NO USER AUTHENTICATION. Requiring it would raise a system prompt
            // on every launch, which is the thing this class exists to remove
            // — a biometric gate is a different feature with a different
            // decision behind it, not a default to slip in here.
            .apply {
                // Usable only while the device is unlocked, where the platform
                // can enforce it (28+). Free, and it means a locked phone in
                // somebody else's hands cannot open the node even through a
                // bug that reaches this code.
                //
                // The second key (ALIAS_BG) is the same key WITHOUT this line,
                // and nothing else differs — see [receiveWhileLocked] for the
                // decision behind it.
                if (alias == ALIAS && Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                    setUnlockedDeviceRequired(true)
                }
            }
            .build()
        gen.init(spec)
        return gen.generateKey()
    }

    private companion object {
        const val TAG = "quiet-vault"
        const val PREFS = "quiet-unlock"
        const val KEY_BLOB = "passphrase_v1"
        const val PROVIDER = "AndroidKeyStore"
        const val ALIAS = "quiet.passphrase.v1"
        const val KEY_BLOB_BG = "passphrase_bg_v1"
        const val ALIAS_BG = "quiet.passphrase.bg.v1"
        const val KEY_RECEIVE_LOCKED = "receive_while_locked"
        const val KEY_REQUIRE_CODE = "require_code_on_open"
        const val TRANSFORM = "AES/GCM/NoPadding"
        const val IV_LEN = 12
        const val TAG_BITS = 128
    }
}
