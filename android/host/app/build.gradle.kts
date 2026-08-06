plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
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

        // AR-1b.6b.5. The structural notification tests are INSTRUMENTED
        // rather than JVM: what has to be asserted is what Android actually
        // holds — active notifications, their extras, and the shortcut
        // surfaces — and that only exists on a device. They need no relay and
        // no network: b.5 proved delivery, and this proves the projection of
        // an already-accepted state into system surfaces.
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    testOptions {
        unitTests {
            // android.util.Log throws "not mocked" by default, which turns a
            // test of a REFUSAL — the path that logs why it refused — into a
            // test of the logger. Returning defaults keeps the assertion on
            // the behaviour under test.
            isReturnDefaultValues = true
        }
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

    // AR-0's MEASUREMENT RIG LIVES IN THE DEBUG SOURCE SET, and the separation
    // is the point rather than tidiness. RigActivity and ContenderActivity are
    // an intent-driven control surface and a second process that deliberately
    // fights for the data-dir lock: correct for a gate, and things no shipped
    // application may carry. They stay JAVA and stay byte-for-byte what AR-0
    // measured — a frozen artifact is not improved by being ported.
    //
    // Everything under src/main is the product host, in Kotlin, and cannot
    // reach the rig: a release variant simply does not compile it.
    sourceSets {
        getByName("main") {
            java.srcDirs("src/main/kotlin")
        }
        getByName("debug") {
            java.srcDirs("src/debug/java")
            manifest.srcFile("src/debug/AndroidManifest.xml")
        }
        getByName("test") {
            java.srcDirs("src/test/kotlin")
        }
        getByName("androidTest") {
            java.srcDirs("src/androidTest/kotlin")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlin {
        compilerOptions {
            jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
        }
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

    // AR-1b.3. ONE AndroidX dependency, for one thing: registerForActivityResult
    // is the supported way to request a runtime permission, and the deprecated
    // onRequestPermissionsResult path would have to be replaced anyway. This is
    // not the door opening for Compose, Hilt or Navigation — the interface
    // stays the web UI the node already serves.
    implementation("androidx.activity:activity:1.9.3")

    // AR-1b.2. Plain JUnit on the JVM, not instrumentation: the notification
    // decisions are ordinary Java by design — no Android type appears in
    // NotificationCoordinator — so they run in milliseconds on any machine
    // rather than needing a booted device to answer whether a swipe clears
    // the dedup.
    testImplementation("junit:junit:4.13.2")

    androidTestImplementation("androidx.test.ext:junit:1.2.1")
    androidTestImplementation("androidx.test:runner:1.6.2")
    androidTestImplementation("androidx.test:rules:1.6.1")
}
