# jupgrade

A Go CLI for deterministic Java build upgrades in Maven repositories. Java 21 is
the default target; Java 17 and 25 can be selected explicitly.
It resolves local parent inheritance and dependency management, then changes Maven
configuration and explicitly selected dependency versions. It scans for several Java migration risks, executes Maven on the selected JDK,
and checks that a second migration would make no further edits. It does not yet
rewrite Java APIs or perform framework migrations.

OpenRewrite is not required. A type-aware Java transformation engine can be added
later behind the Go CLI when the supported migrations need source changes.

## Build and test

Requires Go 1.22 or later. Applying plans needs no JVM; verifying them requires
the selected target JDK and Maven, or an executable repository Maven wrapper.

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
plugin versions. Repeatable `--dependency`, `--plugin`, and `--parent` rules merge
by coordinate: CLI rules override matching environment entries, preserving other
entries. The last repeated rule for a coordinate wins. Plans save the final resolved
policy, so later environment changes do not affect apply, verify, or rollback.

| CLI option | Environment variable |
| --- | --- |
| `--repo` | `JUPGRADE_REPO` |
| `--java` | `JUPGRADE_JAVA` |
| `--preset` | `JUPGRADE_PRESET` |
| `--policy` | `JUPGRADE_POLICY` |
| `--dependency` | `JUPGRADE_DEPENDENCIES` (comma-separated `G:A=V`) |
| `--plugin` | `JUPGRADE_PLUGINS` (comma-separated `G:A=V`) |
| `--parent` | `JUPGRADE_PARENTS` (comma-separated `G:A=V`) |
| `--spring-boot` | `JUPGRADE_SPRING_BOOT` |
| `--maven` | `JUPGRADE_MAVEN` |
| `--maven-sha256` | `JUPGRADE_MAVEN_SHA256` |
| `--out` / `--plan` | `JUPGRADE_OUT` / `JUPGRADE_PLAN` |
| `--java-home` | `JUPGRADE_JAVA_HOME` (otherwise `JAVA_HOME`/`PATH`) |
| `--maven-repo` | `JUPGRADE_MAVEN_REPO` |
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
release-note/startup finding. Boot major changes are blockers because the CLI does
not yet perform framework source/configuration migrations such as `javax` to
`jakarta`. Other managed dependencies still require local ownership or explicit
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
to stdout. Plans are created with mode `0600`, contain source POM content, and are
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
It follows local parent chains and local BOM imports and honors dependency type and
classifier when locating management. It does not invoke Maven or query repositories.

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
Visible newer numeric versions, including locally inherited plugin versions, are retained. External parent POM plugin versions
are not resolved during planning; newly added local pins can override them and
are reported for review. Review your organization's parent POM policy before using
the default pins across a fleet.

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
declaration. External BOM contents, inherited parent-profile overrides, compound
expressions, and general Maven-specific qualified-version ordering require further resolution
or review. Merely requesting a BOM version does not establish its member versions.
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
- Verification requires Maven's runtime JDK to match the saved Java target, runs
  `clean verify`, checks for source/input changes, and requires zero further edits.
- POM-only plans use schema 2; wrapper plans use schema 3. Both validate all transformations together, including
  edits to owning properties and dependent modules. Regenerate old plans when the
  new ownership rules change their intended output.

## Current scope and next steps

The CLI reports findings for removed Java EE APIs, old internal Base64 APIs,
internal JDK access, reflective accessibility, and selected runtime/CI pins. This
is a heuristic scan that can flag comments and does not exhaustively detect Java
8-to-21 incompatibilities. Toolchains, alternate compiler executables, and targets
above the chosen Java version are blockers. Runtime images, CI configuration,
Gradle projects, arbitrary compiler arguments, full Maven effective-model resolution,
external BOM resolution, and framework/API rewrites are not automated yet. Root
Gradle Groovy/Kotlin and mixed Maven/Gradle builds receive explicit unsupported-build
errors instead of being treated as Maven projects.

The priorities for expanding coverage are:

1. Effective Maven model discovery from a supplied/cached snapshot: resolve external
   parent/BOM ownership while recording every resolved input in the plan.
2. Gradle Kotlin/Groovy support, starting with wrappers, toolchains, version catalogs,
   and a worker that understands Gradle's model. Java 25 runtime support starts with
   Gradle 9.1, while Boot 3.5 documents Gradle 7/8 build support: choosing versions
   independently can create an unsupported combination.
3. Type-aware source/configuration recipes for Boot major upgrades and common
   removed APIs, using OpenRewrite as a JVM worker where useful.
4. Pinned external test repositories representing Maven reactors, Boot parent/BOM
   applications, Gradle Groovy/Kotlin/version catalogs, and small runtime services.
   For each, require deterministic planning, build/tests, application startup,
   convergence, and exact rollback. Expand by project shape instead of clone count.
5. Human-readable plan diffs, recipe discovery, and resumable batch reports. Add
   Cobra/completion when those command surfaces justify another dependency.

[Gradle compatibility](https://docs.gradle.org/current/userguide/compatibility.html),
[Boot 3.5 requirements](https://docs.spring.io/spring-boot/3.5/system-requirements.html).

Useful references:
[Maven compiler release configuration](https://maven.apache.org/plugins/maven-compiler-plugin/examples/set-compiler-release.html),
[Maven effective POM](https://maven.apache.org/plugins/maven-help-plugin/effective-pom-mojo.html),
[OpenRewrite Java 21 migration](https://docs.openrewrite.org/recipes/java/migrate/upgradetojava21).

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
`--verify` to also run Maven using the configured target JDK and artifact cache.
Blocked and failed repositories return a nonzero exit code and JSON findings;
passing the default run proves transformation/recovery, not compilation or startup.
It requires Python 3 and a built CLI.

```sh
go build -buildvcs=false -o bin/jupgrade ./cmd/jupgrade
python3 scripts/test-corpus.py testdata/java8 testdata/reactor \
  /src/jupgrade/testrepos/NewsFeedSite --java 21 --preset common

JUPGRADE_MAVEN_REPO=/tmp/jupgrade-m2 JUPGRADE_OFFLINE=1 \
  python3 scripts/test-corpus.py testdata/java8 testdata/reactor --verify
```

Java 17/21/25 plan, apply, verification preflight, and rollback paths have automated
coverage. The real Maven fixtures and Jetty scanner test require Java 21; Java 17/25
build and runtime smoke tests still need dedicated JDKs and a CI matrix before
claiming equivalent end-to-end coverage.
