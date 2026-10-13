package upgrade

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestGradle9MigratesProjectCompatibilityProperties(t *testing.T) {
	for _, java := range []int{17, 21, 25} {
		for _, current := range []string{"VERSION_1_8", "VERSION_" + strconv.Itoa(java)} {
			t.Run(strconv.Itoa(java)+"/"+current, func(t *testing.T) {
				root := t.TempDir()
				before := "plugins { id 'java' }\r\n// sourceCompatibility must move to the Java extension\r\nsourceCompatibility = JavaVersion." + current + "\r\ntargetCompatibility = JavaVersion." + current + "\r\n"
				writeTestFile(t, root, "build.gradle", before)
				policy, _ := PolicyForJava(java)
				policy.Gradle = "9.1.0"
				p := testPlan(t, root, policy)
				if p.HasBlockers() {
					t.Fatal(p.Findings)
				}
				if err := Apply(p, false); err != nil {
					t.Fatal(err)
				}
				after := readTestFile(t, root, "build.gradle")
				for _, property := range []string{"sourceCompatibility", "targetCompatibility"} {
					if !strings.Contains(after, "\r\njava."+property+" = JavaVersion.VERSION_"+strconv.Itoa(java)+"\r\n") {
						t.Fatal(after)
					}
				}
				if !strings.Contains(after, "// sourceCompatibility must move to the Java extension\r\n") || strings.Contains(strings.ReplaceAll(after, "\r\n", ""), "\n") {
					t.Fatal("lost comment or CRLF", after)
				}
				if next := testPlan(t, root, p.Policy); len(next.Files) != 0 || next.HasBlockers() {
					t.Fatal("Gradle 9 migration did not converge", next.Findings)
				}
				if err := Apply(p, true); err != nil {
					t.Fatal(err)
				}
				if readTestFile(t, root, "build.gradle") != before {
					t.Fatal("rollback did not restore legacy project properties")
				}
			})
		}
	}
}

func TestGradle9PreservesJavaExtensionAndTaskPropertyScopes(t *testing.T) {
	root := t.TempDir()
	before := `plugins { id 'java' }
java.sourceCompatibility = JavaVersion.VERSION_25
java.targetCompatibility = JavaVersion.VERSION_25
java { sourceCompatibility = JavaVersion.VERSION_25 }
tasks.withType(JavaCompile).configureEach {
    sourceCompatibility = '25'
    targetCompatibility = '25'
}`
	writeTestFile(t, root, "build.gradle", before)
	policy, _ := PolicyForJava(25)
	p := testPlan(t, root, policy)
	if len(p.Files) != 0 || p.HasBlockers() {
		t.Fatal("changed supported extension/task properties", p.Files, p.Findings)
	}
}

func TestGradle9MigratesLiteralPropertySetterNotation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "build.gradle", "plugins { id 'java' }\nsourceCompatibility '1.8'\ntargetCompatibility 8\n")
	policy, _ := PolicyForJava(25)
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	after := readTestFile(t, root, "build.gradle")
	if !strings.Contains(after, "java.sourceCompatibility = '25'") || !strings.Contains(after, "java.targetCompatibility = 25") {
		t.Fatal(after)
	}
	if next := testPlan(t, root, p.Policy); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal("literal setter migration did not converge", next.Findings)
	}
}

