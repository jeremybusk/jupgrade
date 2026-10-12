# jupgrade

A Go CLI for repeatable Java 21 build upgrades in internal Maven repositories.
It resolves local parent inheritance and dependency management, then changes Maven
configuration and explicitly selected dependency versions. It scans for several Java migration risks, executes Maven on Java 21,
and checks that a second migration would make no further edits. It does not yet
rewrite Java APIs or perform framework migrations.

OpenRewrite is not required. A type-aware Java transformation engine can be added
later behind the Go CLI when the supported migrations need source changes.

## Build and test

Requires Go 1.22 or later. Applying plans needs no JVM; verifying them requires
Java 21 and Maven, or an executable repository Maven wrapper.

```sh
go build -buildvcs=false -o bin/jupgrade ./cmd/jupgrade
go test ./...
go vet ./...
```

The tool uses only the Go standard library. `-buildvcs=false` also allows building
from a source directory without usable Git metadata.

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

Omit `--policy` to use the built-in policy. Omit `--out` to print the plan as JSON
to stdout. Plans are created with mode `0600`, contain source POM content, and are
bound to the absolute repository path. Existing output files are never overwritten.
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

The fields are `java` (currently must be `21`), `plugins`, `dependencies`, and
`parents`. Each version map uses `groupId:artifactId` keys and exact release
versions. A parent entry changes a matching declared parent version; it does not
perform the corresponding framework source/configuration migration.

The default policy selects conservative Java 21 build-plugin versions:

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
- Verification requires Maven's runtime JDK to be exactly Java 21, runs
  `clean verify`, checks for source/input changes, and requires zero further edits.
- New plans use schema 2 and validate all POM transformations together, including
  edits to owning properties and dependent modules. Regenerate old plans when the
  new ownership rules change their intended output.

## Current scope and next steps

The CLI reports findings for removed Java EE APIs, old internal Base64 APIs,
internal JDK access, reflective accessibility, and selected runtime/CI pins. This
is a heuristic scan that can flag comments and does not exhaustively detect Java
8-to-21 incompatibilities. Toolchains, alternate compiler executables, and newer
Java targets are blockers. Runtime images, Maven wrappers, CI configuration,
Gradle projects, arbitrary compiler arguments, full Maven effective-model resolution,
external BOM resolution, and framework/API rewrites are not automated yet.

The next useful extension is Maven effective-model discovery for external parents,
profiles, settings, and remote BOMs, followed by a tested set of compatible library
versions for a defined internal application stack. Add source recipes where that stack requires them.
For broad type-aware transformations, OpenRewrite is a possible JVM worker;
ordinary targeted POM upgrades can stay in Go.

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
