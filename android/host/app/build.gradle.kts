import java.io.File
import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "space.quiet.arprobe"
    compileSdk = 35

    defaultConfig {
        // THE NAME EVERY PHONE WILL REMEMBER IT BY. Changed from the AR probe's
        // own id before the first build left this machine: an applicationId is
        // the identity Android keys the install, the data directory and every
        // future update on, so changing it after somebody has the app gives
        // them a SECOND app and abandons the node inside the first.
        //
        // The Kotlin namespace below is deliberately NOT renamed with it. That
        // is where the classes live, it is invisible to anybody holding the
        // phone, and moving several thousand lines between packages to make
        // two strings match is churn with a real chance of breaking the
        // binding's javapkg seam.
        applicationId = "quite.space"
        minSdk = 24
        // API 30+ is what ApplicationExitInfo needs, and the exit reason is the
        // whole point of the low-memory lane. 24 stays the floor only because
        // the binding was built for it; every device AR-0 measures is 30+.
        targetSdk = 35
        // BUMP versionCode FOR EVERY BUILD THAT LEAVES THIS MACHINE. Android
        // refuses to install an apk whose code is not higher than the one on
        // the phone, and the failure reads as a corrupt download.
        //
        // A tagged release overrides both from the tag, because the release
        // workflow is exactly the case where "leaves this machine" is
        // certain and remembering to bump by hand is exactly what nobody
        // does. The code it passes is derived arithmetically from the
        // version (major*10000 + minor*100 + patch), which is monotonic for
        // as long as tags are — and 0.1.0 gives 100, comfortably past the
        // hand-bumped 8 these local builds have reached.
        versionCode = (project.findProperty("quietVersionCode") as String?)?.toInt() ?: 10
        versionName = (project.findProperty("quietVersionName") as String?) ?: "0.1.4"
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

    // THE SIGNING KEY IS NOT IN THIS REPOSITORY, and it never will be. It
    // lives wherever its owner keeps it, and Gradle is told where by a
    // git-ignored properties file or by the environment — so a clone of this
    // project builds, tests and runs without one, and only a RELEASE build
    // asks:
    //
    //   android/host/keystore.properties
    //     storeFile=/absolute/path/quiet-release.jks
    //     storePassword=…
    //     keyAlias=quiet
    //     keyPassword=…
    //
    //   or QUIET_KEYSTORE / QUIET_KEYSTORE_PASSWORD / QUIET_KEY_ALIAS /
    //   QUIET_KEY_PASSWORD, for a machine that has no business holding a file.
    //
    // Make the key yourself; nothing here will do it for you:
    //   keytool -genkeypair -v -keystore quiet-release.jks -alias quiet \
    //     -keyalg RSA -keysize 4096 -validity 10000
    //
    // AN APK SIGNED BY A DIFFERENT KEY IS A DIFFERENT APP to Android: it
    // cannot update an existing install, and the data directory of the old
    // one is unreachable — every tester would reinstall from scratch and lose
    // their node. That is why this is a documented seam rather than a
    // generated convenience.
    signingConfigs {
        create("release") {
            val props = Properties()
            val file = rootProject.file("keystore.properties")
            if (file.exists()) {
                file.inputStream().use { props.load(it) }
            }
            fun value(key: String, env: String): String? =
                props.getProperty(key) ?: System.getenv(env)

            val store = value("storeFile", "QUIET_KEYSTORE")
            if (!store.isNullOrEmpty()) {
                storeFile = File(store)
                storePassword = value("storePassword", "QUIET_KEYSTORE_PASSWORD")
                keyAlias = value("keyAlias", "QUIET_KEY_ALIAS")
                keyPassword = value("keyPassword", "QUIET_KEY_PASSWORD")
            }
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

        // THE BETA BUILD. No rig and no contender: src/debug is not compiled
        // into it, so the measurement surfaces are absent by construction
        // rather than by a flag somebody could flip.
        getByName("release") {
            // Signed only when a key was actually found. Without one the build
            // still succeeds and produces an UNSIGNED apk, which cannot be
            // installed and says so — better than quietly shipping something
            // signed by a debug key that no update can ever follow.
            val key = signingConfigs.getByName("release")
            signingConfig = if (key.storeFile != null) key else null

            // MINIFICATION OFF for the first beta, deliberately. The core is
            // reached through a gomobile binding whose Java stubs are called
            // by name; shrinking that surface is its own piece of work with
            // its own way of failing — silently, at runtime, on somebody
            // else's phone — and it buys a few megabytes.
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