func TestGradleGroovyAndKotlinPlanApplyRollback(t *testing.T) {
	for _, kotlin := range []bool{false, true} {
		name, plugin, dependency := "build.gradle", "id 'java'", "testImplementation 'junit:junit:4.12'"
		if kotlin {
			name, plugin, dependency = "build.gradle.kts", "java", "testImplementation(\"junit:junit:4.12\")"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			before := "// keep junit:junit:4.12 unchanged here\r\nplugins { " + plugin + " }\r\njava { sourceCompatibility = JavaVersion.VERSION_1_8; targetCompatibility = JavaVersion.VERSION_1_8 }\r\ndependencies { " + dependency + " }\r\n"
			writeTestFile(t, root, name, before)
			policy := DefaultPolicy()
			_ = policy.AddPreset("common")
			p := testPlan(t, root, policy)
			if p.HasBlockers() {
				t.Fatal(p.Findings)
			}
			if p.Build != "gradle" || p.Policy.Gradle != "8.14.3" {
				t.Fatal(p)
			}
			planPath := filepath.Join(t.TempDir(), "plan.json")
			if err := WritePlan(planPath, p); err != nil {
				t.Fatal(err)
			}
			p, err := ReadPlan(planPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := Apply(p, false); err != nil {
				t.Fatal(err)
			}
			after := readTestFile(t, root, name)
			if !strings.Contains(after, "VERSION_21") || !strings.Contains(after, "junit:junit:4.13.2") || !strings.Contains(after, "// keep junit:junit:4.12 unchanged here") {
				t.Fatal(after)
			}
			if strings.Contains(strings.ReplaceAll(after, "\r\n", ""), "\n") {
				t.Fatal("lost CRLF")
			}
			if next := testPlan(t, root, p.Policy); len(next.Files) != 0 || next.HasBlockers() {
				t.Fatal(next)
			}
			if err := Apply(p, false); err != nil {
				t.Fatal(err)
			}
			if err := Apply(p, true); err != nil {
				t.Fatal(err)
			}
			if readTestFile(t, root, name) != before {
				t.Fatal("rollback did not restore build")
			}
		})
	}
}

func TestGradleModulesToolchainsAndProperties(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "settings.gradle.kts", `rootProject.name = "test"
include(":app", ":nested:lib")`)
	writeTestFile(t, root, "build.gradle.kts", `plugins { id("org.springframework.boot") version "3.4.13" apply false }`)
	writeTestFile(t, root, "app/build.gradle.kts", `plugins { java }
java { toolchain { languageVersion = JavaLanguageVersion.of(17) } }
tasks.withType<JavaCompile>().configureEach { options.release.set(17) }
dependencies { testImplementation("junit:junit:$junitVersion") }`)
	writeTestFile(t, root, "nested/lib/build.gradle", `plugins { id 'java-library' }`)
	writeTestFile(t, root, "gradle.properties", "junitVersion=4.12\n")
	policy := DefaultPolicy()
	policy.Dependencies["junit:junit"] = "4.13.2"
	policy.GradlePlugins = map[string]string{"org.springframework.boot": "3.5.16"}
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"app/build.gradle.kts", "nested/lib/build.gradle"} {
		if !strings.Contains(readTestFile(t, root, path), "JavaLanguageVersion.of(21)") {
			t.Fatal(path)
		}
	}
	if !strings.Contains(readTestFile(t, root, "app/build.gradle.kts"), "options.release.set(21)") {
		t.Fatal("compiler release left old")
	}
	if !strings.Contains(readTestFile(t, root, "build.gradle.kts"), `version "3.5.16"`) {
		t.Fatal("Boot plugin not upgraded")
	}
	if next := testPlan(t, root, p.Policy); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal(next)
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
}

func TestGradleCatalogSharedOwnershipAndPreservedReferences(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "build.gradle.kts", `plugins { java; alias(libs.plugins.boot) }
dependencies { testImplementation(libs.junit); implementation(libs.commons) }`)
	before := `# keep catalog comments
[versions]
junit = "4.12"
shared = "1.0"
boot = "3.4.13"
[libraries]
junit = { module = "junit:junit", version.ref = "junit" }
chosen = { group = "internal", name = "chosen", version.ref = "shared" }
other = { module = "internal:other", version.ref = "shared" }
commons = "commons-io:commons-io:2.22.0"
[plugins]
boot = { id = "org.springframework.boot", version.ref = "boot" }
`
	writeTestFile(t, root, "gradle/libs.versions.toml", before)
	policy := DefaultPolicy()
	_ = policy.AddPreset("common")
	policy.Dependencies["internal:chosen"] = "2.0"
	policy.GradlePlugins = map[string]string{"org.springframework.boot": "3.5.16"}
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	after := readTestFile(t, root, "gradle/libs.versions.toml")
	for _, want := range []string{`junit = "4.13.2"`, `version.ref = "junit"`, `shared = "1.0"`, `name = "chosen", version = "2.0"`, `module = "internal:other", version.ref = "shared"`, `boot = "3.5.16"`, `commons = "commons-io:commons-io:2.22.0"`} {
		if !strings.Contains(after, want) {
			t.Fatalf("missing %s in %s", want, after)
		}
	}
	if next := testPlan(t, root, p.Policy); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal(next)
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, "gradle/libs.versions.toml") != before {
		t.Fatal("catalog rollback lost bytes")
	}
}

