package space.quiet.arprobe;

import android.app.Activity;
import android.os.Bundle;
import android.os.Process;
import android.util.Log;

import org.json.JSONObject;

import java.io.File;
import java.io.FileOutputStream;
import java.nio.charset.StandardCharsets;

import space.quiet.quietcore.Quietcore;

/**
 * The second process, and it exists for exactly one assertion.
 *
 * <p>"Two processes cannot open one data directory" is the observable form of
 * `flock` working under an app UID, and it cannot be made by calling start()
 * twice: the Go side would refuse on its own in-process mutex long before the
 * lock was consulted, and the test would pass without ever reaching the thing
 * it claims to test. So this runs in {@code android:process=":contender"} — a
 * genuinely separate process, same UID, same filesDir — and tries to open the
 * directory the main process already holds.
 *
 * <p>PASSING LOOKS LIKE FAILING HERE: the expected outcome is an error, and it
 * must be the NAMED one about the directory already being open. A different
 * error means the lock was never reached; success means it does not hold on
 * this volume, which is one of AR-0's gate failures.
 */
public class ContenderActivity extends Activity {

    private static final String TAG = "quiet-ar0-contender";

    @Override
    protected void onCreate(Bundle saved) {
        super.onCreate(saved);
        final String pass = getIntent().getStringExtra("pass");
        final int seq = getIntent().getIntExtra("seq", 0);

        new Thread(new Runnable() {
            @Override public void run() { attempt(seq, pass); }
        }, "ar0-contender").start();

        finish();
    }

    private void attempt(int seq, String pass) {
        JSONObject out = new JSONObject();
        try {
            out.put("seq", seq);
            out.put("pid", Process.myPid());
            out.put("uid", Process.myUid());
            File dir = new File(getFilesDir(), "node");
            try {
                Quietcore.start(dir.getAbsolutePath(), pass == null ? "" : pass, "contender", false);
                // Reaching here is the FAILURE case: a second process opened a
                // data directory the first one holds.
                out.put("opened", true);
                out.put("verdict", "LOCK DID NOT HOLD");
                Quietcore.stop();
            } catch (Throwable t) {
                out.put("opened", false);
                out.put("error", String.valueOf(t.getMessage()));
                out.put("verdict", "refused");
            }
        } catch (Exception e) {
            Log.e(TAG, "contender failed", e);
        }
        File dst = new File(getFilesDir(), "rig-contender.json");
        File tmp = new File(getFilesDir(), "rig-contender.json.tmp");
        try (FileOutputStream f = new FileOutputStream(tmp)) {
            f.write(out.toString().getBytes(StandardCharsets.UTF_8));
            f.getFD().sync();
        } catch (Exception e) {
            Log.e(TAG, "write failed", e);
            return;
        }
        if (!tmp.renameTo(dst)) Log.e(TAG, "rename failed");
    }
}
