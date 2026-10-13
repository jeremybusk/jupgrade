# jupgrade

A Go CLI for deterministic Java modernization in Maven and Gradle repositories.
Java 21 is the default; Java 17 and 25 are explicit targets. Built-in transformations
upgrade build configuration and selected common dependencies without running a JVM.
Cached Maven parent/BOM snapshots resolve external ownership. Optional pinned
OpenRewrite recipes perform type-aware source/configuration changes on an isolated
copy, then save exact edits for offline replay and recovery.

## Build and test

Requires Go 1.22 or later. Applying plans needs no JVM; verifying them requires
the selected target JDK and Maven/Gradle, or an executable repository wrapper.
Source-recipe planning also needs a JVM and cached OpenRewrite artifacts.

```sh
go build -buildvcs=false -o bin/jupgrade ./cmd/jupgrade
go test ./...
go vet ./...
```

The tool uses only the Go standard library. `-buildvcs=false` also allows building
from a source directory without usable Git metadata.

## Quick start and overrides

```sh
# Preview a Java/library upgrade without editing or running the repository.
bin/jupgrade upgrade --repo /path/to/service --java 21 --preset common \
  --dry-run --out /tmp/service-plan.json

# Or plan, apply, and build in one command, saving a recovery plan first.
bin/jupgrade upgrade --repo /path/to/service --java 21 --preset common \
  --out /tmp/service-upgrade.json --java-home /path/to/jdk-21

# Override individual versions without writing a policy file.
bin/jupgrade plan --repo /path/to/service --java 17 \
  --dependency junit:junit=4.13.2 \
  --dependency org.projectlombok:lombok=1.18.46 \
  --plugin org.apache.maven.plugins:maven-compiler-plugin=3.14.1

# Environment configuration works with the same commands.
export JUPGRADE_JAVA=21 JUPGRADE_PRESET=common
export JUPGRADE_DEPENDENCIES='junit:junit=4.13.2,org.projectlombok:lombok=1.18.46'
bin/jupgrade plan --repo /path/to/service --out /tmp/service-env-plan.json
```

`upgrade` requires a new `--out` file unless `--dry-run` is selected. It saves the
plan, refuses blockers, applies, then verifies. Build failures leave applied edits
available for diagnosis and print a rollback command. It never claims runtime
startup success. Plan output files are never overwritten.

Scalar CLI flags override environment values. Policy precedence is built-in
build/common preset, then a complete JSON policy, then environment and CLI version
rules. A supplied JSON policy replaces the preset maps entirely. `--java` or
`JUPGRADE_JAVA` overrides its Java field; it does not silently change its custom
plugin versions. Repeatable `--dependency`, `--plugin`, `--parent`, and `--gradle-plugin` rules merge
by coordinate: CLI rules override matching environment entries, preserving other
entries. The last repeated rule for a coordinate wins. Plans save the final resolved
policy, so later environment changes do not affect apply, verify, or rollback.

| CLI option | Environment variable |
| --- | --- |
| `--repo` | `JUPGRADE_REPO` |
| `--build` | `JUPGRADE_BUILD` (`auto`, `maven`, `gradle`) |
| `--java` | `JUPGRADE_JAVA` |
| `--preset` | `JUPGRADE_PRESET` |
| `--policy` | `JUPGRADE_POLICY` |
| `--dependency` | `JUPGRADE_DEPENDENCIES` (comma-separated `G:A=V`) |
| `--plugin` | `JUPGRADE_PLUGINS` (comma-separated `G:A=V`) |
| `--parent` | `JUPGRADE_PARENTS` (comma-separated `G:A=V`) |
| `--spring-boot` | `JUPGRADE_SPRING_BOOT` |
| `--maven` | `JUPGRADE_MAVEN` |
| `--maven-sha256` | `JUPGRADE_MAVEN_SHA256` |
| `--resolve-maven` | `JUPGRADE_RESOLVE_MAVEN` |
| `--gradle` / `--gradle-sha256` | `JUPGRADE_GRADLE` / `JUPGRADE_GRADLE_SHA256` |
| `--gradle-plugin` | `JUPGRADE_GRADLE_PLUGINS` (comma-separated `ID=V`) |
| `--recipe` | `JUPGRADE_RECIPES` (comma-separated aliases) |
| `--gradle-command` / `--gradle-user-home` | `JUPGRADE_GRADLE_COMMAND` / `JUPGRADE_GRADLE_USER_HOME` |
| `--out` / `--plan` | `JUPGRADE_OUT` / `JUPGRADE_PLAN` |
| `--java-home` | `JUPGRADE_JAVA_HOME` (otherwise `JAVA_HOME`/`PATH`) |
| `--maven-repo` | `JUPGRADE_MAVEN_REPO` |
| `--maven-settings` | `JUPGRADE_MAVEN_SETTINGS` |
| `inspect --model` | `JUPGRADE_MODEL` |
| `batch --manifest` | `JUPGRADE_BATCH_MANIFEST` |
| `--profiles` | `JUPGRADE_PROFILES` |
| `--offline` | `JUPGRADE_OFFLINE` (`1`/`0` or `true`/`false`) |
| `--timeout` | `JUPGRADE_TIMEOUT` (for example `20m`) |

