plugins {
    id("com.android.application")
}

android {
    namespace = "space.quiet.arprobe"
    compileSdk = 35

    defaultConfig {
        applicationId = "space.quiet.arprobe"
        minSdk = 24
        // API 30+ is what ApplicationExitInfo needs, and the exit reason is the
        // whole point of the low-memory lane. 24 stays the floor only because
        // the binding was built for it; every device AR-0 measures is 30+.
        targetSdk = 35
        versionCode = 1
        versionName = "ar0"
        ndk { abiFilters += "arm64-v8a" }
    }

    buildTypes {
        // DEBUG ONLY, and that is load-bearing rather than lazy: `run-as` needs
        // a debuggable package, and `run-as` is how the harness reads filesDir
        // without root — which is what keeps this working on the Play Store
        // system image and on a stock retail phone alike.
        getByName("debug") {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    packaging {
        jniLibs {
            // gomobile's libgojni.so is already the Go runtime; nothing here
            // should try to be clever about it.
            useLegacyPackaging = false
        }
    }
}

dependencies {
    implementation(files("libs/quietcore.aar"))
}
