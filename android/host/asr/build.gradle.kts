// SR-0 — the offline ASR module: whisper.cpp behind one JNI seam, in its
// OWN module so the app never links native code directly and the backend
// can be swapped (sherpa-onnx) without touching a caller. arm64-v8a only,
// like the rest of the app.
plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "space.quiet.asr"
    compileSdk = 35
    defaultConfig {
        minSdk = 24
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        ndk { abiFilters += "arm64-v8a" }
        externalNativeBuild {
            cmake {
                arguments += listOf(
                    // A debug APK must not mean debug INFERENCE: AGP's
                    // debug variant compiles CMake at -O0, and ggml at -O0
                    // turned a one-second utterance into two minutes of
                    // one core. The model runs optimized in every variant.
                    "-DCMAKE_BUILD_TYPE=Release",
                    "-DGGML_NATIVE=OFF",
                    // libomp from the NDK deadlock-spins on this device
                    // (1712 s of user CPU in a barrier, zero progress,
                    // single-thread included) — ggml's own pthread pool
                    // works. Measured, not assumed.
                    "-DGGML_OPENMP=OFF",
                    // GGML_NATIVE=OFF alone builds SCALAR kernels — a
                    // 1.2 s utterance took minutes. The dotprod+fp16
                    // arch flags are what the official whisper.android
                    // build uses; Cortex-A78/A55 (armv8.2) carries both.
                    "-DCMAKE_C_FLAGS=-march=armv8.2-a+dotprod+fp16",
                    "-DCMAKE_CXX_FLAGS=-march=armv8.2-a+dotprod+fp16",
                    "-DWHISPER_BUILD_TESTS=OFF",
                    "-DWHISPER_BUILD_EXAMPLES=OFF",
                    "-DBUILD_SHARED_LIBS=OFF",
                )
                cppFlags += "-std=c++17"
            }
        }
    }
    externalNativeBuild {
        cmake {
            path = file("CMakeLists.txt")
            version = "4.0.2"
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}

dependencies {
    androidTestImplementation("androidx.test.ext:junit:1.2.1")
    androidTestImplementation("androidx.test:runner:1.6.2")
}
