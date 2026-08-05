package space.quiet.arprobe;

import android.app.Activity;
import android.app.ActivityManager;
import android.app.ApplicationExitInfo;
import android.content.Context;
import android.content.Intent;
import android.os.Build;
import android.os.Bundle;
import android.os.Process;
import android.os.SystemClock;
import android.util.Log;
import android.widget.TextView;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.File;
import java.io.FileOutputStream;
import java.nio.charset.StandardCharsets;
import java.util.List;

import space.quiet.quietcore.Quietcore;

/**
 * AR-0's control surface: start | stop | status, and nothing else.
 *
 * <p>This is a MEASUREMENT RIG, not the beginning of an Android app. It has no
 * product interface — the web UI AR-0d looks at is the one the node already
 * embeds, reached over the ordinary local HTTP API, which is ADR-011's boundary
 * and the same seam every other client uses.
 *
 * <p>HOW THE HARNESS TALKS TO IT. Commands arrive as intents:
 *
 * <pre>
 *   adb shell am start -n space.quiet.arprobe/.RigActivity \
 *       --es cmd start --es pass P --ei seq 7
 * </pre>
 *
 * and the answer is written to {@code filesDir/rig-out.json}, read back with
 * {@code adb shell run-as space.quiet.arprobe cat files/rig-out.json}. The
 * answer carries the {@code seq} it was asked with, so the harness polls for
 * ITS OWN answer rather than racing a previous one — logcat scraping would be
 * exactly that race, dressed up.
 *
 * <p>Work runs off the UI thread because {@code node.Open} is scrypt plus a
 * full log replay, and blocking the main thread would earn an ANR in the
 * middle of the measurement it was taking.
 */
public class RigActivity extends Activity {

    private static final String TAG = "quiet-ar0";
    private TextView view;

    @Override
    protected void onCreate(Bundle saved) {
        super.onCreate(saved);
        view = new TextView(this);
        view.setPadding(32, 96, 32, 32);
        view.setText("Quiet AR-0 rig\npid " + Process.myPid());
        setContentView(view);
        handle(getIntent());
    }