`--offline=false` overrides an environment request for offline builds. Targets
and version rules must be pinned releases; `latest`, wildcards, snapshots, and
unsupported Java targets are rejected before writing a plan.

## Maven wrapper and Spring Boot targets

```sh
# Updates an existing standard HTTPS Maven 3 wrapper distribution URL.
bin/jupgrade plan --repo /path/to/service --maven 3.9.11 \
  --out /tmp/service-maven-plan.json

# Update existing Spring Boot parent, imported BOM, and Maven plugin declarations.
bin/jupgrade plan --repo /path/to/boot-service --spring-boot 3.5.10 \
  --out /tmp/service-boot-plan.json
```

`--maven` edits `.mvn/wrapper/maven-wrapper.properties`, preserves mirror hosts,
ZIP/tar.gz archive type, comments, escaped URL colons, and line endings, and retains
newer Maven 3 versions. It does not install system Maven or generate a wrapper.
Existing distribution checksums require the target archive's `--maven-sha256`;
checksums are never removed to make an upgrade pass. Supply the digest for the exact
archive type and version selected by the URL. Custom URL layouts, duplicate or
continued properties, and Maven 4 are blockers. Verification checks that Maven 3's
runtime version meets the saved target. Wrapper plans use schema 3; POM-only plans
remain schema 2. Wrapper script/JAR updates are not automated.

`--spring-boot` is explicit opt-in and has no moving default. It targets existing
`spring-boot-starter-parent`, `spring-boot-dependencies`, and
`spring-boot-maven-plugin` declarations together. Same-major bumps produce a
release-note/startup finding. Boot major changes require an explicit source recipe
such as `--recipe boot3.5` or `--recipe boot4.0`. The Boot Gradle plugin is also
selected by `--spring-boot`. Other managed dependencies still require local ownership or explicit
external model resolution; the tool never invents BOM member versions.

## Common library preset

`--preset common` adds the following rules to the build defaults. Only existing
artifacts are selected; new libraries are not introduced. Locally managed/property
versions are updated at their owners, and known newer releases are retained.

| Library family | Pin |
| --- | --- |
| JUnit 4 | 4.13.2 |
| JUnit Jupiter / JUnit BOM | 5.12.2 |
| JUnit Platform Commons / Engine / Launcher | 1.12.2 |
| Lombok | 1.18.46 |
| Commons Lang 3 | 3.20.0 |
| Commons IO | 2.20.0 |
| ASM / Commons / Tree / Analysis / Util | 9.7.1; 9.9.1 for Java 25 |

These are reviewed fixed choices, not a freshness or compatibility guarantee for
all applications. Framework-managed versionless declarations that cannot be
resolved locally produce blockers when selected. Use a narrower explicit version
rule or an organization-specific complete policy where needed. Jackson, Spring
Framework, logging binding changes, and JUnit 4-to-5 source migration are excluded
until framework-aware recipes and integration coverage exist.

Java 25 build defaults select JaCoCo 0.8.14; Java 17/21 retain 0.8.12. Known old
Lombok, ASM, and JaCoCo declarations are checked against target-specific compatibility
floors. Jetty's ASM overrides are checked together, and legacy Jetty without a
verified Java 25 parser is blocked. Java 25 annotation processing may also require
explicit compiler processor configuration; changing the release property does not
provide that configuration automatically.

