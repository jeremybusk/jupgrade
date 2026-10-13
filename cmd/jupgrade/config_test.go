package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jupgrade/internal/upgrade"
)

func cleanConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"GRADLE", "GRADLE_SHA256", "GRADLE_PLUGINS", "GRADLE_COMMAND", "GRADLE_USER_HOME", "RECIPES", "RESOLVE_MAVEN", "REPO", "POLICY", "JAVA", "PRESET", "SPRING_BOOT", "DEPENDENCIES", "PLUGINS", "PARENTS", "OUT", "PLAN", "JAVA_HOME", "MAVEN", "MAVEN_SHA256", "MAVEN_REPO", "PROFILES", "OFFLINE", "TIMEOUT"} {
		t.Setenv("JUPGRADE_"+key, "")
	}
	// Empty preset means the build preset; empty timeout is invalid by design.
	t.Setenv("JUPGRADE_TIMEOUT", "20m")
}

func resolvedPolicy(t *testing.T, args ...string) (upgrade.Policy, error) {
	t.Helper()
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	config := bindPolicy(flags)
	if err := parseFlags(flags, args); err != nil {
		return upgrade.Policy{}, err
	}
	return config.resolve()
}

func TestPolicyPrecedenceAndSavedPins(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("JUPGRADE_JAVA", "25")
	t.Setenv("JUPGRADE_PRESET", "common")
	t.Setenv("JUPGRADE_DEPENDENCIES", "junit:junit=4.13.1,internal:util=2.0")
	t.Setenv("JUPGRADE_PLUGINS", "org.apache.maven.plugins:maven-compiler-plugin=3.14.1")
	p, err := resolvedPolicy(t, "--java", "17", "--dependency", "junit:junit=4.13.2", "--dependency", "internal:new=3.0")
	if err != nil {
		t.Fatal(err)
	}
	if p.Java != 17 || p.Dependencies["junit:junit"] != "4.13.2" || p.Dependencies["internal:util"] != "2.0" || p.Dependencies["internal:new"] != "3.0" || p.Plugins["org.apache.maven.plugins:maven-compiler-plugin"] != "3.14.1" {
		t.Fatalf("incorrect precedence: %+v", p)
	}
	if p.Dependencies["org.projectlombok:lombok"] == "" {
		t.Fatal("common preset omitted Lombok")
	}

	repo := t.TempDir()
	before := `<project><dependencies><dependency><groupId>junit</groupId><artifactId>junit</artifactId><version>4.12</version></dependency></dependencies></project>`
	if err := os.WriteFile(filepath.Join(repo, "pom.xml"), []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(t.TempDir(), "plan.json")
	if err := run([]string{"plan", "--repo", repo, "--out", planPath, "--java", "17", "--dependency", "junit:junit=4.13.2"}); err != nil {
		t.Fatal(err)
	}
	// Apply must use the saved policy even if the environment changes later.
	t.Setenv("JUPGRADE_JAVA", "nonsense")
	t.Setenv("JUPGRADE_DEPENDENCIES", "junit:junit=invalid")
	if err := run([]string{"apply", "--plan", planPath}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(repo, "pom.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "<maven.compiler.release>17</maven.compiler.release>") || !strings.Contains(string(after), "<version>4.13.2</version>") {
		t.Fatal(string(after))
	}
	if err := run([]string{"rollback", "--plan", planPath}); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(filepath.Join(repo, "pom.xml"))
	if string(restored) != before {
		t.Fatal("rollback changed original")
	}
}

func TestCompletePolicyOverridesPreset(t *testing.T) {
	cleanConfigEnv(t)
	policy := upgrade.DefaultPolicy()
	policy.Java = 17
	policy.Dependencies["internal:chosen"] = "2.0"
	data, _ := json.Marshal(policy)
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUPGRADE_POLICY", path)
	t.Setenv("JUPGRADE_PRESET", "common")
	p, err := resolvedPolicy(t, "--java", "21", "--dependency", "internal:chosen=3.0")
	if err != nil {
		t.Fatal(err)
	}
	if p.Java != 21 || p.Dependencies["internal:chosen"] != "3.0" || len(p.Dependencies) != 1 {
		t.Fatalf("policy was not a complete replacement: %+v", p)
	}
}

func TestInvalidConfigurationFailsBeforeWriting(t *testing.T) {
	for _, args := range [][]string{
		{"--java", "23"}, {"--java", "latest"}, {"--preset", "everything"},
		{"--dependency", "junit:junit=LATEST"}, {"--dependency", "broken"},
		{"--dependency", "junit:junit=4.14-SNAPSHOT"}, {"--spring-boot", "3.5.x"},
		{"--maven", "4.0.0"}, {"--maven-sha256", "bad"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cleanConfigEnv(t)
			if _, err := resolvedPolicy(t, args...); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
}

func TestSpringBootAndMavenFlags(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("JUPGRADE_SPRING_BOOT", "3.5.9")
	t.Setenv("JUPGRADE_MAVEN", "3.9.10")
	p, err := resolvedPolicy(t, "--spring-boot", "3.5.10", "--maven", "3.9.11")
	if err != nil {
		t.Fatal(err)
	}
	if p.Maven != "3.9.11" || p.Parents["org.springframework.boot:spring-boot-starter-parent"] != "3.5.10" || p.Dependencies["org.springframework.boot:spring-boot-dependencies"] != "3.5.10" || p.Plugins["org.springframework.boot:spring-boot-maven-plugin"] != "3.5.10" {
		t.Fatalf("missing coordinated targets: %+v", p)
	}
}

func TestVerifyEnvironmentAndExplicitFalse(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("JUPGRADE_OFFLINE", "1")
	t.Setenv("JUPGRADE_TIMEOUT", "5m")
	t.Setenv("JUPGRADE_JAVA_HOME", "/jdk")
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	vf := bindVerify(flags)
	offline := bindOffline(flags)
	if err := flags.Parse([]string{"--offline=false"}); err != nil {
		t.Fatal(err)
	}
	opts, err := vf.options(flags, *offline)
	if err != nil || opts.Offline || opts.JavaHome != "/jdk" || opts.Timeout.Minutes() != 5 {
		t.Fatalf("options: %+v, %v", opts, err)
	}
	for _, key := range []string{"JUPGRADE_OFFLINE", "JUPGRADE_TIMEOUT"} {
		t.Setenv(key, "invalid")
		fs := flag.NewFlagSet("verify", flag.ContinueOnError)
		f := bindVerify(fs)
		o := bindOffline(fs)
		if _, err := f.options(fs, *o); err == nil {
			t.Fatal("accepted invalid " + key)
		}
		t.Setenv(key, "1")
	}
}

func TestUpgradeDryRunAndRecoveryRequirement(t *testing.T) {
	cleanConfigEnv(t)
	repo := t.TempDir()
	before := `<project/>`
	path := filepath.Join(repo, "pom.xml")
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"upgrade", "--repo", repo}); err == nil {
		t.Fatal("upgrade ran without recovery plan")
	}
	planPath := filepath.Join(t.TempDir(), "preview.json")
	if err := run([]string{"upgrade", "--repo", repo, "--out", planPath, "--dry-run", "--java", "25"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != before {
		t.Fatal("dry-run wrote POM")
	}
	p, err := upgrade.ReadPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if p.Policy.Java != 25 || len(p.Files) == 0 {
		t.Fatal("dry-run omitted migration")
	}
}

func TestUpgradeAppliesBuildsAndKeepsRecoveryOnFailure(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fails], func(t *testing.T) {
			cleanConfigEnv(t)
			repo := t.TempDir()
			before := `<project/>`
			if err := os.WriteFile(filepath.Join(repo, "pom.xml"), []byte(before), 0600); err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Apache Maven 3.9.11'; echo 'Java version: 21'; exit 0; fi\n"
			if fails {
				script += "exit 1\n"
			} else {
				script += "exit 0\n"
			}
			if err := os.WriteFile(filepath.Join(repo, "mvnw"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "recovery.json")
			err := run([]string{"upgrade", "--repo", repo, "--java", "21", "--out", path})
			if fails {
				if err == nil || !strings.Contains(err.Error(), "rollback") {
					t.Fatal("failure omitted recovery guidance", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(filepath.Join(repo, "pom.xml"))
			if string(data) == before {
				t.Fatal("upgrade did not apply changes")
			}
			if _, err := upgrade.ReadPlan(path); err != nil {
				t.Fatal("missing recovery plan", err)
			}
			if err := run([]string{"rollback", "--plan", path}); err != nil {
				t.Fatal(err)
			}
			data, _ = os.ReadFile(filepath.Join(repo, "pom.xml"))
			if string(data) != before {
				t.Fatal("recovery lost original")
			}
		})
	}
}

func TestGradleAndRecipeOverrides(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("JUPGRADE_GRADLE", "8.13")
	t.Setenv("JUPGRADE_GRADLE_PLUGINS", "org.springframework.boot=3.5.10,example.plugin=1.0")
	t.Setenv("JUPGRADE_RECIPES", "base64")
	p, err := resolvedPolicy(t, "--gradle", "8.14.3", "--gradle-plugin", "org.springframework.boot=3.5.16", "--recipe", "string-format")
	if err != nil {
		t.Fatal(err)
	}
	if p.Gradle != "8.14.3" || p.GradlePlugins["org.springframework.boot"] != "3.5.16" || p.GradlePlugins["example.plugin"] != "1.0" || len(p.Recipes) != 1 || p.Recipes[0] != "string-format" {
		t.Fatal(p)
	}
}
