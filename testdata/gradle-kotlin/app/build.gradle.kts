plugins { java }
repositories { mavenCentral() }
java { toolchain { languageVersion = JavaLanguageVersion.of(17) } }
dependencies { testImplementation(libs.junit) }