func TestGradleRefusesAmbiguousTargetsAndManagement(t *testing.T) {
	for _, build := range []string{
		`plugins { java }; java { sourceCompatibility = JavaVersion.VERSION_25 }`,
		`plugins { java }; java { sourceCompatibility = providers.gradleProperty("jdk") }`,
		`plugins { java }; dependencies { testImplementation("junit:junit") }`,
		`plugins { java }; dependencies { testImplementation("junit:junit:$unknown") }`,
		`plugins { java }; apply(from = "versions.gradle.kts")`,
		`plugins { kotlin("jvm") }`,
		`plugins { id("org.springframework.boot") version "2.7.18" }`,
	} {
		root := t.TempDir()
		writeTestFile(t, root, "build.gradle.kts", build)
		policy := DefaultPolicy()
		_ = policy.AddPreset("common")
		policy.GradlePlugins = map[string]string{"org.springframework.boot": "3.5.16"}
		p := testPlan(t, root, policy)
		if !p.HasBlockers() {
			t.Fatal("unsafe Gradle build accepted", build)
		}
		if err := Apply(p, false); err == nil {
			t.Fatal("blocked Gradle plan applied")
		}
		if readTestFile(t, root, "build.gradle.kts") != build {
			t.Fatal("blocked plan changed build")
		}
	}
	for _, catalog := range []string{
		"[libraries]\njunit = { module = \"junit:junit\", version.ref = \"unknown\" }\n",
		"[versions]\njunit = { strictly = \"4.12\" }\n",
		"[libraries.junit]\nmodule = \"junit:junit\"\n",
	} {
		root := t.TempDir()
		writeTestFile(t, root, "build.gradle.kts", `plugins { java }`)
		writeTestFile(t, root, "gradle/libs.versions.toml", catalog)
		policy := DefaultPolicy()
		_ = policy.AddPreset("common")
		if p := testPlan(t, root, policy); !p.HasBlockers() {
			t.Fatal("ambiguous catalog accepted", catalog)
		}
	}
}

func TestGradleWrapperStaleAndTamperedPlanProtection(t *testing.T) {
	root := t.TempDir()
	before := `plugins { id 'java' }`
	writeTestFile(t, root, "build.gradle", before)
	wrapper := "distributionUrl=https\\://services.gradle.org/distributions/gradle-8.4-bin.zip\r\ndistributionSha256Sum=" + strings.Repeat("a", 64) + "\r\n"
	writeTestFile(t, root, gradleWrapperProperties, wrapper)
	policy := DefaultPolicy()
	policy.Gradle = "8.14.3"
	p := testPlan(t, root, policy)
	if !p.HasBlockers() {
		t.Fatal("stale checksum accepted")
	}
	policy.GradleSHA256 = strings.Repeat("b", 64)
	p = testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	tampered := p
	tampered.Files = append([]File(nil), p.Files...)
	for i := range tampered.Files {
		if tampered.Files[i].Path == gradleWrapperProperties {
			tampered.Files[i].After += "# unrelated\n"
			tampered.Files[i].AfterSHA = hash([]byte(tampered.Files[i].After))
		}
	}
	if err := Apply(tampered, false); err == nil {
		t.Fatal("tampered wrapper accepted")
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	if next := testPlan(t, root, policy); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal(next)
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, root, gradleWrapperProperties) != wrapper {
		t.Fatal("wrapper rollback changed bytes")
	}
	p = testPlan(t, root, policy)
	writeTestFile(t, root, "build.gradle", before+"\n// user change")
	if err := Apply(p, false); err == nil {
		t.Fatal("stale Gradle build accepted")
	}
}