Pin references: [Lombok changelog](https://projectlombok.org/changelog),
[ASM versions](https://asm.ow2.io/versions.html),
[JaCoCo Java support](https://www.jacoco.org/jacoco/trunk/doc/changes.html),
[Commons Lang releases](https://commons.apache.org/proper/commons-lang/changes.html),
[Commons IO releases](https://commons.apache.org/proper/commons-io/changes.html).

## Upgrade a repository

Work on an upgrade branch in a checked-out repository. The CLI works with existing
local edits and protects them with input hashes; it does not clone repositories,
create commits, or publish changes.

```sh
# Print the default policy; edit it to choose your internal dependency versions.
bin/jupgrade policy > /tmp/java21-policy.json

# Inspect declared/resolved versions and their owning POM/property.
bin/jupgrade inspect --repo /path/to/service

# Planning reads the repository and saves complete before/after POM content.
bin/jupgrade plan --repo /path/to/service \
  --policy /tmp/java21-policy.json --out /tmp/service-java21-plan.json

# Review the plan, including its changes and findings, before applying it.
bin/jupgrade apply --plan /tmp/service-java21-plan.json

# Run Maven clean verify on Java 21 and check that the migration has converged.
bin/jupgrade verify --plan /tmp/service-java21-plan.json \
  --java-home /path/to/jdk-21

# Review the Git diff and run application-specific smoke/integration tests.
# To restore the exact POM bytes saved in the plan:
bin/jupgrade rollback --plan /tmp/service-java21-plan.json
```

Omit `--policy` to use the selected built-in preset (default: build). Omit `--out` to print the plan as JSON
to stdout. Plans are created with mode `0600`, contain build and edited source content, and are
bound to the absolute repository path. Wrapper plans also contain the wrapper properties. Existing output files are never overwritten.
Save plans outside your repository, or in an ignored `.jupgrade` directory you create.

`verify` supports `--profiles staging,integration`, `--offline`, `--maven-repo DIR`,
and `--timeout 20m`. Run it for each profile set relevant to your application.
It supplies `maven.test.skip=false`, `skipTests=false`, and `skipITs=false`, but
custom test plugins or application tests outside the Maven lifecycle still need
their own verification. Build success and idempotence do not establish runtime
behavioral equivalence.

## Policies

`inspect` produces JSON with dependencies, managed dependencies, plugins, parents,
and annotation processors. Each record includes the declaring POM, resolved version,
owning POM/property, profile ID where applicable, and any resolution problem.
Maven inspection follows local/captured parent chains and BOM imports and honors
dependency type/classifier. Use `inspect --resolve-maven --maven-repo DIR` for cached
external models. Gradle inspection currently lists literal dependency strings in
build scripts; it is not a complete effective dependency graph. Inspection does not
invoke a build or query remote repositories.

The policy is a complete JSON object, not an overlay on the defaults. Generate it
with `jupgrade policy`, then add entries such as:

```json
"dependencies": {
  "junit:junit": "4.13.2"
}
```

The fields are `java` (`17`, `21`, or `25`), `plugins`, `dependencies`, and
`parents`, plus optional `maven` and `maven_sha256` wrapper targets. Each version map uses `groupId:artifactId` keys and exact release
versions. A parent entry changes a matching declared parent version; it does not
perform the corresponding framework source/configuration migration.

The default build policy selects conservative Java 21 build-plugin versions:

| Plugin | Version |
| --- | --- |
| Maven Compiler | 3.13.0 |
| Maven Surefire / Failsafe | 3.5.6 |
| Maven Clean | 3.4.1 |
| Maven Resources | 3.3.1 |
| Maven JAR | 3.4.2 |
| Maven WAR | 3.4.0 |
| JaCoCo | 0.8.12 |
| Jetty Maven Plugin (legacy coordinate) | 9.4.58.v20250814 |

These are explicit policy choices, not a claim that they are the latest releases.
The Jetty pin applies to an existing `org.eclipse.jetty:jetty-maven-plugin`
declaration; it does not introduce Jetty into other projects. It updates legacy
Jetty within its 9.4 family and preserves the existing `javax` API stack. Newer
numeric/datestamped Jetty versions are retained. A migration to Jetty 12 and its
EE-specific coordinates needs a separate recipe. Jetty 9.4 is an EOL family;
this compatibility rule does not constitute a migration to a supported server line.
Compiler, Surefire, Clean, Resources, and the appropriate JAR/WAR lifecycle plugin
are activated locally when absent. Other plugins are updated when declared.
Visible newer numeric versions, including inherited plugin versions established
by local or captured external models, are retained. Without a captured model,
external parent plugin defaults remain unresolved and locally added pins are
reported for review. Review your organization's parent POM policy before using
the defaults across a fleet.

Dependency rules update existing versioned declarations, including
`dependencyManagement`, BOM imports, plugin dependencies, and compiler
`annotationProcessorPaths`. They do not add new libraries or individually override
versionless managed dependencies. Local managed versions are updated in their owning
parent/BOM; the consuming dependency stays versionless.

When every known POM consumer of a version property is selected for the same target,
the property is updated once in its owning POM. This preserves expressions such as
`${lombok.version}` across dependencies and annotation processors. Property aliases
and local parent chains are resolved. If a property has unrelated consumers or
conflicting targets, it stays intact and selected versioned declarations receive
individual pins. Profile-local properties currently receive declaration-local pins.

Unresolved requested upgrades are blockers. In particular, a requested versionless
dependency controlled by an external BOM/parent no longer silently remains unchanged.
Upgrade the owning BOM/parent with a policy rule, or supply a local management
declaration. Captured external BOM contents are resolved; inherited parent-profile overrides,
compound expressions, and general Maven-specific qualified-version ordering still
require further resolution or review. Merely requesting a BOM version does not establish its member versions.
Jetty's `9.4.x.vYYYYMMDD` releases are ordered by numeric release and build date;
this exception is scoped to Jetty coordinates.

## Legacy Jetty and Java 21 startup

Java 21 classes use class-file major version 65. Jetty 9.4.48 bundles ASM 9.3,
which cannot scan that bytecode during `jetty:run`. Maven compilation and tests
can still pass; Jetty may open a connector while the webapp context is unavailable.
The default plugin upgrade selects Jetty 9.4.58, whose published parent POM pins
ASM 9.7.1. Source packages and Servlet/WebSocket API dependencies stay unchanged.

Regenerate a custom policy from `jupgrade policy`, or add
`org.eclipse.jetty:jetty-maven-plugin` with version `9.4.58.v20250814` to its
`plugins` map. A policy that leaves the known old scanner in place now produces
a blocker. An explicit plugin dependency on an old ASM can override the upgraded
Jetty parser; that is also reported as a blocker. Such overrides should be reviewed
and updated together, rather than adding ASM to application dependencies.

After applying the plan, start the app and check the application URL:

```sh
cd /path/to/service
mvn clean jetty:run
# From another terminal, require a healthy application response:
curl --fail http://localhost:8080/demo/
```

`verify` executes `clean verify`; it does not start `jetty:run` or prove that
an application is serving requests. Runtime startup is a separate required check.
The Jetty plugin must be downloaded when it is not already in your Maven cache.

References: [Jetty 9.4.58 dependency versions](https://raw.githubusercontent.com/jetty/jetty.project/jetty-9.4.58.v20250814/pom.xml),
[ASM's Java 21 support](https://asm.ow2.io/versions.html),
[Jetty compatibility and support status](https://jetty.org/docs/jetty/12.1/index.html).

## Repeatability and recovery

- Plans contain pinned policy versions, input hashes, and exact before/after bytes.
- Planning follows declared reactor modules, including modules in Maven profiles,
  and matching local parent POMs inside the repository.
- Comments, namespaces, existing whitespace, and line endings are preserved outside
  edited scalar values and new elements.
- Apply checks all inventoried POMs, Java sources, and selected build/CI files before
  writing. Changed or newly discovered inputs require a new plan.
- Applying an already-applied plan is a no-op. An interrupted run accepts either
  the original or planned content in each file and resumes the remaining edits.
- Each file is written atomically. A multi-file upgrade is resumable rather than a
  single filesystem transaction.
- Rollback restores exact originals and refuses to overwrite edits made since
  planning/applying. It also requires the original source/input inventory.
- Repository paths that escape the root and symlinked module paths are rejected.
- A `.jupgrade.lock` prevents competing CLI operations. If a process is killed,
  remove its stale lock only after checking that it is no longer running.
- Verification requires the build tool's runtime JDK to match the saved Java target,
  runs Maven `clean verify` or Gradle `clean check`, checks for source/input changes,
  and requires zero further edits. Gradle verification disables automatic toolchain
  downloads and reruns tasks.
- Native Maven POM-only plans use schema 2; wrapper plans use schema 3. Captured
  Maven models, Gradle, and source edits use schema 4; recipe creates/deletes use
  schema 5. All validate all transformations together, including
  edits to owning properties and dependent modules. Regenerate old plans when the
  new ownership rules change their intended output.

## Cached Maven parents and BOMs

```sh
# Capture the original AND selected target model versions from a populated cache.
bin/jupgrade resolve --repo /path/to/service --preset common \
  --maven-repo /path/to/.m2/repository --out /tmp/service-policy.json
bin/jupgrade plan --repo /path/to/service --policy /tmp/service-policy.json \
  --out /tmp/service-plan.json

# Or resolve and plan together.
bin/jupgrade plan --repo /path/to/service --resolve-maven --preset common \
  --maven-repo /path/to/.m2/repository --out /tmp/service-plan.json
```

Resolution embeds each required POM and its SHA-256 in the policy/plan. It follows
external parents and imported BOMs recursively, preserves versionless consumers,
and never edits the cache. Missing models fail explicitly; populate the cache with
Maven before resolving. Replay uses the saved models even if the cache changes.
This is bounded static model resolution: Maven settings, active profile evaluation,
compound property interpolation and arbitrary effective-model behavior are not
reproduced. Ambiguous requested ownership blocks rather than inventing a version.

## Effective build discovery

```sh
bin/jupgrade discover --repo /path/to/service --profiles production \
  --maven-settings /path/to/settings.xml --out /tmp/service-model.json
bin/jupgrade inspect --repo /path/to/service --model /tmp/service-model.json

# Evaluate a dynamic Gradle build using its original supported runtime/JDK.
bin/jupgrade discover --repo /path/to/gradle-service \
  --gradle-command /path/to/gradle --out /tmp/gradle-model.json
```

Discovery executes build configuration on an isolated copy twice and requires
identical normalized results and unchanged inputs. Maven uses pinned Help Plugin
3.5.1 to capture effective reactor POMs with active profiles/settings; inactive
profile declarations are excluded from the report. Gradle captures evaluated
project directories, custom build files and resolved external dependencies for
every resolvable configuration, including dynamic includes and version expressions.
Unresolved dependencies, escaping project directories and composite builds fail.

Snapshots record the runtime tool/JDK, selected profiles, settings-file hashes and
repository input hashes. Settings contents and arbitrary project properties are
not exported. Inspection of a snapshot needs no build execution and rejects
changed inputs/settings. Save snapshots outside the repo or in `.jupgrade`.
`--maven-settings` also applies to recipe workers and build verification.

Discovery is read-only evidence. It does not invent writable declaration owners in
opaque scripts or automatically make native planning support arbitrary DSLs.
Use its resolved versions to choose explicit rules/recipes; model-driven edits to
dynamic declarations remain future work. Maven effective configuration is not a
complete transitive dependency graph. Gradle discovery still needs its original
DSL to evaluate with the selected runtime. The Gradle CI matrix exercises model
capture after migration; a separate Maven job exercises effective BOM management.

## Gradle Java builds

```sh
bin/jupgrade plan --repo /path/to/gradle-service --java 21 --preset common \
  --gradle 8.14.3 --gradle-plugin org.springframework.boot=3.5.16 \
  --out /tmp/gradle-plan.json
bin/jupgrade verify --plan /tmp/gradle-plan.json --java-home /path/to/jdk-21
```

Supports literal Groovy and Kotlin DSL Java builds, declared subprojects, Java
toolchains and source/target/release pins, literal dependency strings/map notation,
`gradle.properties` references, plugin versions, and the default
`gradle/libs.versions.toml` catalog. Shared catalog versions stay shared when all
consumers agree; otherwise only the selected declaration receives a pin. Selected
versionless dependencies require ownership evidence instead of a guessed override.

For Gradle 9, top-level legacy `sourceCompatibility` and `targetCompatibility`
declarations move to the Java extension (`java.sourceCompatibility` and
`java.targetCompatibility`). Existing Java extension blocks and compile-task
properties retain their scopes. Gradle removed the old project convention
properties in 9.0; see the [Gradle migration guide](https://docs.gradle.org/current/userguide/upgrading_version_8.html).

Saved defaults are Gradle 8.14.3 for Java 17/21 and 9.1.0 for Java 25. Existing
standard HTTPS `gradle-X-bin.zip`/`all.zip` wrapper URLs are upgraded while preserving
mirrors, archive type and checksums. A changed checksummed distribution needs
`--gradle-sha256`. Wrapper scripts/JARs are not generated or upgraded. Verification
uses `gradlew`, installed Gradle, or `--gradle-command`, and checks the saved minimum
Gradle version and exact runtime Java major.

Dynamic includes, composite builds, custom project directories, applied scripts,
rich catalog versions, multiline/escaped build strings and unresolved target
expressions block. Kotlin DSL support does not include Kotlin-language/compiler
migration. Mixed Maven/Gradle roots require `--build maven` or `--build gradle`;
auto mode refuses to guess. The saved policy retains the selection. Boot/Gradle
compatibility is checked; Boot 3 with the Java 25/Gradle 9 defaults blocks.

## Pinned source recipes

```sh
bin/jupgrade recipes
bin/jupgrade plan --repo /path/to/service --recipe string-format --java 21 \
  --offline --maven-repo /path/to/.m2/repository --out /tmp/source-plan.json

# An explicit framework family and exact release must agree.
bin/jupgrade plan --repo /path/to/boot-service --recipe boot3.5 --recipe java21 \
  --spring-boot 3.5.16 --resolve-maven --java 21 \
  --maven-repo /path/to/.m2/repository --out /tmp/boot-plan.json
```

Aliases: `string-format`, `base64`, `java17`, `java21`, `java25`, `boot3.5`, and
`boot4.0`. Java aliases require the matching Java target; Boot aliases require a
matching explicit Boot target. The engine is pinned to Maven plugin 6.46.1 or
Gradle plugin 7.41.0, with `rewrite-migrate-java:3.42.1` and
`rewrite-spring:6.37.1`. `recipes` prints full recipe names and coordinates.

Recipe planning executes repository build configuration on a temporary copy using
the chosen JVM. Cache the pinned engine/recipe JARs and their dependencies first;
`--offline` uses that cache. Two worker runs must converge before native build
upgrades are merged. Failures, reported parser errors, unsupported paths and
nonconvergence produce no original repository edits. Recipes can edit, create,
delete and rename supported source/resource/build files. Renames are recorded as
creation/deletion pairs; destinations are written before sources are removed.
Schema 5 records absent file states, created/deleted file permissions and new
directories. Rollback restores exact bytes and permissions and removes only the
recorded empty directories. Conflicting files, symlinks and changed context block
before writes. Existing-file permission changes are rejected. Maven explicit
version pins are checked against numeric downgrades and unresolved pin removal.
Gradle source migrations need additional effective-model validation for such pins.

The plan records artifact/cache digests, context hashes, and exact before/after
bytes. Apply, verify convergence and rollback replay these bytes without executing
OpenRewrite again. New or changed context requires a fresh plan. Keep plan files
trusted and outside the repository or in `.jupgrade`; hashes provide integrity and
reproducibility checks, not authentication or a sandbox for build scripts.

## Current scope and next steps

Heuristic findings cover removed Java EE/Base64 APIs, internal JDK access,
reflective accessibility and selected runtime/CI pins. Scans are not an exhaustive
Java migration analysis. Native Maven toolchains, alternate compiler executables,
arbitrary compiler arguments, runtime images and CI upgrades still require review.
Recipe and build success do not prove application startup or behavioral equivalence.

Effective-model discovery, lifecycle recovery, pinned corpus tooling, readable
diffs, batch reports and shell completion are implemented. Remaining work is
model-driven editing of opaque declarations and the first network-enabled runs
of the expanded real-project corpus. Expand by project shape rather than clone
count, and keep application-specific startup and behavioral tests.

References: [Gradle compatibility](https://docs.gradle.org/current/userguide/compatibility.html),
[Boot 3.5 requirements](https://docs.spring.io/spring-boot/3.5/system-requirements.html),
[Maven effective POM](https://maven.apache.org/plugins/maven-help-plugin/effective-pom-mojo.html),
[OpenRewrite Maven plugin](https://docs.openrewrite.org/reference/rewrite-maven-plugin).

## Real Maven integration test

The integration test copies `testdata/java8` to a temporary repository, updates
JUnit Jupiter 5.11.4 to 5.12.2, upgrades the build, runs a real JUnit test through Maven,
checks for Java 21 bytecode (class-file version 65), repeats the migration, and
rolls it back. It is opt-in because Maven may resolve dependencies from your
configured artifact repositories.

A second fixture, `testdata/reactor`, exercises a real parent/child Maven build.
It upgrades parent-owned JUnit and Lombok version properties, preserves the child's
versionless dependencies and inherited annotation processor expression, compiles
Lombok-generated accessors on Java 21, runs JUnit tests, checks idempotence, and
restores the exact original files. Its test policy pins Lombok to 1.18.46.

```sh
JUPGRADE_INTEGRATION=1 go test -v ./internal/upgrade \
  -run 'Test(Java8To21MavenIntegration|ReactorInheritanceAndAnnotationProcessorIntegration)$' -count=1
```

Optional environment variables: `JUPGRADE_JAVA_HOME`, `JUPGRADE_MAVEN_REPO`, and
`JUPGRADE_OFFLINE=1` for a fully populated local cache.

## Jetty scanner regression

The opt-in scanner test executes Jetty 9.4.48 against a Java 21 annotated servlet
and WebSocket fixture. ASM 9.3 must reproduce `Unsupported class file major version
65`; changing only ASM to 9.7 must make the context available and an in-memory HTTP
request return 200. It uses Jetty's `LocalConnector`, so it needs no socket access.

```sh
JUPGRADE_JETTY_PARSER_INTEGRATION=1 go test -v ./internal/upgrade \
  -run TestJettyJava21AnnotationScanningIntegration -count=1
```

This test requires cached Jetty 9.4.48 jars, ASM/ASM Commons/ASM Tree 9.3 and 9.7,
Servlet API 3.1.0, WebSocket API 1.1, Annotation API 1.3.2, and SLF4J API 1.7.36.
It isolates the scanner failure; it does not execute the Jetty 9.4.58 Maven plugin.
Run the application's own startup check after downloading the upgraded plugin.

## Testing a repository corpus

The corpus runner copies each supplied repository to a temporary directory and
checks deterministic/no-write planning, apply/reapply, zero-edit replanning, and
exact rollback. It checks that the original repositories are unchanged. Add
`--verify` to also run Maven or Gradle using the configured target JDK and artifact cache.
Blocked and failed repositories return a nonzero exit code and JSON findings;
passing the default run proves transformation/recovery, not compilation or startup.
It requires Python 3 and a built CLI.

```sh
go build -buildvcs=false -o bin/jupgrade ./cmd/jupgrade
python3 scripts/test-corpus.py testdata/java8 testdata/reactor \
  testdata/gradle-groovy testdata/gradle-kotlin /src/jupgrade/testrepos/NewsFeedSite --java 21 --preset common

JUPGRADE_MAVEN_REPO=/tmp/jupgrade-m2 JUPGRADE_OFFLINE=1 \
  python3 scripts/test-corpus.py testdata/java8 testdata/reactor --verify
```

Java 17/21/25 plan, apply, verification preflight, and rollback paths have automated
coverage. The real Maven fixtures and Jetty scanner test require Java 21; Java 17/25
build and runtime smoke tests still need dedicated JDKs and a CI matrix before
claiming equivalent end-to-end coverage.

The new opt-in tests exercise a cached external JUnit BOM, a real type-aware
`String.format` rewrite followed by Java 21 compilation, and a Gradle Java build:

```sh
JUPGRADE_INTEGRATION=1 JUPGRADE_OFFLINE=1 \
  go test -v ./internal/upgrade -run TestExternalJUnitBOMMavenIntegration -count=1
JUPGRADE_SOURCE_INTEGRATION=1 \
  go test -v ./internal/upgrade -run TestTypeAwareRecipeMavenIntegration -count=1
JUPGRADE_BOOT_SOURCE_INTEGRATION=1 \
  go test -v ./internal/upgrade -run TestBootMajorSourceAndContextMavenIntegration -count=1
JUPGRADE_GRADLE_INTEGRATION=1 JUPGRADE_GRADLE_COMMAND=/path/to/gradle \
  go test -v ./internal/upgrade -run TestGradleBuildMatrixIntegration -count=1
```

Gradle needs local socket access even without a long-lived daemon. Environments
that forbid sockets cannot run its real build test. The source integration test
proves the API recipe path. `testdata/boot-context` exercises a real Boot 3.5.16 to
4.0.8 migration with a non-web context startup test, Java 21 bytecode and rollback.
Larger Boot applications still need corpus and application-specific startup coverage
before broad unattended framework migrations.

Additional corpus paths exercise saved Maven models and source receipts:

```sh
python3 scripts/test-corpus.py testdata/maven-bom --resolve-maven --verify
python3 scripts/test-corpus.py testdata/source-recipe --recipe string-format --verify
python3 scripts/test-corpus.py testdata/boot-context --preset build \
  --recipe boot4.0 --recipe java21 --spring-boot 4.0.8 --resolve-maven --verify
```

Set the artifact-cache/JDK environment variables above as needed. The runner
replans with the captured policy, so source replay and model resolution do not rely
on changing external state. Originals are never modified.

## Gradle validation matrix

[Validate](.github/workflows/validate.yml) runs Go race tests and vet, followed by
independent real Gradle builds on Temurin Java 17, 21 and 25. Java 17/21 use Gradle
8.14.3; Java 25 uses 9.1.0. Each job exercises both Groovy and Kotlin/catalog
fixtures with an installed Gradle and an actual generated wrapper. Tests require
identical plans, successful JUnit execution, the requested bytecode major,
convergence, reapply and exact rollback, including the wrapper upgrade. Build logs
are kept as workflow artifacts. A failing matrix entry fails validation.

Wrapper generation runs in a separate minimal build and copies the generated
infrastructure into the unchanged fixture. This lets the target Gradle generate
its wrapper before jupgrade migrates syntax that the target runtime has removed.

To run one matrix entry locally with the matching JDK and Gradle installed:

```sh
JUPGRADE_GRADLE_INTEGRATION=1 JUPGRADE_TEST_JAVA=21 \
  JUPGRADE_JAVA_HOME=/path/to/jdk-21 \
  JUPGRADE_GRADLE_COMMAND=/path/to/gradle-8.14.3/bin/gradle \
  JUPGRADE_TEST_REPORTS=/tmp/jupgrade-gradle-reports \
  go test -v -count=1 -timeout=20m ./internal/upgrade \
    -run '^TestGradleBuildMatrixIntegration$'
```

Use `JUPGRADE_OFFLINE=1` only after caching the distribution and all fixture
dependencies. A successful default `go test ./...` skips these real builds; it
does not substitute for a passing matrix. The workflow runs on pushes, pull
requests and manual dispatch. The initial matrix passed according to the repository
owner; the added effective-model tests still need a new CI run. Local socket
restrictions prevent real Gradle execution in this workspace.

## Pinned real-project corpus

[corpus.json](scripts/corpus.json) pins complete commit SHAs for NewsFeedSite,
Commons Lang 3.17.0 and Petclinic on Boot 3.4.2. Petclinic has separate Maven and
Gradle cases selected using `--build`. Both descriptors remain intact in the
disposable copy and the source checkout. Manifests can explicitly project other
build shapes with `exclude`; reports record any such projection.
Gradle wrapper digests come from [Gradle's published checksums](https://gradle.org/release-checksums/).
Revision references: [Commons Lang](https://github.com/apache/commons-lang/commit/29ccc7665f3bc5d84155a3092ab2209a053324e6),
[Petclinic](https://github.com/spring-projects/spring-petclinic/commit/1f89996e1ff53eb0d3990bbb68e382b118b1b556),
[NewsFeedSite](https://github.com/Azure-Samples/NewsFeedSite/commit/76a406c34455c524ebe07d84768cd37400a5208c).

```sh
python3 scripts/fetch-corpus.py --dest /src/jupgrade/testrepos/pinned
python3 scripts/test-corpus.py --manifest scripts/corpus.json \
  --checkout-dir /src/jupgrade/testrepos/pinned --case petclinic-maven \
  --warm-cache --verify --startup --timeout 1200

# Reuse an existing local repository when network access is unavailable.
python3 scripts/fetch-corpus.py --dest /src/jupgrade/testrepos/pinned \
  --case news-feed --local-source news-feed=/src/jupgrade/testrepos/NewsFeedSite
```

The fetcher checks out the exact revision and refuses dirty or mismatched existing
checkouts. The runner can also fetch with `--fetch`. `--warm-cache` evaluates the
original build and resolves selected target parents/BOMs using pinned Maven Help
Plugin 3.5.1 before planning. It needs network access unless artifacts are already
cached. Source recipe artifacts still need preloading when explicit recipes are
selected. Standalone Git metadata is copied for build plugins that inspect it.

`--startup` requires `--verify`. Application cases must keep the launched process
alive and return the expected HTTP status/body on localhost before startup is
recorded as successful. Boot cases execute their packaged JAR directly; Gradle
packaging runs `bootJar` first. Process groups are stopped before rollback. The
runner checks input bytes and permissions after verification/startup and confirms
original checkouts stayed unchanged, including failure paths. Application smoke
checks do not establish full behavioral equivalence. Libraries report startup as
not applicable. JSON explicitly records `build_verified` and `startup_verified`.

[Real project corpus](.github/workflows/corpus.yml) provides manual CI runs with
download, build, HTTP startup, convergence and rollback. These expanded external
cases are pending their first network-enabled runs; the local NewsFeedSite build
and recovery case passes, while local HTTP startup is prevented by socket limits.
The scripts require Python 3.9+ and a Unix host for process-group cleanup.

## Plan review, batch upgrades and completion

```sh
bin/jupgrade diff --plan /tmp/service-plan.json
bin/jupgrade diff --plan /tmp/service-plan.json --format summary

bin/jupgrade batch --manifest /path/to/repositories.json \
  --out /tmp/modernization-batch --java 21 --preset common --dry-run
# Review each saved plan, then apply/verify using the captured policy.
bin/jupgrade batch --manifest /path/to/repositories.json \
  --out /tmp/modernization-batch --resume

# Once jupgrade is on PATH, enable completion for the current shell.
source <(jupgrade completion bash)
# Also available: jupgrade completion zsh / fish
```

Diffs use standard Git-compatible unified patches with context, create/delete
metadata and missing-newline markers. `--format summary` lists operations;
`--format json` prints the captured plan. Review validates saved hashes without
requiring the original checkout to still exist. Apply and verify retain live
inventory validation. Large generated files use a bounded-memory replacement
diff. Core rollback preserves exact Unix permissions; Git patches express Git's
regular/executable file modes.

A batch manifest uses schema 1:

```json
{
  "schema": 1,
  "repositories": [
    {"name": "orders", "repo": "../orders"},
    {"name": "billing", "repo": "../billing"}
  ]
}
```

Relative paths are resolved against the manifest file. Names and canonical roots
must be unique. A new output directory stores `report.json`, per-repository
recovery plans and logs. Place it outside all inputs or under `.jupgrade`.
Batch uses the same policy/env/CLI defaults as upgrade; normal execution plans,
applies and verifies each repository. Dry run saves plans without applying.
Recipe planning still runs its isolated workers when explicitly selected.

Resume retains the original policy and Maven-resolution setting, including for
unplanned repositories. It reuses saved recovery plans and refuses stale inputs.
Previously verified repositories avoid redundant builds when their verification
configuration is unchanged; changing JVM/cache/settings/profile options triggers
verification again. Failed builds keep their applied edits and recovery plans.
Other repositories continue and reports identify the failing stage. Roll back a
repository with its saved plan, or use a new batch directory/policy to resolve
blockers. Cross-repository upgrades are not one filesystem transaction.

Completion for Bash, Zsh and Fish uses the same flag registry as command parsing.
Bash completion handles target/recipe values and repository paths containing
spaces. `jupgrade help COMMAND` shows the complete options for any command. The
CLI still uses the Go standard library; these commands do not add dependencies.
