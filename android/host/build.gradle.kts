plugins {
    // AGP 8.2 predates Gradle 9 and only appeared to work on it: a Java-only
    // module never exercised the paths where the two disagree. The moment the
    // Kotlin plugin contributed to a compile classpath, AGP 8.2 resolved it
    // too early and the build died with "cannot mutate the dependencies of
    // configuration ... after the configuration was resolved". That is a
    // version mismatch reported at the wrong place, not a Kotlin problem —
    // so the version moves rather than a workaround being invented for it.
    id("com.android.application") version "8.13.0" apply false
    // AR-1b.2 — the host becomes Kotlin from here on, and nothing else about
    // the stack moves with it: no Compose, no Hilt, no Navigation component,
    // no native UI. The web UI the node already serves stays THE interface.
    // This module owns only the seams Android will not let anybody else own —
    // process lifecycle, permissions, notifications, services, deep links.
    id("org.jetbrains.kotlin.android") version "2.2.20" apply false
}