func TestGradleVerifyChecksRuntimeAndRunsTests(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "build.gradle", `plugins { id 'java' }`)
	wrapper := filepath.Join(root, "gradlew")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Gradle 8.14.3'; echo 'Launcher JVM: 21.0.1'; exit 0; fi\nprintf '%s\\n' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	p := testPlan(t, root, DefaultPolicy())
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if err := Verify(context.Background(), p, VerifyOptions{Stdout: &log, Stderr: &log, Offline: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "clean\ncheck") || !strings.Contains(log.String(), "--offline") || !strings.Contains(log.String(), "--rerun-tasks") {
		t.Fatal(log.String())
	}
	for _, wrong := range []string{strings.Replace(script, "Gradle 8.14.3", "Gradle 8.4", 1), strings.Replace(script, "21.0.1", "17.0.1", 1)} {
		if err := os.WriteFile(wrapper, []byte(wrong), 0755); err != nil {
			t.Fatal(err)
		}
		p = testPlan(t, root, p.Policy)
		if err := Verify(context.Background(), p, VerifyOptions{}); err == nil {
			t.Fatal("wrong Gradle/JDK passed")
		}
	}
}

func TestGradleMapNotationAndCompatibilityFloors(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "build.gradle", `plugins { id 'java'; id 'jacoco' }
 dependencies {
 testImplementation group: 'junit', name: 'junit', version: junitVersion
 implementation(group = "org.ow2.asm", name = "asm", version = "9.3")
 }
 jacoco { toolVersion = "0.8.8" }`)
	writeTestFile(t, root, "gradle.properties", "junitVersion=4.12\n")
	policy := DefaultPolicy()
	if p := testPlan(t, root, policy); !p.HasBlockers() {
		t.Fatal("old ASM accepted")
	}
	_ = policy.AddPreset("common")
	p := testPlan(t, root, policy)
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	after := readTestFile(t, root, "build.gradle")
	if !strings.Contains(after, `version: "4.13.2"`) || strings.Contains(after, `version = "9.3"`) || strings.Contains(after, `toolVersion = "0.8.8"`) {
		t.Fatal(after)
	}
	if next := testPlan(t, root, p.Policy); len(next.Files) != 0 || next.HasBlockers() {
		t.Fatal(next)
	}
	if err := Apply(p, true); err != nil {
		t.Fatal(err)
	}
	delete(policy.Plugins, "org.jacoco:jacoco-maven-plugin")
	if p := testPlan(t, root, policy); !p.HasBlockers() {
		t.Fatal("old JaCoCo accepted by custom policy")
	}
}

func TestGradleBootAndJavaRuntimeCompatibility(t *testing.T) {
	for _, version := range []string{"2.7.18", "3.5.16"} {
		root := t.TempDir()
		writeTestFile(t, root, "build.gradle.kts", `plugins { java; id("org.springframework.boot") version "`+version+`" }`)
		policy, _ := PolicyForJava(25)
		if p := testPlan(t, root, policy); !p.HasBlockers() {
			t.Fatal("Boot/Gradle 9 mismatch accepted", version)
		}
	}
}

func TestGradleNewerWrapperCannotBypassBootCompatibility(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "build.gradle.kts", `plugins { java; id("org.springframework.boot") version "3.5.16" }`)
	writeTestFile(t, root, gradleWrapperProperties, `distributionUrl=https\://services.gradle.org/distributions/gradle-9.1.0-bin.zip`)
	if p := testPlan(t, root, DefaultPolicy()); !p.HasBlockers() {
		t.Fatal("retained Gradle 9 wrapper bypassed Boot 3 compatibility")
	}
}

func TestGradleInstalledOverrideChecksFrameworkCompatibility(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "build.gradle.kts", `plugins { java; id("org.springframework.boot") version "3.5.16" }`)
	executable := filepath.Join(t.TempDir(), "gradle")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Gradle 9.1.0'; echo 'Launcher JVM: 21.0.1'; exit 0; fi\necho 'unexpected build execution'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	p := testPlan(t, root, DefaultPolicy())
	if p.HasBlockers() {
		t.Fatal(p.Findings)
	}
	if err := Apply(p, false); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	err := Verify(context.Background(), p, VerifyOptions{GradleCommand: executable, Stdout: &log, Stderr: &log})
	if err == nil || !strings.Contains(err.Error(), "does not declare Gradle 9 support") || strings.Contains(log.String(), "unexpected build execution") {
		t.Fatal("incompatible installed Gradle accepted", err, log.String())
	}
}
