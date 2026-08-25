// SR-0 — the one JNI seam over whisper.cpp: init, one BATCH transcription
// of a bounded utterance, a cancel that actually aborts inference, free.
// No audio ever touches storage here; the PCM arrives as a short[] and
// leaves as a jstring.
#include <jni.h>
#include <android/log.h>
#include <atomic>
#include <string>
#include <vector>
#include "whisper.h"

namespace {
struct Ctx {
    whisper_context *wc = nullptr;
    std::atomic_bool cancel{false};
};
}

extern "C" JNIEXPORT jlong JNICALL
Java_space_quiet_asr_WhisperAsr_nativeInit(JNIEnv *env, jclass, jstring modelPath) {
    const char *path = env->GetStringUTFChars(modelPath, nullptr);
    whisper_context_params p = whisper_context_default_params();
    p.use_gpu = false;
    whisper_context *wc = whisper_init_from_file_with_params(path, p);
    env->ReleaseStringUTFChars(modelPath, path);
    if (!wc) return 0;
    auto *c = new Ctx();
    c->wc = wc;
    return reinterpret_cast<jlong>(c);
}

extern "C" JNIEXPORT void JNICALL
Java_space_quiet_asr_WhisperAsr_nativeCancel(JNIEnv *, jclass, jlong h) {
    if (h) reinterpret_cast<Ctx *>(h)->cancel.store(true);
}

extern "C" JNIEXPORT void JNICALL
Java_space_quiet_asr_WhisperAsr_nativeFree(JNIEnv *, jclass, jlong h) {
    if (!h) return;
    auto *c = reinterpret_cast<Ctx *>(h);
    whisper_free(c->wc);
    delete c;
}

extern "C" JNIEXPORT jstring JNICALL
Java_space_quiet_asr_WhisperAsr_nativeTranscribe(
    JNIEnv *env, jclass, jlong h, jshortArray pcm, jstring lang, jint threads) {
    if (!h) return nullptr;
    auto *c = reinterpret_cast<Ctx *>(h);
    c->cancel.store(false);

    jsize n = env->GetArrayLength(pcm);
    std::vector<float> f(n);
    jshort *raw = env->GetShortArrayElements(pcm, nullptr);
    for (jsize i = 0; i < n; i++) f[i] = raw[i] / 32768.0f;
    env->ReleaseShortArrayElements(pcm, raw, JNI_ABORT);

    whisper_full_params p = whisper_full_default_params(WHISPER_SAMPLING_GREEDY);
    p.n_threads = threads > 0 ? threads : 4;
    p.translate = false;
    p.no_timestamps = true;
    p.print_progress = false;
    p.print_realtime = false;
    p.print_special = false;
    const char *l = lang ? env->GetStringUTFChars(lang, nullptr) : nullptr;
    p.language = (l && l[0]) ? l : "auto";
    p.abort_callback = [](void *ud) -> bool {
        return reinterpret_cast<Ctx *>(ud)->cancel.load();
    };
    p.abort_callback_user_data = c;

    int rc = whisper_full(c->wc, p, f.data(), (int)f.size());
    if (l) env->ReleaseStringUTFChars(lang, l);
    if (rc != 0 || c->cancel.load()) return nullptr;

    std::string out;
    int segs = whisper_full_n_segments(c->wc);
    for (int i = 0; i < segs; i++) {
        const char *t = whisper_full_get_segment_text(c->wc, i);
        if (t) out += t;
    }
    return env->NewStringUTF(out.c_str());
}