    /**
     * singleTask means a second `am start` lands here rather than in a second
     * process. That is the whole reason for the launch mode: the rig's claims
     * are about ONE process, and two would make core_pid meaningless.
     */
    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        handle(intent);
    }

    private void handle(final Intent intent) {
        if (intent == null) return;
        final String cmd = intent.getStringExtra("cmd");
        if (cmd == null) return;
        final int seq = intent.getIntExtra("seq", 0);
        final String pass = intent.getStringExtra("pass");
        final String name = intent.getStringExtra("name");
        final boolean lan = intent.getBooleanExtra("lan", false);

        new Thread(new Runnable() {
            @Override public void run() { execute(cmd, seq, pass, name, lan); }
        }, "ar0-cmd").start();
    }

    private void execute(String cmd, int seq, String pass, String name, boolean lan) {
        JSONObject out = new JSONObject();
        try {
            out.put("seq", seq);
            out.put("cmd", cmd);
            out.put("ok", true);

            File dir = dataDir();
            switch (cmd) {
                case "start":
                    Quietcore.start(dir.getAbsolutePath(),
                            pass == null ? "" : pass,
                            name == null ? "me" : name,
                            lan);
                    break;
                case "stop":
                    Quietcore.stop();
                    break;
                case "status":
                    break;
                default:
                    out.put("ok", false);
                    out.put("error", "unknown cmd: " + cmd);
            }
        } catch (Throwable t) {
            // A failure is a RESULT here, not an exception to swallow: "the
            // core would not start under an app UID" is one of AR-0's named
            // gate failures and has to reach the harness as data.
            try {
                out.put("ok", false);
                out.put("error", String.valueOf(t.getMessage()));
                out.put("error_class", t.getClass().getName());
            } catch (Exception ignored) { }
            Log.w(TAG, "cmd " + cmd + " failed", t);
        }
        // The status goes in on EVERY path, including the failing one.
        // It did not, at first, and a `start` against an already-running core
        // therefore answered with no `core` key at all — so the harness could
        // not read the pid or the fingerprint of the node that was running
        // perfectly well. A command failing must never cost the reader the
        // state of the thing the command was about.
        try {
            out.put("core", new JSONObject(Quietcore.status()));
        } catch (Exception e) {
            Log.w(TAG, "status failed", e);
        }
        try {
            addHostFacts(out);
        } catch (Exception e) {
            Log.w(TAG, "host facts failed", e);
        }
        write(out);
        post(out);
    }

    /** The data directory is the real one — app-private internal storage. */
    private File dataDir() {
        File dir = new File(getFilesDir(), "node");
        //noinspection ResultOfMethodCallIgnored
        dir.mkdirs();
        return dir;
    }

    /**
     * The half of the status contract only the framework can answer: the app
     * UID this is all running under, the real filesDir, and the exit reasons.
     *
     * <p>Exit reasons are reported as a LIST with pid and timestamp on each,
     * not as a single "last reason". After a series of KILL / force-stop / LMK
     * events a single field would get attributed to the wrong operation, and
     * the low-memory lane's most important row would be quietly wrong.
     */
    private void addHostFacts(JSONObject out) throws Exception {
        out.put("package", getPackageName());
        out.put("uid", Process.myUid());
        out.put("host_pid", Process.myPid());
        out.put("files_dir", getFilesDir().getAbsolutePath());
        out.put("external_files_dir", String.valueOf(getExternalFilesDir(null)));

        // The framework's two clocks, for the same reason the core reports its
        // two: elapsedRealtime COUNTS deep sleep, uptimeMillis does NOT, and
        // their difference across a Doze is the suspended time. Sampled here as
        // well as in the core so a discrepancy between the Java and native
        // views is visible rather than assumed away.
        out.put("elapsed_realtime_ms", SystemClock.elapsedRealtime());
        out.put("uptime_ms", SystemClock.uptimeMillis());
        out.put("wall_clock_ms", System.currentTimeMillis()); // correlation ONLY

        // The process's own memory, so the low-memory lane can separate the
        // ART/JNI host's baseline from what an operation adds on top. VmHWM is
        // the lifetime peak and is the only one that catches a transient.
        readProcStatus(out);

        ActivityManager am = (ActivityManager) getSystemService(Context.ACTIVITY_SERVICE);
        if (am != null) {
            // What the framework will actually let this app have, which is the
            // budget the 128 MiB quicklink KDF runs inside — an 8 GB device can
            // hand an app far less.
            out.put("memory_class_mb", am.getMemoryClass());
            out.put("large_memory_class_mb", am.getLargeMemoryClass());

            ActivityManager.MemoryInfo mi = new ActivityManager.MemoryInfo();
            am.getMemoryInfo(mi);
            out.put("system_avail_mem", mi.availMem);
            out.put("system_total_mem", mi.totalMem);
            out.put("system_low_memory", mi.lowMemory);
        }

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R && am != null) {
            JSONArray exits = new JSONArray();
            List<ApplicationExitInfo> list =
                    am.getHistoricalProcessExitReasons(getPackageName(), 0, 10);
            for (ApplicationExitInfo e : list) {
                JSONObject j = new JSONObject();
                j.put("pid", e.getPid());
                j.put("reason", e.getReason());
                j.put("reason_name", reasonName(e.getReason()));
                j.put("status", e.getStatus());
                j.put("importance", e.getImportance());
                j.put("pss_kb", e.getPss());
                j.put("rss_kb", e.getRss());
                j.put("timestamp", e.getTimestamp());
                j.put("description", String.valueOf(e.getDescription()));
                j.put("process_name", String.valueOf(e.getProcessName()));
                exits.put(j);
            }
            out.put("exit_reasons", exits);
        } else {
            out.put("exit_reasons_unavailable", "API " + Build.VERSION.SDK_INT + " < 30");
        }
    }

    /**
     * Names the reason rather than leaving a bare integer in the report. A
     * device that cannot attribute an LMK kill honestly reports SIGNALED
     * instead of LOW_MEMORY — recording WHICH of the two arrived is the point,
     * so the low-memory lane never claims more than it saw.
     */
    private static String reasonName(int r) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) return "unknown";
        switch (r) {
            case ApplicationExitInfo.REASON_UNKNOWN: return "UNKNOWN";
            case ApplicationExitInfo.REASON_EXIT_SELF: return "EXIT_SELF";
            case ApplicationExitInfo.REASON_SIGNALED: return "SIGNALED";
            case ApplicationExitInfo.REASON_LOW_MEMORY: return "LOW_MEMORY";
            case ApplicationExitInfo.REASON_CRASH: return "CRASH";
            case ApplicationExitInfo.REASON_CRASH_NATIVE: return "CRASH_NATIVE";
            case ApplicationExitInfo.REASON_ANR: return "ANR";
            case ApplicationExitInfo.REASON_INITIALIZATION_FAILURE: return "INIT_FAILURE";
            case ApplicationExitInfo.REASON_PERMISSION_CHANGE: return "PERMISSION_CHANGE";
            case ApplicationExitInfo.REASON_EXCESSIVE_RESOURCE_USAGE: return "EXCESSIVE_RESOURCE";
            case ApplicationExitInfo.REASON_USER_REQUESTED: return "USER_REQUESTED";
            case ApplicationExitInfo.REASON_USER_STOPPED: return "USER_STOPPED";
            case ApplicationExitInfo.REASON_DEPENDENCY_DIED: return "DEPENDENCY_DIED";
            case ApplicationExitInfo.REASON_OTHER: return "OTHER";
            // The three below appeared as bare "code_14/15/16" in the first
            // phone run — 16 was this rig's own reinstall. An unnamed code in
            // the exit column is the one place a reader will guess, and the
            // freezer one especially matters: AR-0c's background gate has to
            // tell "Android froze it" from "Android killed it".
            case ApplicationExitInfo.REASON_FREEZER: return "FREEZER";
            case ApplicationExitInfo.REASON_PACKAGE_STATE_CHANGE: return "PACKAGE_STATE_CHANGE";
            case ApplicationExitInfo.REASON_PACKAGE_UPDATED: return "PACKAGE_UPDATED";
            default: return "code_" + r;
        }
    }

    /**
     * VmHWM (lifetime peak RSS) and VmRSS (settled) for this process. Read
     * from procfs rather than from Debug.getMemoryInfo because the peak is the
     * figure that matters and only procfs keeps it: a 128 MiB spike lasting a
     * second is invisible to anything sampled afterwards.
     */
    private void readProcStatus(JSONObject out) {
        try (java.io.BufferedReader r = new java.io.BufferedReader(
                new java.io.FileReader("/proc/self/status"))) {
            String line;
            while ((line = r.readLine()) != null) {
                if (line.startsWith("VmHWM:")) out.put("vm_hwm_kb", kbOf(line));
                else if (line.startsWith("VmRSS:")) out.put("vm_rss_kb", kbOf(line));
            }
        } catch (Exception e) {
            Log.w(TAG, "procfs unreadable", e);
        }
    }

    private static long kbOf(String line) {
        String[] f = line.trim().split("\\s+");
        try {
            return f.length >= 2 ? Long.parseLong(f[1]) : 0;
        } catch (NumberFormatException e) {
            return 0;
        }
    }

    /** Written tmp-then-rename, so the harness can never read half an answer. */
    private void write(JSONObject out) {
        File dst = new File(getFilesDir(), "rig-out.json");
        File tmp = new File(getFilesDir(), "rig-out.json.tmp");
        try (FileOutputStream f = new FileOutputStream(tmp)) {
            f.write(out.toString().getBytes(StandardCharsets.UTF_8));
            f.getFD().sync();
        } catch (Exception e) {
            Log.e(TAG, "write failed", e);
            return;
        }
        if (!tmp.renameTo(dst)) Log.e(TAG, "rename failed");
    }

    private void post(final JSONObject out) {
        runOnUiThread(new Runnable() {
            @Override public void run() {
                if (view != null) view.setText("Quiet AR-0 rig\npid " + Process.myPid()
                        + "\n\n" + out.toString());
            }
        });
    }
}
