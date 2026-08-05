// AR-0's package-shaped host. Deliberately NOT part of any Go build and not
// referenced by the root module — see android/quietcore/go.mod for why the
// binding is nested, and android/README.md for what this rig is and is not.
pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "quiet-ar0-host"
include(":app")
